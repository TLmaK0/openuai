package voice

import (
	"encoding/base64"
	"fmt"
	"openuai/internal/logger"
	"sync"
	"time"
)

// Recorder captures audio from the system microphone via miniaudio (see Mic),
// so it works on Windows, macOS and Linux with no external tools. It is the
// backend for push-to-talk recording.
type Recorder struct {
	mu     sync.Mutex
	mic    *Mic
	active bool
	stopCh chan struct{}
	Device string // device name (see ListDevices); empty = system default

	// OnLevel is called periodically with the current audio level (0-100).
	OnLevel func(level int)
}

// NewRecorder creates a new audio recorder.
func NewRecorder() *Recorder {
	return &Recorder{}
}

// Start begins capturing audio.
func (r *Recorder) Start() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.active {
		return fmt.Errorf("already recording")
	}

	mic, err := OpenMic(r.Device)
	if err != nil {
		return fmt.Errorf("start recorder: %w", err)
	}

	r.mic = mic
	r.active = true
	r.stopCh = make(chan struct{})
	logger.Info("Voice recording started (device=%q)", r.Device)

	if r.OnLevel != nil {
		go r.monitorLevel()
	}
	return nil
}

// monitorLevel forwards the live capture level to OnLevel until Stop.
func (r *Recorder) monitorLevel() {
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-r.stopCh:
			return
		case <-ticker.C:
			r.mu.Lock()
			mic := r.mic
			r.mu.Unlock()
			if mic == nil {
				return
			}
			if r.OnLevel != nil {
				r.OnLevel(mic.Level())
			}
		}
	}
}

// Stop ends recording and returns the captured audio as a base64 WAV.
func (r *Recorder) Stop() (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.active || r.mic == nil {
		return "", fmt.Errorf("not recording")
	}

	if r.stopCh != nil {
		close(r.stopCh)
	}

	wav := r.mic.WAV()
	r.mic.Close()
	r.mic = nil
	r.active = false

	logger.Info("Voice recording stopped")

	if len(wav) < 100 {
		return "", fmt.Errorf("recording too short")
	}
	logger.Info("Voice recording: %d bytes captured", len(wav))
	return base64.StdEncoding.EncodeToString(wav), nil
}

// IsRecording returns whether recording is active.
func (r *Recorder) IsRecording() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active
}
