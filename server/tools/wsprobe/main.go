// Command wsprobe authenticates against a running Smart Remote server and
// reports exactly what the server replies.
//
// It exists because "the app says the PIN is wrong" is otherwise impossible to
// diagnose from the outside: the app collapses every failure into one message,
// so a wrong PIN, a protocol mismatch and a rejected nonce all look identical
// on screen. This probe sends the same first frame the app sends and prints
// the raw reply, which separates those cases immediately.
//
// Usage:
//
//	go run ./tools/wsprobe -addr 127.0.0.1:9520 -pin 368581
//	go run ./tools/wsprobe -addr 192.168.1.36:9520 -pin 368581 -command key.tap
package main

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"
)

// carry holds bytes read past the end of the handshake headers, which belong
// to the server's first frame rather than to the header block.
var carry []byte

func main() {
	addr := flag.String("addr", "127.0.0.1:9520", "host:port of the server")
	pin := flag.String("pin", "", "PIN to authenticate with")
	command := flag.String("command", "", "optional command to send after auth")
	key := flag.String("key", "enter", "key name for -command")
	flag.Parse()

	if *pin == "" {
		fmt.Println("FAIL: -pin is required")
		os.Exit(2)
	}

	if err := run(*addr, *pin, *command, *key); err != nil {
		fmt.Printf("FAIL: %v\n", err)
		os.Exit(1)
	}
}

func run(addr, pin, command, key string) error {
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return fmt.Errorf("cannot reach %s: %w", addr, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	if err := handshake(conn); err != nil {
		return err
	}
	fmt.Printf("websocket upgrade to %s: OK\n", addr)

	// The server pushes a hello event immediately, carrying the PIN it
	// currently expects, so the correct PIN is never a guess.
	hello, err := readFrame(conn)
	if err != nil {
		return fmt.Errorf("reading hello: %w", err)
	}
	fmt.Printf("server hello: %s\n", hello)

	// Send the auth frame.
	msg := map[string]any{"type": "auth", "pin": pin, "nonce": "probe-1"}
	frame, _ := json.Marshal(msg)
	if err := writeFrame(conn, frame); err != nil {
		return fmt.Errorf("sending auth: %w", err)
	}

	// Read until the ack arrives. Authenticating also triggers a
	// clients_changed broadcast to every client including this one, so the
	// first frame back is an event, not the reply. A real client routes on
	// the presence of "ok"; the probe does the same rather than assuming
	// ordering.
	reply, ack, err := awaitAck(conn, "probe-1")
	if err != nil {
		return err
	}
	fmt.Printf("auth reply : %s\n", reply)

	if !ack.OK {
		return fmt.Errorf("server REJECTED the PIN: %s", ack.Err)
	}
	fmt.Printf("RESULT: authentication ACCEPTED (nonce echoed: %q)\n", ack.Nonce)

	if command != "" {
		cmd := map[string]any{"type": command, "nonce": "probe-2"}
		// macro.run names its target in "macroId", not "key". Putting the
		// value in "key" only produced `macro.run requires 'macroId'`,
		// which reads like a server fault when it is really the probe
		// writing to the wrong field.
		if command == "macro.run" {
			cmd["macroId"] = key
		} else {
			cmd["key"] = key
		}
		frame, _ := json.Marshal(cmd)
		if err := writeFrame(conn, frame); err != nil {
			return fmt.Errorf("sending command: %w", err)
		}
		resp, err := readFrame(conn)
		if err != nil {
			return fmt.Errorf("reading command reply: %w", err)
		}
		fmt.Printf("cmd reply : %s\n", resp)
	}
	return nil
}

// ackFrame is the server's reply envelope.
type ackFrame struct {
	OK    bool   `json:"ok"`
	Err   string `json:"err"`
	Nonce string `json:"nonce"`
}

// awaitAck reads frames until one is an ack for wantNonce.
//
// Events ({"kind":...}) are skipped because the server broadcasts
// clients_changed to the authenticating client before replying to it, so the
// ack is not necessarily the first frame received.
func awaitAck(conn net.Conn, wantNonce string) (string, ackFrame, error) {
	for i := 0; i < 8; i++ {
		raw, err := readFrame(conn)
		if err != nil {
			return "", ackFrame{}, fmt.Errorf("reading reply: %w", err)
		}

		var ack ackFrame
		if err := json.Unmarshal([]byte(raw), &ack); err != nil {
			continue // not JSON: skip rather than abort
		}
		// Only an ack has a boolean "ok"; events do not.
		if _, isAck := mustKey(raw, "ok"); !isAck {
			fmt.Printf("  (skipped event) %s\n", raw)
			continue
		}
		if ack.Nonce != wantNonce {
			continue
		}
		return raw, ack, nil
	}
	return "", ackFrame{}, fmt.Errorf("no ack with nonce %q arrived", wantNonce)
}

// mustKey reports whether the JSON object contains key, without relying on a
// full decode into a struct.
func mustKey(raw, key string) (bool, bool) {
	var probe map[string]any
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return false, false
	}
	_, ok := probe[key]
	return ok, true
}

