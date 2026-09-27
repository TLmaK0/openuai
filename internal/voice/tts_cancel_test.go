package voice

import (
	"context"
	"testing"
)

// A cancelled context (sound turned off) must produce no audio at all.
func TestSpeakCancelledProducesNoAudio(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := Speak(ctx, "hola", "es_ES", t.TempDir())
	if res.AudioBase64 != "" {
		t.Fatalf("expected no audio when cancelled, got %d bytes", len(res.AudioBase64))
	}
	if res.Error == "" {
		t.Fatalf("expected a cancellation error")
	}
}
