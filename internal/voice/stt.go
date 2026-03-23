package voice

import (
	"encoding/base64"
	"fmt"
	"openuai/internal/logger"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Transcribe runs local Whisper CLI on base64-encoded WAV audio and returns the transcript.
func Transcribe(audioBase64, model, language string) TranscribeResult {
	if model == "" {
		model = "small"
	}
	if language == "" {
		language = "auto"
	}

	whisperPath, err := exec.LookPath("whisper")
	if err != nil {
		return TranscribeResult{Error: "whisper not found — install with: pip install openai-whisper"}
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

	outDir := tmpDir
	logger.Info("Voice STT: running whisper on %d bytes (model=%s)", len(audioBytes), model)

	// Run whisper CLI: whisper audio.wav --model small --output_format txt --output_dir /tmp
	args := []string{
		wavFile,
		"--model", model,
		"--output_format", "txt",
		"--output_dir", outDir,
		"--fp16", "False",
	}
	// If language is "auto", omit --language so Whisper auto-detects
	if language != "auto" {
		args = append(args, "--language", language)
	}
	cmd := exec.Command(whisperPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Error("Whisper error: %s\nOutput: %s", err, string(output))
		return TranscribeResult{Error: fmt.Sprintf("whisper failed: %v — %s", err, string(output))}
	}

	// Read the generated .txt file
	txtFile := filepath.Join(outDir, "openuai_stt.txt")
	text, err := os.ReadFile(txtFile)
	if err != nil {
		return TranscribeResult{Error: fmt.Sprintf("read transcript: %v", err)}
	}
	os.Remove(txtFile)

	transcript := strings.TrimSpace(string(text))
	logger.Info("Voice STT: transcribed → %q", transcript)

	return TranscribeResult{
		Text: transcript,
	}
}
