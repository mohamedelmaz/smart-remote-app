// Command wsdump performs the WebSocket handshake and hexdumps the first bytes
// the server sends.
//
// It is a diagnostic for frame-format problems: if the server's first byte does
// not carry FIN+opcode as RFC 6455 requires, a conforming client disconnects
// immediately and the symptom looks like "cannot connect" rather than a
// protocol bug.
package main

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9520", "host:port of the server")
	flag.Parse()

	conn, err := net.DialTimeout("tcp", *addr, 5*time.Second)
	if err != nil {
		fmt.Println("dial:", err)
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(8 * time.Second))

	var nonce [16]byte
	_, _ = rand.Read(nonce[:])
	req := "GET /ws HTTP/1.1\r\nHost: " + conn.RemoteAddr().String() +
		"\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: " +
		base64.StdEncoding.EncodeToString(nonce[:]) +
		"\r\nSec-WebSocket-Version: 13\r\n\r\n"

	if _, err := conn.Write([]byte(req)); err != nil {
		fmt.Println("write:", err)
		return
	}

	br := bufio.NewReader(conn)
	line, _ := br.ReadString('\n')
	fmt.Println("handshake:", strings.TrimSpace(line))
	for {
		h, err := br.ReadString('\n')
		if err != nil {
			fmt.Println("headers:", err)
			return
		}
		if strings.TrimSpace(h) == "" {
			break
		}
		fmt.Println("  hdr:", strings.TrimSpace(h))
	}

	raw := make([]byte, 48)
	n, err := io.ReadFull(br, raw)
	if err != nil && n == 0 {
		fmt.Println("no frame bytes received:", err)
		return
	}
	raw = raw[:n]

	fmt.Printf("first %d bytes: ", n)
	for i, b := range raw {
		if i > 0 {
			fmt.Print(" ")
		}
		fmt.Printf("%02X", b)
	}
	fmt.Println()

	if len(raw) >= 2 {
		b0, b1 := raw[0], raw[1]
		fin := b0&0x80 != 0
		op := b0 & 0x0F
		masked := b1&0x80 != 0
		length := int(b1 & 0x7F)

		fmt.Printf("byte0=0x%02X -> FIN=%v opcode=0x%X\n", b0, fin, op)
		fmt.Printf("byte1=0x%02X -> MASK=%v length=%d\n", b1, masked, length)

		switch op {
		case 0x1:
			fmt.Println("opcode = TEXT (correct value, but FIN should be set)")
		case 0x2:
			fmt.Println("opcode = BINARY (correct value, but FIN should be set)")
		default:
			fmt.Printf("opcode = 0x%X (UNEXPECTED)\n", op)
		}
		if fin {
			fmt.Println("VERDICT: FIN bit is set -> framing looks CORRECT")
		} else {
			fmt.Println("VERDICT: FIN bit is MISSING -> framing is MALFORMED")
		}
		if masked {
			fmt.Println("VERDICT: MASK bit is SET on a server frame -> MALFORMED")
		} else {
			fmt.Println("VERDICT: MASK bit is clear -> correct for server-to-client")
		}
	}
}