package voice

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/gen2brain/malgo"
	"openuai/internal/logger"
)

const (
	micSampleRate = 16000
	micChannels   = 1
)

// AudioDevice represents an available audio input device.
type AudioDevice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Mic captures mono 16-bit PCM at 16 kHz from the system microphone using
// miniaudio (malgo). miniaudio links each OS's native audio API — WASAPI on
// Windows, CoreAudio on macOS, ALSA/PulseAudio on Linux — and is compiled into
// the binary, so capture needs no external tools (parecord, ffmpeg, …).
type Mic struct {
	ctx    *malgo.AllocatedContext
	device *malgo.Device

	mu    sync.Mutex
	buf   bytes.Buffer // accumulated s16le PCM since the last Reset
	level int32        // atomic: most recent capture level, 0-100
}

// OpenMic starts capturing from the named device (as returned by ListDevices),
// or the system default when name is "". Audio accumulates in an internal
// buffer until WAV() snapshots it; the device keeps running until Close.
func OpenMic(name string) (*Mic, error) {
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, fmt.Errorf("init audio context: %w", err)
	}

	cfg := malgo.DefaultDeviceConfig(malgo.Capture)
	cfg.Capture.Format = malgo.FormatS16
	cfg.Capture.Channels = micChannels
	cfg.SampleRate = micSampleRate
	cfg.Alsa.NoMMap = 1

	// devInfos must stay alive until InitDevice has consumed the DeviceID
	// pointer below (it points into the slice's backing array).
	var devInfos []malgo.DeviceInfo
	if name != "" {
		devInfos, _ = ctx.Devices(malgo.Capture)
		for i := range devInfos {
			if devInfos[i].Name() == name {
				cfg.Capture.DeviceID = devInfos[i].ID.Pointer()
				break
			}
		}
		if cfg.Capture.DeviceID == nil {
			logger.Info("Mic: device %q not found — using default", name)
		}
	}

	m := &Mic{ctx: ctx}
	device, err := malgo.InitDevice(ctx.Context, cfg, malgo.DeviceCallbacks{Data: m.onFrames})
	runtime.KeepAlive(devInfos)
	if err != nil {
		_ = ctx.Uninit()
		ctx.Free()
		return nil, fmt.Errorf("init capture device: %w", err)
	}
	if err := device.Start(); err != nil {
		device.Uninit()
		_ = ctx.Uninit()
		ctx.Free()
		return nil, fmt.Errorf("start capture device: %w", err)
	}
	m.device = device
	return m, nil
}

// onFrames is miniaudio's capture callback (runs on the audio thread): it
// appends the incoming PCM and updates the live level. Keep it cheap.
func (m *Mic) onFrames(_, in []byte, _ uint32) {
	if len(in) == 0 {
		return
	}
	atomic.StoreInt32(&m.level, int32(calculateLevel(in)))
	m.mu.Lock()
	m.buf.Write(in)
	m.mu.Unlock()
}

// Level returns the most recent capture level (0-100).
func (m *Mic) Level() int { return int(atomic.LoadInt32(&m.level)) }

// Reset discards buffered audio (used between wake-word utterances so each
// transcription only sees the latest phrase).
func (m *Mic) Reset() {
	m.mu.Lock()
	m.buf.Reset()
	m.mu.Unlock()
}

// WAV returns the audio captured so far as a 16 kHz mono WAV, or nil if too
// short to be meaningful.
func (m *Mic) WAV() []byte {
	m.mu.Lock()
	pcm := append([]byte(nil), m.buf.Bytes()...)
	m.mu.Unlock()
	if len(pcm) < 100 {
		return nil
	}
	return wavFromPCM(pcm)
}

