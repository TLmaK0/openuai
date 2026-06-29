package voice

import (
	"testing"
	"time"
)

func TestSilenceEndFor(t *testing.T) {
	cases := []struct {
		name   string
		speech time.Duration
		want   time.Duration
	}{
		{"no speech yet", 0, wakeSilenceName},
		{"just below name threshold", wakeNameSpeech - time.Millisecond, wakeSilenceName},
		{"at name threshold is a normal command", wakeNameSpeech, wakeSilenceEnd},
		{"typical short command", 1500 * time.Millisecond, wakeSilenceEnd},
		{"just below dictation threshold", wakeDictSpeech - time.Millisecond, wakeSilenceEnd},
		{"at dictation threshold", wakeDictSpeech, wakeSilenceLong},
		{"long dictation", 30 * time.Second, wakeSilenceLong},
	}
	for _, c := range cases {
		if got := silenceEndFor(c.speech); got != c.want {
			t.Errorf("%s: silenceEndFor(%v) = %v, want %v", c.name, c.speech, got, c.want)
		}
	}
}

// TestSilenceWindowsAreSnappy guards the tuning invariants, independent of the
// exact millisecond values. The name-pause window may sit slightly above the
// normal one (the user often pauses after just the name before the command), but
// no non-dictation window may be laggy — the original regression had it at 1500ms,
// which made every quick command feel like it "always waits a minimum". Only true
// dictation gets the most patient window.
func TestSilenceWindowsAreSnappy(t *testing.T) {
	if wakeSilenceName >= time.Second {
		t.Errorf("name-pause window (%v) is laggy; keep short utterances sub-second", wakeSilenceName)
	}
	if wakeSilenceEnd >= time.Second {
		t.Errorf("normal-command window (%v) is laggy; keep it sub-second", wakeSilenceEnd)
	}
	if wakeSilenceLong < wakeSilenceName || wakeSilenceLong < wakeSilenceEnd {
		t.Errorf("dictation window (%v) should be the most patient of all", wakeSilenceLong)
	}
}

func TestStripWakeWord(t *testing.T) {
	const wake = "Pepito"
	cases := []struct {
		name       string
		transcript string
		wantRest   string
		wantHit    bool
	}{
		{"exact", "Pepito enciende la luz", "enciende la luz", true},
		{"case and accent fold", "PÉPITO apaga la tele", "apaga la tele", true},
		{"whisper mishears", "Papito pon música", "pon música", true},
		{"markdown wrapped", "*Pepito* qué hora es", "qué hora es", true},
		{"filler before name", "Oye Pepito abre el navegador", "abre el navegador", true},
		{"repeated wake word", "Pepito Pepito sube el volumen", "sube el volumen", true},
		{"punctuation after name", "Pepito, baja el volumen", "baja el volumen", true},
		{"name only", "Pepito", "", true},
		{"no wake word", "enciende la luz del salón", "", false},
		{"wake word too deep", "venga va a ver Pepito qué tal", "", false},
		{"empty", "", "", false},
	}
	for _, c := range cases {
		rest, hit := stripWakeWord(c.transcript, wake)
		if hit != c.wantHit {
			t.Errorf("%s: stripWakeWord(%q) hit = %v, want %v", c.name, c.transcript, hit, c.wantHit)
			continue
		}
		if hit && rest != c.wantRest {
			t.Errorf("%s: stripWakeWord(%q) rest = %q, want %q", c.name, c.transcript, rest, c.wantRest)
		}
	}
}

func TestLevenshtein(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "abc", 0},
		{"pepito", "papito", 1},
		{"pepito", "pepe", 3},
		{"kitten", "sitting", 3},
		{"café", "cafe", 1}, // rune-based, not byte-based
	}
	for _, c := range cases {
		if got := levenshtein(c.a, c.b); got != c.want {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
