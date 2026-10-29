package subagents

import "testing"

func TestParseBackground(t *testing.T) {
	d, err := Parse([]byte("---\nname: a\ndescription: x\nbackground: true\n---\nbody"))
	if err != nil || !d.Background {
		t.Fatalf("got %+v %v", d, err)
	}
	d, err = Parse([]byte("---\nname: a\ndescription: x\n---\nbody"))
	if err != nil || d.Background {
		t.Fatalf("default must be foreground: %+v %v", d, err)
	}
}
