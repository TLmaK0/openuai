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

func TestIsNonSpeech(t *testing.T) {
	for _, s := range []string{
		"", "  ", "Música", "*música*", "[BLANK_AUDIO]", "(risas)",
		"¡Suscríbete!", "Gracias por ver el vídeo.",
		// Decoder collapse over noise — phrases seen in real logs:
		"¡Suscríbete! ¡Suscríbete! ¡Puede ser! ¡Suscríbete! ¡Suscríbete! ¡Suscríbete! ¡Suscríbete! ¡Pero, no!",
	} {
		if !isNonSpeech(s) {
			t.Errorf("isNonSpeech(%q) = false, want true", s)
		}
	}
	for _, s := range []string{
		"hola", "pon música en el salón",
		"enciende la luz del salón y apaga la de la cocina",
	} {
		if isNonSpeech(s) {
			t.Errorf("isNonSpeech(%q) = true, want false", s)
		}
	}
}
