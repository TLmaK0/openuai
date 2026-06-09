package voice

import (
	"encoding/base64"
	"fmt"
	"openuai/internal/logger"
	"openuai/internal/piper"
	"openuai/internal/sysproc"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Speak converts text to speech and returns WAV audio as base64. It prefers
// Piper (local neural TTS, natural voices) and falls back to espeak-ng when
// Piper is unavailable for the platform or synthesis fails.
func Speak(text, voice, configDir string) SpeakResult {
	if voice == "" {
		voice = "es_ES"
	}

	if piper.Supported() {
		if wav, err := piper.Speak(configDir, voice, text); err == nil {
			logger.Info("Voice TTS: piper %d chars, voice=%s -> %d bytes", len(text), voice, len(wav))
			return SpeakResult{
				AudioBase64: base64.StdEncoding.EncodeToString(wav),
				Format:      "wav",
				CharCount:   len(text),
			}
		} else {
			logger.Error("Voice TTS: piper failed (%v) — falling back to espeak-ng", err)
		}
	}

	return speakEspeak(text, voice)
}

// speakEspeak is the offline fallback using espeak-ng (robotic but always works).
func speakEspeak(text, voice string) SpeakResult {
	// espeak uses 2-letter codes; map "es_ES" -> "es"
	if i := strings.IndexAny(voice, "_-"); i > 0 {
		voice = voice[:i]
	}
	voice = strings.ToLower(voice)
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
	sysproc.HideConsole(cmd)
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