// TakeWAV atomically snapshots the audio captured since the last Reset as a
// 16 kHz mono WAV and clears the buffer, so frames arriving between snapshot
// and reset are never lost (unlike a WAV() + Reset() pair). Returns nil if the
// captured audio is too short to be meaningful.
func (m *Mic) TakeWAV() []byte {
	m.mu.Lock()
	pcm := append([]byte(nil), m.buf.Bytes()...)
	m.buf.Reset()
	m.mu.Unlock()
	if len(pcm) < 100 {
		return nil
	}
	return wavFromPCM(pcm)
}

// Close stops capture and releases the device and audio context. Safe to call
// more than once.
func (m *Mic) Close() {
	if m.device != nil {
		m.device.Uninit() // stops the device and frees it
		m.device = nil
	}
	if m.ctx != nil {
		_ = m.ctx.Uninit()
		m.ctx.Free()
		m.ctx = nil
	}
}

// wavFromPCM wraps raw s16le mono 16 kHz PCM in a canonical 44-byte WAV header.
func wavFromPCM(pcm []byte) []byte {
	const (
		bitsPerSample = 16
		blockAlign    = micChannels * bitsPerSample / 8
		byteRate      = micSampleRate * blockAlign
	)
	dataLen := uint32(len(pcm))
	var b bytes.Buffer
	b.Grow(44 + len(pcm))
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+dataLen))
	b.WriteString("WAVE")
	b.WriteString("fmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16)) // PCM fmt chunk size
	binary.Write(&b, binary.LittleEndian, uint16(1))  // audio format: PCM
	binary.Write(&b, binary.LittleEndian, uint16(micChannels))
	binary.Write(&b, binary.LittleEndian, uint32(micSampleRate))
	binary.Write(&b, binary.LittleEndian, uint32(byteRate))
	binary.Write(&b, binary.LittleEndian, uint16(blockAlign))
	binary.Write(&b, binary.LittleEndian, uint16(bitsPerSample))
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, dataLen)
	b.Write(pcm)
	return b.Bytes()
}

// calculateLevel computes an audio level 0-100 from 16-bit PCM samples using an
// RMS-to-dB mapping (gentler than linear, so quiet speech still moves the bar).
func calculateLevel(data []byte) int {
	numSamples := len(data) / 2
	if numSamples == 0 {
		return 0
	}
	var sumSquares float64
	for i := 0; i < numSamples; i++ {
		sample := int16(binary.LittleEndian.Uint16(data[i*2 : i*2+2]))
		sumSquares += float64(sample) * float64(sample)
	}
	rms := math.Sqrt(sumSquares / float64(numSamples))
	if rms < 1 {
		return 0
	}
	// dB scale: 20*log10(rms/32768) ≈ -90..0; map -60..0 dB onto 0..100.
	db := 20 * math.Log10(rms/32768)
	level := int((db + 60) * 100 / 60)
	if level < 0 {
		level = 0
	}
	if level > 100 {
		level = 100
	}
	return level
}

// ListDevices returns the available microphone input devices via miniaudio.
func ListDevices() []AudioDevice {
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		logger.Error("Mic: list devices: %s", err.Error())
		return nil
	}
	defer func() {
		_ = ctx.Uninit()
		ctx.Free()
	}()

	infos, err := ctx.Devices(malgo.Capture)
	if err != nil {
		logger.Error("Mic: enumerate capture devices: %s", err.Error())
		return nil
	}
	var devices []AudioDevice
	for i := range infos {
		name := infos[i].Name()
		if name == "" || isMonitorSource(name) {
			continue // skip output loopbacks ("Monitor of …") — they aren't mics
		}
		// Name doubles as the ID: it's stable and portable across restarts,
		// unlike miniaudio's opaque per-backend DeviceID bytes.
		devices = append(devices, AudioDevice{ID: name, Name: name})
	}
	return devices
}

// isMonitorSource reports whether a device is an output monitor/loopback rather
// than a real microphone (PulseAudio exposes these as capture devices).
func isMonitorSource(name string) bool {
	return strings.Contains(strings.ToLower(name), "monitor")
}
