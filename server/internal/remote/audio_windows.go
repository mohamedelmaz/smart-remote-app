//go:build windows

package remote

import (
	"sync"
	"syscall"
	"unsafe"
)

// AudioSink is the interface the dispatcher uses to play streamed voice
// audio from the phone through the PC speakers.
//
// Implementations must tolerate being started and stopped repeatedly, and
// must never block the caller's goroutine for long: the dispatcher runs on
// the command path.
type AudioSink interface {
	Start()
	Stop()
	// Write feeds PCM bytes. It may be called from the websocket reader
	// goroutine.
	Write(pcm []byte) error
	// Active reports whether the sink is currently playing.
	Active() bool
	// SetVolume adjusts output volume, 0..100.
	SetVolume(pct int) error
	// Close permanently releases the output device.
	Close() error
}

var (
	winmm                = syscall.NewLazyDLL("winmm.dll")
	procWaveOutOpen      = winmm.NewProc("waveOutOpen")
	procWaveOutClose     = winmm.NewProc("waveOutClose")
	procWaveOutPrepare   = winmm.NewProc("waveOutPrepareHeader")
	procWaveOutUnprepare = winmm.NewProc("waveOutUnprepareHeader")
	procWaveOutWrite     = winmm.NewProc("waveOutWrite")
	procWaveOutReset     = winmm.NewProc("waveOutReset")
	procWaveOutRestart   = winmm.NewProc("waveOutRestart")
	procWaveOutSetVolume = winmm.NewProc("waveOutSetVolume")
)

// waveOut constants.
const (
	waveFormatPCM   = 1
	mmsyserrNoError = 0

	// waveCallbackNull keeps the driver from calling back into Go, which
	// avoids re-entering the runtime from an audio thread.
	waveCallbackNull = 0

	whdrDone = 0x00000001
)

// waveMapperNoDriver selects the default playback device. It is the
// WAVE_MAPPER constant, which is signed (-1).
const waveMapperNoDriver = ^uintptr(0)

// WaveFormatEx from mmsystem.h.
type waveFormatEx struct {
	Format         uint16
	Channels       uint16
	SamplesPerSec  uint32
	AvgBytesPerSec uint32
	BlockAlign     uint16
	BitsPerSample  uint16
	Extra          uint16
}

// waveHdr from mmsystem.h.
type waveHdr struct {
	lpData          uintptr
	dwBufferLength  uint32
	dwBytesRecorded uint32
	dwUser          uintptr
	dwFlags         uint32
	dwLoops         uint32
	lpNext          uintptr
	Reserved        uintptr
}

// PCM format used for the voice stream.
//
// The phone sends 16-bit mono 16 kHz. That is small enough to stream over
// WiFi at low latency while still being intelligible, and winmm plays it
// without resampling on any Windows audio stack.
const (
	VoiceSampleRate = 16000
	VoiceChannels   = 1
	VoiceBits       = 16
	voiceBlockAlign = VoiceChannels * VoiceBits / 8
)

// WaveOutSink plays PCM through the legacy waveOut API.
//
// waveOut is used rather than WASAPI because it needs no COM apartment and
// no external package, which keeps the server a single dependency-free
// binary as required.
type WaveOutSink struct {
	mu     sync.Mutex
	device syscall.Handle
	format *waveFormatEx
	volume int
	active bool
	closed bool
	// queued holds the prepared headers still owned by the driver. A header
	// must not be unprepared while the driver may still be reading it, and
	// the byte buffer it points at must stay alive, so both are retained.
	queued []*pendingBuffer
}

// pendingBuffer pairs a prepared header with the PCM it points at.
type pendingBuffer struct {
	hdr waveHdr
	pcm []byte
}

// NewWaveOutSink opens the default playback device.
func NewWaveOutSink() (*WaveOutSink, error) {
	format := &waveFormatEx{
		Format:        waveFormatPCM,
		Channels:      VoiceChannels,
		SamplesPerSec: VoiceSampleRate,
		BitsPerSample: VoiceBits,
		BlockAlign:    voiceBlockAlign,
	}
	format.AvgBytesPerSec = VoiceSampleRate * uint32(voiceBlockAlign)

	s := &WaveOutSink{format: format, volume: 100}

	var device uintptr
	ret, _, _ := procWaveOutOpen.Call(
		uintptr(unsafe.Pointer(&device)),
		uintptr(waveMapperNoDriver),
		uintptr(unsafe.Pointer(format)),
		0, // CALLBACK_NULL
		0, // no instance
		waveCallbackNull,
	)
	if ret != mmsyserrNoError {
		return nil, errNoDevice(ret)
	}
	s.device = syscall.Handle(device)
	return s, nil
}

