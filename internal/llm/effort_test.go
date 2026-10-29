package llm

import (
	"context"
	"testing"
)

func TestEffortContext(t *testing.T) {
	if EffortFrom(context.Background()) != "" {
		t.Fatal("no effort by default")
	}
	if EffortFrom(WithEffort(context.Background(), "high")) != "high" {
		t.Fatal("effort not carried")
	}
	if !ValidEffort("xhigh") || ValidEffort("huge") {
		t.Fatal("ValidEffort")
	}
}
