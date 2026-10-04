// Package wsx implements the server half of the WebSocket protocol
// (RFC 6455) using only the Go standard library.
//
// It supports the subset the remote-control protocol needs: text and binary
// data frames, fragmentation, ping/pong, the close handshake, and a
// per-message payload limit.
package wsx

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const acceptGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// Opcodes as defined by RFC 6455 section 5.2.
const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA
)

// Exported opcodes, so callers can distinguish message kinds without
// duplicating the numeric values.
const (
	OpContinuation = opContinuation
	OpText         = opText
	OpBinary       = opBinary
	OpClose        = opClose
	OpPing         = opPing
	OpPong         = opPong
)

// Close status codes used by this package.
const (
	CloseNormal          = 1000
	CloseGoingAway       = 1001
	CloseProtocolError   = 1002
	CloseUnsupportedData = 1003
	CloseMessageTooBig   = 1009
)

// ErrClosed is returned once the connection is no longer usable.
var ErrClosed = errors.New("wsx: connection closed")

// CloseError reports that the peer sent a Close frame.
type CloseError struct {
	Code int
	Text string
}

func (e *CloseError) Error() string {
	return fmt.Sprintf("wsx: peer closed (%d): %s", e.Code, e.Text)
}

// Conn is an established WebSocket connection.
type Conn struct {
	conn net.Conn
	br   *bufio.Reader

	wmu    sync.Mutex
	cmu    sync.Mutex
	closed bool

	// MaxMessage bounds a single reassembled message so a misbehaving peer
	// cannot exhaust server memory.
	MaxMessage int64

	// OnPong, when set, is invoked for each Pong payload received.
	OnPong func([]byte)
}

// IsWebSocketUpgrade reports whether r is a WebSocket handshake request.
func IsWebSocketUpgrade(r *http.Request) bool {
	if !strings.EqualFold(r.Method, http.MethodGet) {
		return false
	}
	if !headerContainsToken(r.Header.Get("Connection"), "upgrade") {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket")
}

func headerContainsToken(header, token string) bool {
	for _, part := range strings.Split(header, ",") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}

// Upgrade completes the server-side handshake and returns a Conn.
//
// It hijacks the underlying connection, so the ResponseWriter must not be
// used for anything else after a successful call.
func Upgrade(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	if r.ProtoMajor < 1 || (r.ProtoMajor == 1 && r.ProtoMinor < 1) {
		return nil, errors.New("wsx: unsupported HTTP version")
	}
	if !IsWebSocketUpgrade(r) {
		return nil, errors.New("wsx: not a websocket upgrade request")
	}
	if r.Header.Get("Sec-Websocket-Version") != "13" {
		return nil, errors.New("wsx: unsupported websocket version")
	}
	key := r.Header.Get("Sec-Websocket-Key")
	if key == "" {
		return nil, errors.New("wsx: missing Sec-WebSocket-Key")
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("wsx: response writer does not support hijacking")
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		return nil, fmt.Errorf("wsx: hijack: %w", err)
	}

	sum := sha1.Sum([]byte(key + acceptGUID))
	accept := base64.StdEncoding.EncodeToString(sum[:])

	var resp strings.Builder
	resp.WriteString("HTTP/1.1 101 Switching Protocols\r\n")
	resp.WriteString("Upgrade: websocket\r\n")
	resp.WriteString("Connection: Upgrade\r\n")
	resp.WriteString("Sec-WebSocket-Accept: " + accept + "\r\n\r\n")

	// Best-effort deadline so a stalled peer cannot leak the connection.
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(conn, resp.String()); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("wsx: write handshake: %w", err)
	}
	_ = conn.SetWriteDeadline(time.Time{})

	return &Conn{
		conn:       conn,
		br:         brw.Reader,
		MaxMessage: 8 << 20,
	}, nil
}

// SendText writes a text message, fragmenting it if necessary.
func (c *Conn) SendText(p []byte) error { return c.send(opText, p) }

// SendBinary writes a binary message.
func (c *Conn) SendBinary(p []byte) error { return c.send(opBinary, p) }

