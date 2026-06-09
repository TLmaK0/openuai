package voice

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math"
	"openuai/internal/logger"
	"openuai/internal/sysproc"
	"openuai/internal/whisper"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const noSpeechMsg = "No se detectó voz — revisa el micrófono en Ajustes"

// isSilentWAV returns true when the PCM payload is essentially silence, so we
// can avoid feeding Whisper empty audio (which makes it hallucinate "[Música]").
func isSilentWAV(wav []byte) bool {
	if len(wav) <= 44 {
		return true
	}
	pcm := wav[44:]
	n := len(pcm) / 2
	if n == 0 {
		return true
	}
	var sum float64
	for i := 0; i < n; i++ {
		s := int16(binary.LittleEndian.Uint16(pcm[i*2:]))
		sum += float64(s) * float64(s)
	}
	return math.Sqrt(sum/float64(n)) < 30
}

// wavDurationSec estimates a PCM WAV's duration in seconds from its header.
// Returns 0 if it can't be parsed (caller then uses the full encoder context).
func wavDurationSec(wav []byte) float64 {
	if len(wav) < 44 {
		return 0
	}
	channels := int(binary.LittleEndian.Uint16(wav[22:24]))
	rate := int(binary.LittleEndian.Uint32(wav[24:28]))
	bits := int(binary.LittleEndian.Uint16(wav[34:36]))
	frameBytes := channels * bits / 8
	if rate <= 0 || frameBytes <= 0 {
		return 0
	}
	return float64(len(wav)-44) / float64(frameBytes) / float64(rate)
}

// audioCtxFor caps whisper's encoder audio context to roughly the clip length.
// By default whisper.cpp always runs the encoder over a full 30s window (~1500
// positions, ~50/sec) regardless of how short the audio is — and that fixed
// pass, not model loading, dominates latency (~5s for a 3s clip). Capping it to
// the clip length cuts a short utterance to ~1-2s. 0 means "use the default
// (all)"; we also return 0 once the clip is long enough to need the full window.
func audioCtxFor(durSec float64) int {
	if durSec <= 0 {
		return 0
	}
	ctx := int(durSec*50*1.25) + 32 // +25% margin so the tail isn't clipped
	if ctx < 256 {
		ctx = 256 // floor: even tiny clips need enough context to stay accurate
	}
	if ctx >= 1500 {
		return 0 // at/over the full window — just use the default
	}
	return ctx
}

// isNonSpeech matches Whisper's common non-speech hallucinations.
func isNonSpeech(t string) bool {
	s := strings.ToLower(strings.TrimSpace(t))
	s = strings.Trim(s, "[](){}¡!¿?.… \t\n*")
	switch s {
	case "", "música", "musica", "music", "blank_audio", "silence", "silencio",
		"aplausos", "applause", "risas", "laughter", "ruido", "noise":
		return true
	}
	return false
}

// Transcribe runs whisper.cpp (whisper-cli) on base64-encoded WAV audio and returns the transcript.
// prompt is an optional initial prompt that biases recognition (e.g. the wake
// word, so an isolated name like "Pepito" isn't mis-heard); pass "" for none.
func Transcribe(audioBase64, model, language, prompt, configDir string) TranscribeResult {
	if model == "" {
		model = "small"
	}
	if language == "" || language == "auto" {
		language = "es"
	}

	whisperPath := whisper.BinPath(configDir)
	if whisperPath == "" {
		return TranscribeResult{Error: "whisper-cli not found — it should be auto-downloaded on startup"}
	}

	mPath := whisper.ModelPath(configDir, model)
	if _, err := os.Stat(mPath); err != nil {
		return TranscribeResult{Error: fmt.Sprintf("model not found at %s — it should be auto-downloaded on startup", mPath)}
	}

	// Write audio to temp file
	tmpDir := os.TempDir()
	wavFile := filepath.Join(tmpDir, "openuai_stt.wav")
	audioBytes, err := base64.StdEncoding.DecodeString(audioBase64)
	if err != nil {
		return TranscribeResult{Error: fmt.Sprintf("decode base64: %v", err)}
	}
	if isSilentWAV(audioBytes) {
		logger.Info("Voice STT: captured audio is silent — skipping transcription")
		return TranscribeResult{Error: noSpeechMsg}
	}
	if err := os.WriteFile(wavFile, audioBytes, 0600); err != nil {
		return TranscribeResult{Error: fmt.Sprintf("write temp file: %v", err)}
	}
	defer os.Remove(wavFile)

	// whisper-cli -m model.bin -f audio.wav --no-prints
	// Note: do NOT use --no-timestamps with --no-prints, it truncates output (whisper.cpp bug)
	args := []string{
		"-m", mPath,
		"-f", wavFile,
		"--no-prints",
		"-l", language,
	}
	// Cap the encoder's audio context to the clip length so a short utterance
	// isn't processed over the full 30s window (the main source of STT latency).
	audioCtx := audioCtxFor(wavDurationSec(audioBytes))
	if audioCtx > 0 {
		args = append(args, "-ac", strconv.Itoa(audioCtx))
	}
	logger.Info("Voice STT: running whisper-cli on %d bytes (model=%s, audio_ctx=%d)", len(audioBytes), model, audioCtx)
	if prompt != "" {
		args = append(args, "--prompt", prompt)
	}
	cmd := exec.Command(whisperPath, args...)
	sysproc.HideConsole(cmd) // don't flash a console window on Windows
	output, err := cmd.Output() // stdout only, logs go to stderr
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			logger.Error("whisper-cli error: %s\nstderr: %s", err, string(exitErr.Stderr))
		} else {
			logger.Error("whisper-cli error: %s", err)
		}
		return TranscribeResult{Error: fmt.Sprintf("whisper-cli failed: %v", err)}
	}

	// Strip timestamp prefixes like [00:00:00.000 --> 00:00:02.000]
	raw := string(output)
	re := regexp.MustCompile(`\[\d{2}:\d{2}:\d{2}\.\d{3} --> \d{2}:\d{2}:\d{2}\.\d{3}\]\s*`)
	raw = re.ReplaceAllString(raw, "")
	transcript := strings.TrimSpace(raw)

	logger.Info("Voice STT: transcribed → %q", transcript)

	if isNonSpeech(transcript) {
		return TranscribeResult{Error: noSpeechMsg}
	}

	return TranscribeResult{
		Text: transcript,
	}
}
