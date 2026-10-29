package subagents

import "testing"

func TestParseColor(t *testing.T) {
	d, err := Parse([]byte("---\nname: rev\ndescription: x\ncolor: Purple\n---\nbody"))
	if err != nil || d.Color != "purple" {
		t.Fatalf("got %+v %v", d, err)
	}
	if _, err := Parse([]byte("---\nname: rev\ndescription: x\ncolor: magenta\n---\nbody")); err == nil {
		t.Fatal("invalid colour must be rejected")
	}
	d, err = Parse([]byte("---\nname: rev\ndescription: x\n---\nbody"))
	if err != nil || d.Color != "" {
		t.Fatalf("no colour: got %+v %v", d, err)
	}
	s := Set{Defs: map[string]*Definition{"rev": {Name: "rev", Color: "cyan"}}}
	if s.Color("rev") != "cyan" || s.Color("none") != "" {
		t.Fatal("Set.Color")
	}
}
