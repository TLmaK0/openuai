package voice

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"openuai/internal/logger"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// appendSilence adds silent samples at the end of a WAV file and updates the header.
// This prevents whisper.cpp from clipping the last spoken words.
func appendSilence(wav []byte, sampleRate, ms int) []byte {
	if len(wav) < 44 || string(wav[0:4]) != "RIFF" {
		return wav
	}
	// 16-bit mono silence = zero bytes
	numBytes := sampleRate * 2 * ms / 1000
	silence := make([]byte, numBytes)
	result := append(wav, silence...)
	// Update RIFF size
	binary.LittleEndian.PutUint32(result[4:8], uint32(len(result)-8))
	// Find and update data chunk size
	pos := 12
	for pos+8 <= len(result) {
		if string(result[pos:pos+4]) == "data" {
			binary.LittleEndian.PutUint32(result[pos+4:pos+8], uint32(len(result)-pos-8))
			break
		}
		pos += 8 + int(binary.LittleEndian.Uint32(result[pos+4:pos+8]))
	}
	return result
}

// modelPath returns the path to the whisper.cpp GGML model file.
func modelPath(model string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "whisper-cpp", "ggml-"+model+".bin")
}

// Transcribe runs whisper.cpp (whisper-cli) on base64-encoded WAV audio and returns the transcript.
func Transcribe(audioBase64, model, language string) TranscribeResult {
	if model == "" {
		model = "small"
	}
	if language == "" || language == "auto" {
		language = "es"
	}

	whisperPath, err := exec.LookPath("whisper-cli")
	if err != nil {
		return TranscribeResult{Error: "whisper-cli not found — install whisper.cpp: https://github.com/ggerganov/whisper.cpp"}
	}

	mPath := modelPath(model)
	if _, err := os.Stat(mPath); err != nil {
		return TranscribeResult{Error: fmt.Sprintf("model not found at %s — download with: whisper.cpp/models/download-ggml-model.sh %s", mPath, model)}
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
	}
	args = append(args, "-l", language)
	cmd := exec.Command(whisperPath, args...)
	output, err := cmd.Output() // stdout only, logs go to stderr
	if err != nil {
		logger.Error("whisper-cli error: %s", err)
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
