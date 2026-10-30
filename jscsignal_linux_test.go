//go:build linux

package main

import (
	"os"
	"syscall"
	"testing"
)

func TestConfigureWebKitSignalsMovesJSCOffSIGUSR1(t *testing.T) {
	t.Setenv(jscSignalEnv, "")
	os.Unsetenv(jscSignalEnv)

	configureWebKitSignals()

	sig, ok := jscSignalFromEnv()
	if !ok {
		t.Fatalf("%s not set to a number: %q", jscSignalEnv, os.Getenv(jscSignalEnv))
	}
	if sig == int(syscall.SIGUSR1) || sig == int(syscall.SIGUSR2) {
		t.Fatalf("JSC signal = %d, want a dedicated real-time signal", sig)
	}
	// glibc reserves 32 and 33; real-time signals for programs start at 34.
	if sig < 34 || sig > 64 {
		t.Fatalf("JSC signal = %d, want one in the real-time range", sig)
	}
	// WebKit only stays quiet (and overrides nothing) on a default handler.
	if !signalIsDefault(sig) {
		t.Fatalf("signal %d still has a handler; WebKit would override it", sig)
	}
	// Go keeps its own handler on SIGUSR1.
	if signalIsDefault(int(syscall.SIGUSR1)) {
		t.Fatal("SIGUSR1 lost Go's handler")
	}
}

func TestConfigureWebKitSignalsKeepsUserChoice(t *testing.T) {
	t.Setenv(jscSignalEnv, "12")

	configureWebKitSignals()

	if got := os.Getenv(jscSignalEnv); got != "12" {
		t.Fatalf("%s = %q, want the user's 12", jscSignalEnv, got)
	}
}