// Ping sends a ping frame with an optional payload.
func (c *Conn) Ping(payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closed {
		return ErrClosed
	}
	return c.writeFrame(opPing, payload, true)
}

func (c *Conn) send(opcode byte, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closed {
		return ErrClosed
	}
	if opcode == opText && !utf8.Valid(payload) {
		return errors.New("wsx: payload is not valid UTF-8")
	}

	const maxFragment = 64 << 10
	if len(payload) <= maxFragment {
		return c.writeFrame(opcode, payload, true)
	}
	// Fragmented message: the first frame carries the data opcode and the
	// remainder are continuation frames.
	first := payload[:maxFragment]
	if err := c.writeFrame(opcode, first, false); err != nil {
		return err
	}
	rest := payload[maxFragment:]
	for len(rest) > maxFragment {
		if err := c.writeFrame(opContinuation, rest[:maxFragment], false); err != nil {
			return err
		}
		rest = rest[maxFragment:]
	}
	return c.writeFrame(opContinuation, rest, true)
}

// writeFrame emits one frame.
//
// RFC 6455 packs the header into two bytes:
//
//	byte 0: FIN (bit 7) | RSV (bits 6-4) | Opcode (bits 3-0)
//	byte 1: MASK (bit 7) | Payload length (bits 6-0)
//
// FIN and the opcode therefore share byte 0, and the MASK bit lives in
// byte 1. Writing the opcode alone into byte 0 and then FIN|length into
// byte 1 produced a frame with FIN clear and MASK set: a strict client reads
// the first four payload bytes as a masking key, fails to decode the rest,
// and closes the connection immediately. The symptom was an app that paired
// and authenticated correctly on the server, then dropped the socket without
// ever seeing the ack.
func (c *Conn) writeFrame(opcode byte, payload []byte, fin bool) error {
	header := make([]byte, 0, 14)

	// Byte 0: FIN and opcode together.
	b0 := opcode & 0x0F
	if fin {
		b0 |= 0x80
	}
	header = append(header, b0)

	// Byte 1: MASK clear (a server must never mask) plus the 7-bit length.
	// The extended 16/64-bit length forms replace byte 1 entirely, so the
	// marker value is written rather than OR'd in.
	length := len(payload)
	switch {
	case length < 126:
		header = append(header, byte(length))
	case length <= 0xFFFF:
		header = append(header, 126)
		var b [2]byte
		binary.BigEndian.PutUint16(b[:], uint16(length))
		header = append(header, b[:]...)
	default:
		header = append(header, 127)
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(length))
		header = append(header, b[:]...)
	}

	_ = c.conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
	if _, err := c.conn.Write(append(header, payload...)); err != nil {
		return fmt.Errorf("wsx: write frame: %w", err)
	}
	_ = c.conn.SetWriteDeadline(time.Time{})
	return nil
}

// Close performs the closing handshake and closes the socket.
func (c *Conn) Close() error {
	c.cmu.Lock()
	if c.closed {
		c.cmu.Unlock()
		return nil
	}
	c.cmu.Unlock()

	c.wmu.Lock()
	if !c.closed {
		var payload [2]byte
		binary.BigEndian.PutUint16(payload[:], uint16(CloseNormal))
		_ = c.conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
		_ = c.writeFrame(opClose, payload[:], true)
		c.closed = true
	}
	c.wmu.Unlock()
	return c.conn.Close()
}

// Message is a decoded application message.
type Message struct {
	// Opcode is opText or opBinary for the assembled message.
	Opcode byte
	Data   []byte
}

