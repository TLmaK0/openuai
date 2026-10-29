package subagents

import "testing"

func TestParseEffort(t *testing.T) {
	d, err := Parse([]byte("---\nname: rev\ndescription: x\neffort: High\n---\nbody"))
	if err != nil || d.Effort != "high" {
		t.Fatalf("got %+v %v", d, err)
	}
	if _, err := Parse([]byte("---\nname: rev\ndescription: x\neffort: huge\n---\nbody")); err == nil {
		t.Fatal("invalid effort must be rejected")
	}
}