// Start marks the sink active. Opening the device lazily would add latency
// to the first spoken word, so it is opened in NewWaveOutSink instead.
func (s *WaveOutSink) Start() {
	s.mu.Lock()
	s.active = true
	s.mu.Unlock()
}

// Stop resets the device, discarding any queued audio.
func (s *WaveOutSink) Stop() {
	s.mu.Lock()
	s.active = false
	device := s.device
	s.mu.Unlock()

	if device != 0 {
		procWaveOutReset.Call(uintptr(device), 0)
	}
}

// Active reports whether the sink is playing.
func (s *WaveOutSink) Active() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// SetVolume adjusts output volume, 0..100.
func (s *WaveOutSink) SetVolume(pct int) error {
	if pct < 0 || pct > 100 {
		return errVolumeRange
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.volume = pct

	device := s.device
	if device == 0 {
		return errAudioClosed
	}
	// waveOutSetVolume takes a DWORD packing left in the high word and
	// right in the low word, each 0..0xFFFF.
	gain := uint32(pct) * 0xFFFF / 100
	packed := gain<<16 | gain
	ret, _, _ := procWaveOutSetVolume.Call(uintptr(device), uintptr(packed))
	if ret != mmsyserrNoError {
		return errNoDevice(ret)
	}
	return nil
}

// Close releases the device and stops playback.
func (s *WaveOutSink) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	device := s.device
	s.device = 0
	s.mu.Unlock()

	if device != 0 {
		procWaveOutReset.Call(uintptr(device), 0)
		return wrapCall(procWaveOutClose.Call(uintptr(device)))
	}
	return nil
}

// Write queues PCM for playback.
//
// The buffer is copied into a fresh allocation and its header is retired
// through a WHDR_DONE poll on a fixed number of slots. Using a fixed slot
// ring rather than an unbounded queue means a stalled audio device throttles
// the producer instead of growing memory without limit.
func (s *WaveOutSink) Write(pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}
	// Only whole sample frames can be played; drop a trailing partial frame
	// rather than playing a click.
	usable := len(pcm) - (len(pcm) % voiceBlockAlign)
	if usable == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.device == 0 {
		return errAudioClosed
	}

	// Reclaim any header the driver has finished with.
	for len(s.queued) > 0 && s.queued[0].hdr.dwFlags&whdrDone != 0 {
		pb := s.queued[0]
		procWaveOutUnprepare.Call(uintptr(s.device), uintptr(unsafe.Pointer(&pb.hdr)), 0)
		s.queued = s.queued[1:]
	}

	if len(s.queued) >= maxQueuedHeaders {
		// The device is not draining; drop the oldest frame to keep latency
		// bounded. Dropping audio is better than growing without limit.
		oldest := s.queued[0]
		procWaveOutReset.Call(uintptr(s.device), 0)
		procWaveOutUnprepare.Call(uintptr(s.device), uintptr(unsafe.Pointer(&oldest.hdr)), 0)
		s.queued = s.queued[1:]
	}

	buf := make([]byte, usable)
	copy(buf, pcm[:usable])

	pb := &pendingBuffer{
		hdr: waveHdr{
			lpData:         uintptr(unsafe.Pointer(&buf[0])),
			dwBufferLength: uint32(usable),
		},
		pcm: buf,
	}
	ret, _, _ := procWaveOutPrepare.Call(
		uintptr(s.device), uintptr(unsafe.Pointer(&pb.hdr)), uintptr(unsafe.Sizeof(pb.hdr)))
	if ret != mmsyserrNoError {
		return errNoDevice(ret)
	}

	ret, _, _ = procWaveOutWrite.Call(
		uintptr(s.device), uintptr(unsafe.Pointer(&pb.hdr)), uintptr(usable))
	if ret != mmsyserrNoError {
		procWaveOutUnprepare.Call(uintptr(s.device), uintptr(unsafe.Pointer(&pb.hdr)), 0)
		return errNoDevice(ret)
	}

	// The header and its PCM must stay valid until the driver sets
	// WHDR_DONE, so retain both until the next reclaim pass.
	s.queued = append(s.queued, pb)
	procWaveOutRestart.Call(uintptr(s.device))
	return nil
}

// maxQueuedHeaders bounds outstanding write buffers to roughly 250ms of
// audio at 16kHz mono 16-bit, which is plenty to absorb jitter.
const maxQueuedHeaders = 8
