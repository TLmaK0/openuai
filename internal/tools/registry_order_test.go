package tools

import "testing"

// Tool lists must come out in the same order every time (the headless
// provider hashes them to reuse its CLI session).
func TestRegistryOrderStable(t *testing.T) {
	r := NewRegistry()
	r.Register(ListDir{})
	r.Register(ReadFile{})
	r.Register(WriteFile{})
	r.Register(Bash{})
	first := r.Definitions()
	for i := 0; i < 50; i++ {
		got := r.Definitions()
		for k := range got {
			if got[k].Name != first[k].Name {
				t.Fatalf("order changed: %v vs %v", got[k].Name, first[k].Name)
			}
		}
	}
	for k := 1; k < len(first); k++ {
		if first[k-1].Name > first[k].Name {
			t.Fatal("not sorted")
		}
	}
}
