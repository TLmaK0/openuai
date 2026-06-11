package voice

import "testing"

func TestCleanTranscript(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain", " hola mundo ", "hola mundo"},
		{"timestamps", "[00:00:00.000 --> 00:00:02.000] hola\n[00:00:02.000 --> 00:00:04.000] mundo", "hola mundo"},
		{"sound only brackets", "[Música]", ""},
		{"sound only parens", "(Aplausos)", ""},
		{"several sounds", "[Música] [tos]\n(ruido de fondo)", ""},
		{"sound plus speech", "[Música] enciende la luz", "enciende la luz"},
		{"sound mid speech", "enciende [tos] la luz", "enciende la luz"},
		{"multiline speech", "primera frase.\nsegunda frase.", "primera frase. segunda frase."},
	}
	for _, c := range cases {
		if got := cleanTranscript(c.in); got != c.want {
			t.Errorf("%s: cleanTranscript(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestCollapseRepeatedPhrase(t *testing.T) {
	cases := []struct{ name, in, want string }{
		// Verbatim duplications seen in real logs (whisper artifact on chunks):
		{"doubled question", "Hola Pepito, ¿cómo estás? Hola Pepito, ¿cómo estás?", "Hola Pepito, ¿cómo estás?"},
		{"doubled sentence", "Es que estoy mirando cómo se ve ahora el orbe donde tú hablas. Es que estoy mirando cómo se ve ahora el orbe donde tú hablas.", "Es que estoy mirando cómo se ve ahora el orbe donde tú hablas."},
		{"tripled word", "¡Suscríbete! ¡Suscríbete! ¡Suscríbete!", "¡Suscríbete!"},
		{"not repeated", "enciende la luz del salón", "enciende la luz del salón"},
		{"partial repeat keeps all", "hola hola qué tal", "hola hola qué tal"},
		{"single word", "hola", "hola"},
	}
	for _, c := range cases {
		if got := collapseRepeatedPhrase(c.in); got != c.want {
			t.Errorf("%s: collapseRepeatedPhrase(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestIsNonSpeech(t *testing.T) {
	for _, s := range []string{
		"", "  ", "Música", "*música*", "[BLANK_AUDIO]", "(risas)",
		"¡Suscríbete!", "Gracias por ver el vídeo.",
		// Short phrase stuck on repeat (≤3 distinct words, decoder collapse):
		"¡Suscríbete! ¡Suscríbete! ¡Puede ser! ¡Suscríbete! ¡Suscríbete!",
	} {
		if !isNonSpeech(s) {
			t.Errorf("isNonSpeech(%q) = false, want true", s)
		}
	}
	for _, s := range []string{
		"hola", "pon música en el salón",
		"enciende la luz del salón y apaga la de la cocina",
		// A real sentence whisper duplicated must NOT be treated as noise —
		// collapseRepeatedPhrase reduces it to one copy instead.
		"Hola Pepito, ¿cómo estás? Hola Pepito, ¿cómo estás?",
	} {
		if isNonSpeech(s) {
			t.Errorf("isNonSpeech(%q) = true, want false", s)
		}
	}
}
