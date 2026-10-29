package main

import (
	"context"
	"testing"
)

func TestBackgroundRunFlag(t *testing.T) {
	if inBackgroundRun(context.Background()) {
		t.Fatal("plain context is not a background run")
	}
	if !inBackgroundRun(context.WithValue(context.Background(), backgroundRunKey{}, true)) {
		t.Fatal("flag not read")
	}
}
