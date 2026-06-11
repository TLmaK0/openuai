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

// isNonSpeech matches Whisper's common non-speech hallucinations: sound-effect
// labels plus the YouTube-ish phrases its decoder invents over music/noise.
func isNonSpeech(t string) bool {
	s := strings.ToLower(strings.TrimSpace(t))
	s = strings.Trim(s, "[](){}¡!¿?.,… \t\n*")
	switch s {
	case "", "música", "musica", "music", "blank_audio", "silence", "silencio",
		"aplausos", "applause", "risas", "laughter", "ruido", "noise",
		"suscríbete", "suscribete", "suscríbete al canal", "suscribete al canal",
		"gracias por ver", "gracias por ver el vídeo", "gracias por ver el video",
		"thanks for watching", "subscribe",
		"subtítulos realizados por la comunidad de amara.org",
		"subtitulos realizados por la comunidad de amara.org":
		return true
	}
	return isRepetitionLoop(t)
}

// isRepetitionLoop reports whether a transcript is a SHORT phrase stuck on
// repeat — whisper's decoder collapses like that over music or noise
// ("¡Suscríbete! ¡Suscríbete! ¡Suscríbete! …"). Kept deliberately narrow
// (≤3 distinct words): a real sentence whisper duplicated verbatim is handled
// by collapseRepeatedPhrase instead of being dropped.
func isRepetitionLoop(t string) bool {
	words := strings.Fields(strings.ToLower(t))
	if len(words) < 5 {
		return false
	}
	distinct := map[string]struct{}{}
	for _, w := range words {
		distinct[strings.Trim(w, "[](){}¡!¿?.,… *")] = struct{}{}
	}
	return len(distinct) <= 3
}

// collapseRepeatedPhrase detects a transcript that is one phrase repeated
// verbatim two or more times — a whisper decoding artifact on chunked audio
// ("Hola Pepito, ¿cómo estás? Hola Pepito, ¿cómo estás?") — and returns a
// single copy. Words are compared case/accent-insensitively; the first
// occurrence keeps its original punctuation.
func collapseRepeatedPhrase(t string) string {
	toks := tokenizeFolded(t)
	n := len(toks)
	for size := 1; size <= n/2; size++ {
		if n%size != 0 {
			continue
		}
		periodic := true
		for i := size; i < n && periodic; i++ {
			periodic = toks[i].folded == toks[i%size].folded
		}
		if periodic {
			// Cut just before the second copy starts, dropping any opening
			// punctuation that belongs to it ("… ¡" → "…").
			return strings.TrimRight(t[:toks[size].byteStart], " \t\n¡¿([«\"'*-—")
		}
	}
	return t
}

var (
	// sttTimestampRe matches whisper-cli's segment prefixes, e.g.
	// "[00:00:00.000 --> 00:00:02.000]".
	sttTimestampRe = regexp.MustCompile(`\[\d{2}:\d{2}:\d{2}\.\d{3} --> \d{2}:\d{2}:\d{2}\.\d{3}\]\s*`)
	// sttAnnotRe matches whisper's non-speech annotations: any bracketed or
	// parenthesized chunk, e.g. "[Música]", "(Aplausos)", "[tos]".
	sttAnnotRe = regexp.MustCompile(`\[[^\]]*\]|\([^)]*\)`)
)

// cleanTranscript strips whisper-cli output down to the spoken text: segment
// timestamps and non-speech annotations are removed and whitespace collapsed.
// A clip with only sounds ("[Música]") comes out empty, so the caller treats
// it as no speech.
func cleanTranscript(raw string) string {
	raw = sttTimestampRe.ReplaceAllString(raw, "")
	raw = sttAnnotRe.ReplaceAllString(raw, " ")
	return strings.Join(strings.Fields(raw), " ")
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

	audioBytes, err := base64.StdEncoding.DecodeString(audioBase64)
	if err != nil {
		return TranscribeResult{Error: fmt.Sprintf("decode base64: %v", err)}
	}
	if isSilentWAV(audioBytes) {
		logger.Info("Voice STT: captured audio is silent — skipping transcription")
		return TranscribeResult{Error: noSpeechMsg}
	}
	// Unique temp file per call: long dictations transcribe chunks in the
	// background while capture continues, so runs can overlap.
	tmp, err := os.CreateTemp("", "openuai_stt_*.wav")
	if err != nil {
		return TranscribeResult{Error: fmt.Sprintf("create temp file: %v", err)}
	}
	wavFile := tmp.Name()
	_, werr := tmp.Write(audioBytes)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	defer os.Remove(wavFile)
	if werr != nil {
		return TranscribeResult{Error: fmt.Sprintf("write temp file: %v", werr)}
	}

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

	transcript := collapseRepeatedPhrase(cleanTranscript(string(output)))

	logger.Info("Voice STT: transcribed → %q", transcript)

	if isNonSpeech(transcript) {
		return TranscribeResult{Error: noSpeechMsg}
	}

	return TranscribeResult{
		Text: transcript,
	}
}
