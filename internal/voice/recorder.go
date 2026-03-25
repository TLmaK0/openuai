package voice

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math"
	"openuai/internal/logger"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// AudioDevice represents an available audio input device.
type AudioDevice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Recorder captures audio from the system microphone using PulseAudio/ALSA CLI tools.
type Recorder struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	tmpFile string
	active  bool
	stopCh  chan struct{}
	Device  string // PulseAudio source name (empty = default)

	// OnLevel is called periodically with the current audio level (0-100).
	OnLevel func(level int)
}

// NewRecorder creates a new audio recorder.
func NewRecorder() *Recorder {
	return &Recorder{}
}

// Start begins recording audio to a temp file.
func (r *Recorder) Start() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.active {
		return fmt.Errorf("already recording")
	}

	tmpDir := os.TempDir()
	r.tmpFile = filepath.Join(tmpDir, "openuai_voice.wav")

	// Try parecord (PulseAudio) first, fall back to arecord (ALSA)
	if path, err := exec.LookPath("parecord"); err == nil {
		args := []string{"--file-format=wav", "--format=s16le", "--rate=16000", "--channels=1"}
		if r.Device != "" {
			args = append(args, "--device="+r.Device)
		}
		args = append(args, r.tmpFile)
		r.cmd = exec.Command(path, args...)
	} else if path, err := exec.LookPath("arecord"); err == nil {
		args := []string{"-f", "S16_LE", "-r", "16000", "-c", "1", "-t", "wav"}
		if r.Device != "" {
			args = append(args, "-D", r.Device)
		}
		args = append(args, r.tmpFile)
		r.cmd = exec.Command(path, args...)
	} else {
		return fmt.Errorf("no audio recorder found (install pulseaudio-utils or alsa-utils)")
	}

	// Remove old file
	os.Remove(r.tmpFile)

	if err := r.cmd.Start(); err != nil {
		return fmt.Errorf("start recorder: %w", err)
	}

	r.active = true
	r.stopCh = make(chan struct{})
	logger.Info("Voice recording started: %s (pid %d)", r.cmd.Path, r.cmd.Process.Pid)

	// Start volume monitor goroutine
	if r.OnLevel != nil {
		go r.monitorLevel()
	}

	return nil
}

// monitorLevel reads the WAV file periodically and calculates audio level.
func (r *Recorder) monitorLevel() {
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()

	var lastSize int64

	for {
		select {
		case <-r.stopCh:
			return
		case <-ticker.C:
			info, err := os.Stat(r.tmpFile)
			if err != nil || info.Size() < 100 {
				continue
			}

			currentSize := info.Size()
			if currentSize <= lastSize {
				continue
			}

			// Read the last chunk of audio data (last ~100ms = 3200 bytes at 16kHz 16bit mono)
			chunkSize := int64(3200)
			readFrom := currentSize - chunkSize
			if readFrom < 0 {
				readFrom = 0
				chunkSize = currentSize
			}

			f, err := os.Open(r.tmpFile)
			if err != nil {
				continue
			}
			f.Seek(readFrom, 0)
			buf := make([]byte, chunkSize)
			n, _ := f.Read(buf)
			f.Close()

			if n < 2 {
				continue
			}

			lastSize = currentSize

			// Calculate RMS of 16-bit signed samples
			level := calculateLevel(buf[:n])
			if r.OnLevel != nil {
				r.OnLevel(level)
			}
		}
	}
}

// calculateLevel computes audio level 0-100 from 16-bit PCM samples.
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
	// Normalize: max int16 is 32768, convert to 0-100 with log scale for better UX
	if rms < 1 {
		return 0
	}
	// dB scale: 20*log10(rms/32768), range roughly -90 to 0
	db := 20 * math.Log10(rms/32768)
	// Map -60..0 dB to 0..100
	level := int((db + 60) * 100 / 60)
	if level < 0 {
		level = 0
	}
	if level > 100 {
		level = 100
	}
	return level
}

// Stop ends recording and returns the audio as base64.
func (r *Recorder) Stop() (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.active || r.cmd == nil || r.cmd.Process == nil {
		return "", fmt.Errorf("not recording")
	}

	// Stop monitor
	if r.stopCh != nil {
		close(r.stopCh)
	}

	// Send SIGINT and give recorder time to flush remaining audio
	r.cmd.Process.Signal(os.Interrupt)
	done := make(chan struct{})
	go func() { r.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		r.cmd.Process.Kill()
		<-done
	}
	r.active = false

	logger.Info("Voice recording stopped")

	data, err := os.ReadFile(r.tmpFile)
	if err != nil {
		return "", fmt.Errorf("read recording: %w", err)
	}

	os.Remove(r.tmpFile)

	if len(data) < 100 {
		return "", fmt.Errorf("recording too short")
	}

	// Fix WAV header — parecord/arecord may not update sizes after SIGINT
	data = fixWAVHeader(data)

	logger.Info("Voice recording: %d bytes captured", len(data))
	return base64.StdEncoding.EncodeToString(data), nil
}

// fixWAVHeader corrects the RIFF and data chunk sizes in the WAV header
// to match the actual file size. Recorders killed with SIGINT often leave
// the header with size=0 or a stale value, causing decoders to ignore
// trailing audio data.
func fixWAVHeader(data []byte) []byte {
	if len(data) < 44 {
		return data
	}
	// Verify RIFF header
	if string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return data
	}

	fileSize := uint32(len(data))

	// Fix RIFF chunk size (bytes 4-7): fileSize - 8
	binary.LittleEndian.PutUint32(data[4:8], fileSize-8)

	// Find "data" sub-chunk and fix its size
	pos := 12
	for pos+8 <= len(data) {
		chunkID := string(data[pos : pos+4])
		if chunkID == "data" {
			dataSize := fileSize - uint32(pos) - 8
			binary.LittleEndian.PutUint32(data[pos+4:pos+8], dataSize)
			break
		}
		chunkSize := binary.LittleEndian.Uint32(data[pos+4 : pos+8])
		pos += 8 + int(chunkSize)
	}

	return data
}

// IsRecording returns whether recording is active.
func (r *Recorder) IsRecording() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active
}

// ListDevices returns available audio input devices via PulseAudio.
func ListDevices() []AudioDevice {
	pactlPath, err := exec.LookPath("pactl")
	if err != nil {
		return nil
	}

	out, err := exec.Command(pactlPath, "list", "sources", "short").Output()
	if err != nil {
		return nil
	}

	var devices []AudioDevice
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := fields[1]
		// Skip monitor sources (they capture output, not input)
		if strings.Contains(name, ".monitor") {
			continue
		}
		// Build a friendly label from the source name
		label := name
		label = strings.ReplaceAll(label, "alsa_input.", "")
		label = strings.ReplaceAll(label, "-", " ")
		label = strings.ReplaceAll(label, "_", " ")
		label = strings.ReplaceAll(label, ".", " ")

		devices = append(devices, AudioDevice{ID: name, Name: label})
	}
	return devices
}
