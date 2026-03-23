package voice

// TranscribeResult holds the result of a speech-to-text transcription.
type TranscribeResult struct {
	Text         string  `json:"text"`
	DurationSecs float64 `json:"duration_secs"`
	Error        string  `json:"error,omitempty"`
}

// SpeakResult holds the result of a text-to-speech synthesis.
type SpeakResult struct {
	AudioBase64 string `json:"audio_base64"`
	Format      string `json:"format"`
	CharCount   int    `json:"char_count"`
	Error       string `json:"error,omitempty"`
}