// handshake performs the RFC 6455 opening handshake.
func handshake(conn net.Conn) error {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}

	req := "GET /ws HTTP/1.1\r\n" +
		"Host: " + conn.RemoteAddr().String() + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + base64.StdEncoding.EncodeToString(nonce[:]) + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"

	if _, err := conn.Write([]byte(req)); err != nil {
		return fmt.Errorf("writing handshake: %w", err)
	}

	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading handshake response: %w", err)
	}
	if !strings.Contains(line, "101") {
		return fmt.Errorf("server refused the upgrade: %s", strings.TrimSpace(line))
	}

	for {
		h, err := br.ReadString('\n')
		if err != nil {
			return fmt.Errorf("reading handshake headers: %w", err)
		}
		if strings.TrimSpace(h) == "" {
			break
		}
	}

	// Keep anything already buffered: it is the start of the hello frame.
	if n := br.Buffered(); n > 0 {
		buf := make([]byte, n)
		if _, err := br.Read(buf); err != nil {
			return err
		}
		carry = append(carry, buf...)
	}
	return nil
}

// writeFrame sends a masked text frame, as a client is required to.
func writeFrame(conn net.Conn, payload []byte) error {
	header := []byte{0x81} // FIN + text opcode

	n := len(payload)
	switch {
	case n < 126:
		header = append(header, byte(0x80|n)) // the 0x80 sets the MASK bit
	case n <= 0xFFFF:
		header = append(header, 0x80|126)
		var b [2]byte
		binary.BigEndian.PutUint16(b[:], uint16(n))
		header = append(header, b[:]...)
	default:
		header = append(header, 0x80|127)
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(n))
		header = append(header, b[:]...)
	}

	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return err
	}
	header = append(header, mask[:]...)

	masked := make([]byte, n)
	for i := 0; i < n; i++ {
		masked[i] = payload[i] ^ mask[i%4]
	}
	_, err := conn.Write(append(header, masked...))
	return err
}

// readFrame reads one server frame and returns its payload as text.
//
// Server-to-client frames are never masked, but the mask bit is honoured
// anyway so the reader stays correct if that ever changes.
func readFrame(conn net.Conn) (string, error) {
	head, err := readFull(conn, 2)
	if err != nil {
		return "", err
	}

	opcode := head[0] & 0x0F
	masked := head[1]&0x80 != 0
	length := int(head[1] & 0x7F)

	switch length {
	case 126:
		b, err := readFull(conn, 2)
		if err != nil {
			return "", err
		}
		length = int(binary.BigEndian.Uint16(b))
	case 127:
		b, err := readFull(conn, 8)
		if err != nil {
			return "", err
		}
		length = int(binary.BigEndian.Uint64(b))
	}

	var mask [4]byte
	if masked {
		m, err := readFull(conn, 4)
		if err != nil {
			return "", err
		}
		copy(mask[:], m)
	}

	// Control frames carry no application payload; surface them as errors
	// rather than returning an empty string the caller would misread.
	switch opcode {
	case 0x8:
		return "", fmt.Errorf("server closed the connection")
	case 0x9:
		return "", fmt.Errorf("server sent a ping")
	case 0xA:
		return "", fmt.Errorf("server sent a pong")
	}

	payload, err := readFull(conn, length)
	if err != nil {
		return "", err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return string(payload), nil
}

// readFull reads exactly n bytes, consuming any handshake carry first.
func readFull(conn net.Conn, n int) ([]byte, error) {
	buf := make([]byte, n)

	if len(carry) > 0 {
		copied := copy(buf, carry)
		carry = carry[copied:]
		if copied == n {
			return buf, nil
		}
		buf = buf[copied:]
	}

	if _, err := io.ReadFull(conn, buf); err != nil {
		return nil, err
	}
	return buf, nil
}