package llm

import "testing"

func TestImageMediaType(t *testing.T) {
	cases := map[string]string{"iVBORw0KGgo": "image/png", "/9j/4AAQ": "image/jpeg", "R0lGODlh": "image/gif", "UklGRiQ": "image/webp"}
	for in, want := range cases {
		if got := ImageMediaType(in); got != want {
			t.Errorf("%s: got %s want %s", in, got, want)
		}
	}
}
