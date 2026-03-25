package voice

import (
	"encoding/base64"
	"fmt"
	"openuai/internal/logger"
	"openuai/internal/whisper"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Transcribe runs whisper.cpp (whisper-cli) on base64-encoded WAV audio and returns the transcript.
func Transcribe(audioBase64, model, language, configDir string) TranscribeResult {
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
	if err := os.WriteFile(wavFile, audioBytes, 0600); err != nil {
		return TranscribeResult{Error: fmt.Sprintf("write temp file: %v", err)}
	}
	defer os.Remove(wavFile)

	logger.Info("Voice STT: running whisper-cli on %d bytes (model=%s)", len(audioBytes), model)

	// whisper-cli -m model.bin -f audio.wav --no-prints
	// Note: do NOT use --no-timestamps with --no-prints, it truncates output (whisper.cpp bug)
	args := []string{
		"-m", mPath,
		"-f", wavFile,
		"--no-prints",
		"-l", language,
	}
	cmd := exec.Command(whisperPath, args...)
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

	return TranscribeResult{
		Text: transcript,
	}
}
