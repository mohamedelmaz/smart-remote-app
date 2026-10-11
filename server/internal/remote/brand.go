package remote

import (
	_ "embed"
	"log"
)

// AppVersion is the human-facing product version.
//
// It is deliberately separate from ProtocolVersion in protocol.go: the protocol
// version changes only when the wire format does, whereas this changes with
// every release. The mobile app shows this in its About sheet, so it must match
// the version in mobile/pubspec.yaml.
const AppVersion = "1.0.4"

// Developer is the author shown in the server's own version reporting.
//
// Kept in sync with `developer:` in mobile/pubspec.yaml so the dashboard, the
// status endpoint and the phone all attribute the software to the same person.
const Developer = "elmamo"

// appIcon is the multi-resolution Windows icon, embedded so the tray can never
// be handed a nil or truncated icon at runtime. A missing icon is the single
// most common reason a tray app looks like it has no icon at all: Windows
// draws a blank entry, which is indistinguishable from a broken tray.
//
// The same asset is compiled into the EXE by the .syso resource, so the
// Explorer/taskbar icon and the tray icon can never drift apart.
//
//go:embed assets/smartremote.ico
var appIcon []byte

// trayIconData returns the bytes for systray.SetIcon.
//
// It never returns nil. If the embedded asset is somehow empty (a corrupt
// checkout, a packaging mistake), a minimal valid single-image ICO is generated
// at runtime so the tray entry is at least visible and the user can still quit
// the server, rather than being left with an invisible process.
func trayIconData(logger *log.Logger) []byte {
	// Guard the logger: this is called from the tray setup path, and a nil
	// logger would turn a missing-icon fallback into a panic. A tray that
	// crashes on startup is strictly worse than one with a plain icon.
	if logger == nil {
		logger = log.Default()
	}

	if len(appIcon) > 0 {
		logger.Printf("tray: using embedded icon (%d bytes)", len(appIcon))
		return appIcon
	}
	logger.Printf("tray: embedded icon missing, using generated fallback")
	return fallbackIcon()
}

// fallbackIcon builds a minimal 32x32 32-bit ICO in the brand background
// colour. It is a last-resort path only; it is intentionally plain because its
// job is to be *visible*, not pretty.
func fallbackIcon() []byte {
	const (
		size       = 32
		biSize     = 40
		bytesPerPx = 4
		dirSize    = 6
		entrySize  = 16
	)

	px := make([]byte, size*size*bytesPerPx)
	for i := 0; i < len(px); i += bytesPerPx {
		// BGRA for #0A0E1A: opaque deep-void background.
		px[i+0] = 0x1A
		px[i+1] = 0x0E
		px[i+2] = 0x0A
		px[i+3] = 0xFF
	}

	// 1bpp AND mask, each row padded to a 4-byte boundary.
	maskRow := ((size + 31) / 32) * 4
	maskSize := maskRow * size
	imgSize := biSize + len(px) + maskSize

	out := make([]byte, dirSize+entrySize, dirSize+entrySize+imgSize)
	// ICONDIR: reserved 0, type 1 (icon), count 1.
	out[2], out[3], out[4], out[5] = 1, 0, 1, 0

	e := out[dirSize:]
	e[0], e[1] = size, size // dimensions
	putLE16(e[4:], 1)       // planes
	putLE16(e[6:], 32)      // bits per pixel
	putLE32(e[8:], uint32(imgSize))
	putLE32(e[12:], uint32(dirSize+entrySize))

	bi := make([]byte, biSize)
	putLE32(bi[0:], biSize)
	putLE32(bi[4:], size)
	putLE32(bi[8:], size*2) // height is doubled: XOR bitmap + AND mask
	putLE16(bi[12:], 1)
	putLE16(bi[14:], 32)
	putLE32(bi[20:], uint32(len(px)))

	out = append(out, bi...)
	out = append(out, px...)

	// An all-ones AND mask means nothing is transparent.
	mask := make([]byte, maskSize)
	for i := range mask {
		mask[i] = 0xFF
	}
	return append(out, mask...)
}

func putLE16(b []byte, v uint16) {
	b[0], b[1] = byte(v), byte(v>>8)
}

func putLE32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}