// ReadMessage reads and reassembles the next application message.
//
// Control frames are handled transparently: pings are answered with pongs,
// pongs are surfaced via OnPong, and a close frame triggers the closing
// handshake and returns a *CloseError.
func (c *Conn) ReadMessage() (Message, error) {
	var (
		buf    []byte
		dataOp byte
		assemb bool
	)
	maxSize := c.MaxMessage
	if maxSize <= 0 {
		maxSize = 8 << 20
	}

	for {
		fin, opcode, payload, err := c.readFrame()
		if err != nil {
			return Message{}, err
		}

		// Control frames may be interleaved and must not be fragmented.
		if opcode >= 0x8 {
			switch opcode {
			case opPing:
				if err := c.send(opPong, payload); err != nil {
					return Message{}, err
				}
			case opPong:
				if c.OnPong != nil {
					c.OnPong(payload)
				}
			case opClose:
				code, text := parseClosePayload(payload)
				c.wmu.Lock()
				if !c.closed {
					var echo [2]byte
					binary.BigEndian.PutUint16(echo[:], uint16(code))
					_ = c.writeFrame(opClose, append(echo[:], text...), true)
					c.closed = true
				}
				c.wmu.Unlock()
				_ = c.conn.Close()
				return Message{}, &CloseError{Code: code, Text: text}
			}
			continue
		}

		switch opcode {
		case opText, opBinary:
			if assemb {
				return Message{}, c.protocolViolation("new data frame during fragmented message")
			}
			assemb = true
			dataOp = opcode
			buf = append(buf[:0], payload...)
		case opContinuation:
			if !assemb {
				return Message{}, c.protocolViolation("continuation without start frame")
			}
			buf = append(buf, payload...)
		default:
			return Message{}, c.protocolViolation(fmt.Sprintf("unknown opcode %#x", opcode))
		}

		if int64(len(buf)) > maxSize {
			_ = c.Close()
			return Message{}, fmt.Errorf("wsx: message exceeds %d bytes", maxSize)
		}
		if fin {
			out := make([]byte, len(buf))
			copy(out, buf)
			return Message{Opcode: dataOp, Data: out}, nil
		}
	}
}

func (c *Conn) protocolViolation(msg string) error {
	_ = c.Close()
	return fmt.Errorf("wsx: protocol error: %s", msg)
}

func parseClosePayload(p []byte) (int, string) {
	if len(p) < 2 {
		return CloseNormal, ""
	}
	code := int(binary.BigEndian.Uint16(p[:2]))
	switch code {
	case CloseNormal, CloseGoingAway, CloseProtocolError,
		CloseUnsupportedData, CloseMessageTooBig:
	default:
		code = CloseProtocolError
	}
	return code, string(p[2:])
}

// readFrame reads exactly one frame from the peer, unmasking the payload.
func (c *Conn) readFrame() (fin bool, opcode byte, payload []byte, err error) {
	var head [2]byte
	if _, err = io.ReadFull(c.br, head[:]); err != nil {
		return false, 0, nil, err
	}

	fin = head[0]&0x80 != 0
	if head[0]&0x70 != 0 {
		return false, 0, nil, c.protocolViolation("reserved bits set without negotiated extension")
	}
	opcode = head[0] & 0x0F
	masked := head[1]&0x80 != 0
	length := uint64(head[1] & 0x7F)

	switch length {
	case 126:
		var b [2]byte
		if _, err = io.ReadFull(c.br, b[:]); err != nil {
			return false, 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err = io.ReadFull(c.br, b[:]); err != nil {
			return false, 0, nil, err
		}
		length = binary.BigEndian.Uint64(b[:])
	}
	if opcode >= 0x8 && (length > 125 || !fin) {
		return false, 0, nil, c.protocolViolation("invalid control frame")
	}
	// Client-to-server frames MUST be masked.
	if !masked {
		return false, 0, nil, c.protocolViolation("client frame is not masked")
	}
	if length > 1<<26 {
		return false, 0, nil, c.protocolViolation("frame too large")
	}

	var maskKey [4]byte
	if _, err = io.ReadFull(c.br, maskKey[:]); err != nil {
		return false, 0, nil, err
	}

	payload = make([]byte, length)
	if _, err = io.ReadFull(c.br, payload); err != nil {
		return false, 0, nil, err
	}
	for i := range payload {
		payload[i] ^= maskKey[i%4]
	}
	return fin, opcode, payload, nil
}

// SetReadDeadline sets a deadline on the underlying socket, used for
// liveness checks on idle connections.
func (c *Conn) SetReadDeadline(t time.Time) error { return c.conn.SetReadDeadline(t) }

// SetPongHandler installs a hook invoked for each Pong payload received.
func (c *Conn) SetPongHandler(fn func([]byte)) { c.OnPong = fn }
