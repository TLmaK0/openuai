package voice

import (
	"encoding/base64"
	"fmt"
	"openuai/internal/logger"
	"os"
	"os/exec"
	"path/filepath"
)

// Speak converts text to speech using espeak-ng and returns WAV audio as base64.
func Speak(text, voice string) SpeakResult {
	if voice == "" {
		voice = "es"
	}

	espeakPath, err := exec.LookPath("espeak-ng")
	if err != nil {
		return SpeakResult{Error: "espeak-ng not found — install with: sudo apt install espeak-ng"}
	}

	tmpDir := os.TempDir()
	wavFile := filepath.Join(tmpDir, "openuai_tts.wav")
	defer os.Remove(wavFile)

	logger.Info("Voice TTS: espeak-ng %d chars, voice=%s", len(text), voice)

	// espeak-ng -v es -w output.wav "text"
	cmd := exec.Command(espeakPath, "-v", voice, "-w", wavFile, text)
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Error("espeak-ng error: %s\nOutput: %s", err, string(output))
		return SpeakResult{Error: fmt.Sprintf("espeak-ng failed: %v — %s", err, string(output))}
	}

	wavBytes, err := os.ReadFile(wavFile)
	if err != nil {
		return SpeakResult{Error: fmt.Sprintf("read wav: %v", err)}
	}

	audioB64 := base64.StdEncoding.EncodeToString(wavBytes)
	logger.Info("Voice TTS: generated %d bytes of wav", len(wavBytes))

	return SpeakResult{
		AudioBase64: audioB64,
		Format:      "wav",
		CharCount:   len(text),
	}
}
