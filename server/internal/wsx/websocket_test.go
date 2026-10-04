package wsx

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

// net.Pipe gives an in-memory full-duplex connection, so a frame can be written
// and inspected byte for byte without opening a real socket.
func TestWriteFrameHeaderIsRFC6455(t *testing.T) {
	cases := []struct {
		name      string
		opcode    byte
		fin       bool
		payload   []byte
		wantByte0 byte
		// wantByte1 is only meaningful when the 7-bit length form is used.
		wantByte1    byte
		wantExtended int
	}{
		{
			name: "final text frame", opcode: opText, fin: true,
			payload: []byte(`{"kind":"hello"}`),
			// FIN set with opcode text: 0x80 | 0x1 = 0x81.
			wantByte0: 0x81,
			// MASK must be clear on a server frame; this is the whole bug.
			wantByte1: byte(len(`{"kind":"hello"}`)),
		},
		{
			name: "final binary frame", opcode: opBinary, fin: true,
			payload: []byte{0x01, 0x02, 0x03},
			wantByte0: 0x82, wantByte1: 3,
		},
		{
			name: "ping control frame", opcode: opPing, fin: true,
			payload:  nil,
			wantByte0: 0x89, wantByte1: 0,
		},
		{
			name: "continuation frame not final", opcode: opContinuation, fin: false,
			payload: []byte("more"),
			// FIN clear: 0x00 | 0x0 = 0x00.
			wantByte0: 0x00, wantByte1: 4,
		},
		{
			name: "extended 16-bit length", opcode: opText, fin: true,
			payload:   bytes.Repeat([]byte{'x'}, 200),
			wantByte0: 0x81,
			// 126 marker occupies byte 1 whole; the real length follows.
			wantByte1: 126, wantExtended: 200,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, server := net.Pipe()
			c := &Conn{conn: server, MaxMessage: 1 << 20}

			go func() { _ = c.writeFrame(tc.opcode, tc.payload, tc.fin) }()

			head := make([]byte, 2)
			if _, err := readSome(client, head); err != nil {
				t.Fatalf("reading frame header: %v", err)
			}
			if head[0] != tc.wantByte0 {
				t.Errorf("byte0 = 0x%02X, want 0x%02X "+
					"(FIN and opcode must share byte 0)", head[0], tc.wantByte0)
			}
			if head[0]&0x70 != 0 {
				t.Errorf("byte0 = 0x%02X: reserved bits must be zero", head[0])
			}
			if head[1]&0x80 != 0 {
				t.Errorf("byte1 = 0x%02X: MASK must be clear on a "+
					"server-to-client frame", head[1])
			}
			if head[1] != tc.wantByte1 {
				t.Errorf("byte1 = 0x%02X, want 0x%02X", head[1], tc.wantByte1)
			}

			if tc.wantExtended > 0 {
				var ext [2]byte
				if _, err := readSome(client, ext[:]); err != nil {
					t.Fatalf("reading extended length: %v", err)
				}
				if got := int(binary.BigEndian.Uint16(ext[:])); got != tc.wantExtended {
					t.Errorf("extended length = %d, want %d", got, tc.wantExtended)
				}
			}
			_ = client.Close()
		})
	}
}

// TestWriteFrameRoundTrip proves the server can read back what it writes.
//
// Framing bugs are invisible in a symmetric test that uses the same encoder on
// both sides, so this asserts the written bytes against the raw wire format
// rather than against the package's own parser.
func TestWriteFramePayloadIsUnmasked(t *testing.T) {
	client, server := net.Pipe()
	c := &Conn{conn: server, MaxMessage: 1 << 20}

	payload := []byte(`{"ok":true,"nonce":"n1"}`)
	done := make(chan error, 1)
	go func() { done <- c.writeFrame(opText, payload, true) }()

	got := make([]byte, 2+len(payload))
	if _, err := readSome(client, got); err != nil {
		t.Fatalf("reading: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("writeFrame: %v", err)
	}

	if got[0] != 0x81 {
		t.Errorf("byte0 = 0x%02X, want 0x81 (FIN|text)", got[0])
	}
	if got[1]&0x80 != 0 {
		t.Errorf("byte1 = 0x%02X: MASK set on a server frame", got[1])
	}
	if int(got[1]) != len(payload) {
		t.Errorf("length = %d, want %d", got[1], len(payload))
	}
	// The payload must appear verbatim: a masked frame would need a 4-byte
	// key here and the JSON would be scrambled.
	if !bytes.Equal(got[2:], payload) {
		t.Errorf("payload = %q, want %q", got[2:], payload)
	}
	_ = client.Close()
}

// readSome fills buf, tolerating net.Pipe's partial writes.
func readSome(c net.Conn, buf []byte) (int, error) {
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	got := 0
	for got < len(buf) {
		n, err := c.Read(buf[got:])
		got += n
		if err != nil {
			return got, err
		}
	}
	return got, nil
}