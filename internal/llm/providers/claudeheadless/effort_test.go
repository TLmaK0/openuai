package claudeheadless

import (
	"strings"
	"testing"
)

func TestArgsEffort(t *testing.T) {
	if strings.Contains(strings.Join(invocation{}.args(), " "), "--effort") {
		t.Fatal("no --effort by default")
	}
	if !strings.Contains(strings.Join(invocation{effort: "high"}.args(), " "), "--effort high") {
		t.Fatal("--effort high missing")
	}
}
