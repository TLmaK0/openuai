//go:build linux

package main

import (
	"os"
	"strconv"
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

// An inherited JSC_SIGNAL_FOR_GC (a parent OpenUAI exports it to everything
// it starts) still gets that signal handed over without Go's handler.
func TestConfigureWebKitSignalsCleansInheritedSignal(t *testing.T) {
	sig := jscSignal(sigrtmax()) - 1
	t.Setenv(jscSignalEnv, strconv.Itoa(sig))
	if signalIsDefault(sig) {
		t.Fatalf("signal %d should start with Go's handler", sig)
	}

	configureWebKitSignals()
	defer setJSCSignal(0)

	if !signalIsDefault(sig) {
		t.Fatalf("signal %d still has Go's handler; WebKit would override it", sig)
	}
}

// C libraries' handlers get SA_ONSTACK as they are installed, except on
// JavaScriptCore's suspend signal and SIGUSR1.
func TestCHandlersGetSAOnStack(t *testing.T) {
	rtmax := sigrtmax()
	plain, jsc := rtmax-6, rtmax-7
	setJSCSignal(jsc)
	defer setJSCSignal(0)

	restore, err := installCHandler(plain)
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	if !signalOnStack(plain) {
		t.Fatalf("C handler on signal %d installed without SA_ONSTACK", plain)
	}

	restoreJSC, err := installCHandler(jsc)
	if err != nil {
		t.Fatal(err)
	}
	defer restoreJSC()
	if signalOnStack(jsc) {
		t.Fatalf("JavaScriptCore's signal %d must keep its handler on the thread's own stack", jsc)
	}
}
