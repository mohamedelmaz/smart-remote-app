package remote

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ProtocolVersion is bumped when the wire format changes incompatibly.
// The mobile app checks it after pairing and refuses to operate on a
// mismatch rather than sending commands a newer server would misread.
const ProtocolVersion = "1.0"

// Command names accepted from clients.
const (
	CmdMouseMove     = "mouse.move"
	CmdMouseClick    = "mouse.click"
	CmdMouseDown     = "mouse.down"
	CmdMouseUp       = "mouse.up"
	CmdMouseScroll   = "mouse.scroll"
	CmdMousePosition = "mouse.position"
	CmdKeyTap        = "key.tap"
	CmdKeyChord      = "key.chord"
	CmdKeyLatch      = "key.latch"
	CmdText          = "text.type"
	CmdMacroRun      = "macro.run"
	CmdVolumeSet     = "audio.volume"
	CmdStreamConfig  = "stream.config"
	CmdCursorShow    = "cursor.show"
	CmdPing          = "ping"
	CmdAudioStart    = "audio.start"
	CmdAudioStop     = "audio.stop"
	CmdInputRelease  = "input.release"
)

// Command is a single instruction from a client.
//
// Type is the dotted command name; the remaining fields are populated only
// as the command requires. Keeping one flat struct rather than an interface
// hierarchy keeps decoding on the hot path allocation-light and makes it
// trivial to log a command as JSON.
type Command struct {
	Type string `json:"type"`

	// Mouse
	DX      int32  `json:"dx,omitempty"`
	DY      int32  `json:"dy,omitempty"`
	Button  string `json:"button,omitempty"`
	WheelDX int32  `json:"wheelDx,omitempty"`
	WheelDY int32  `json:"wheelDy,omitempty"`

	// Keyboard
	Key  string   `json:"key,omitempty"`
	Mods []string `json:"mods,omitempty"`
	Text string   `json:"text,omitempty"`
	Down bool     `json:"down,omitempty"`
	X    int32    `json:"x,omitempty"`
	Y    int32    `json:"y,omitempty"`

	// Audio / volume
	Volume int `json:"volume,omitempty"`

	// Stream
	FPS     int    `json:"fps,omitempty"`
	Quality int    `json:"quality,omitempty"`
	Format  string `json:"format,omitempty"`

	// Macro
	MacroID string `json:"macroId,omitempty"`

	// Cursor
	Visible bool `json:"visible,omitempty"`

	// Nonce echoes the client's identifier so replies can be correlated.
	Nonce string `json:"nonce,omitempty"`
}

// Ack is the server's reply to a command.
//
// Ok=false carries Err so the client can surface a real failure instead of
// silently assuming success, which is what makes the mobile UI trustworthy.
type Ack struct {
	OK    bool   `json:"ok"`
	Err   string `json:"err,omitempty"`
	Nonce string `json:"nonce,omitempty"`
}

// Event is an unsolicited server push such as an input-blocked notice.
type Event struct {
	Kind string `json:"kind"`
	Msg  string `json:"msg,omitempty"`
	Data any    `json:"data,omitempty"`
}

// ParseCommand decodes and validates a command frame.
func ParseCommand(raw []byte) (Command, error) {
	var c Command
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("remote: malformed command: %w", err)
	}
	if c.Type == "" {
		return c, fmt.Errorf("remote: command is missing 'type'")
	}
	return c, nil
}

// Normalize lowercases a command name for tolerant matching.
func (c Command) Normalize() Command {
	c.Type = strings.ToLower(strings.TrimSpace(c.Type))
	return c
}

// Validate checks required fields and clamps numeric ranges.
func (c Command) Validate() error {
	switch c.Type {
	case CmdMouseMove, CmdMouseClick, CmdMouseDown, CmdMouseUp, CmdMouseScroll:
		if c.Type != CmdMouseMove && c.Button == "" {
			return fmt.Errorf("remote: %s requires 'button'", c.Type)
		}
	case CmdKeyTap:
		if c.Key == "" {
			return fmt.Errorf("remote: key.tap requires 'key'")
		}
		if _, err := resolveKey(c.Key); err != nil {
			return err
		}
	case CmdKeyChord:
		if c.Key == "" {
			return fmt.Errorf("remote: key.chord requires 'key'")
		}
		if _, err := resolveKey(c.Key); err != nil {
			return err
		}
		for _, m := range c.Mods {
			if _, err := resolveKey(m); err != nil {
				return fmt.Errorf("remote: unknown modifier %q", m)
			}
		}
	case CmdKeyLatch:
		if c.Key == "" {
			return fmt.Errorf("remote: key.latch requires 'key'")
		}
	case CmdText:
		if c.Text == "" {
			return fmt.Errorf("remote: text.type requires 'text'")
		}
	case CmdMacroRun:
		if c.MacroID == "" {
			return fmt.Errorf("remote: macro.run requires 'macroId'")
		}
	case CmdVolumeSet:
		if c.Volume < 0 || c.Volume > 100 {
			return fmt.Errorf("remote: volume %d out of range 0..100", c.Volume)
		}
	case CmdStreamConfig:
		if c.FPS < 1 || c.FPS > 60 {
			return fmt.Errorf("remote: fps %d out of range 1..60", c.FPS)
		}
		if c.Quality < 10 || c.Quality > 100 {
			return fmt.Errorf("remote: quality %d out of range 10..100", c.Quality)
		}
	case CmdMousePosition, CmdPing, CmdAudioStart, CmdAudioStop, CmdCursorShow,
		CmdInputRelease:
		// no required fields
	default:
		return fmt.Errorf("remote: unknown command %q", c.Type)
	}
	return nil
}
