//go:build linux

package main

/*
#include <signal.h>
#include <string.h>

// openuai_signal_to_default gives sig back its default disposition, so that
// WebKit finds no handler on it and installs its own without complaint.
static int openuai_signal_to_default(int sig) {
	struct sigaction sa;
	memset(&sa, 0, sizeof sa);
	sa.sa_handler = SIG_DFL;
	sigemptyset(&sa.sa_mask);
	return sigaction(sig, &sa, NULL);
}

// openuai_signal_is_default reports whether sig has the default disposition.
static int openuai_signal_is_default(int sig) {
	struct sigaction sa;
	if (sigaction(sig, NULL, &sa) != 0) {
		return -1;
	}
	return sa.sa_handler == SIG_DFL;
}

static int openuai_sigrtmax(void) { return SIGRTMAX; }
static int openuai_sigrtmin(void) { return SIGRTMIN; }

// Defined in sigonstack_linux.c.
extern volatile int openuai_jsc_signal;

static void openuai_set_jsc_signal(int sig) { openuai_jsc_signal = sig; }

// Tests: a C handler installed the way GLib and WebKit do, without
// SA_ONSTACK, and the flags it ends up with.
static void openuai_test_handler(int sig) { (void)sig; }

static int openuai_test_install(int sig, struct sigaction *old) {
	struct sigaction sa;
	memset(&sa, 0, sizeof sa);
	sa.sa_handler = openuai_test_handler;
	sigemptyset(&sa.sa_mask);
	return sigaction(sig, &sa, old);
}

static int openuai_test_restore(int sig, struct sigaction *old) {
	return sigaction(sig, old, NULL);
}

static int openuai_signal_onstack(int sig) {
	struct sigaction sa;
	if (sigaction(sig, NULL, &sa) != 0) {
		return -1;
	}
	return (sa.sa_flags & SA_ONSTACK) != 0;
}
*/
import "C"

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// jscSignalEnv is the variable WebKit's JavaScriptCore reads, when its
// threading starts up, to choose the signal it uses to suspend and resume
// threads (for conservative garbage collection). Without it, it takes SIGUSR1.
const jscSignalEnv = "JSC_SIGNAL_FOR_GC"

// jscSignalOffset places the signal below SIGRTMAX, in the real-time range
// that neither Go, GLib, GTK nor WebKit use for anything else.
const jscSignalOffset = 3

// configureWebKitSignals moves JavaScriptCore's thread suspend signal off
// SIGUSR1, which belongs to the Go runtime, onto a dedicated real-time
// signal. It must run before WebKit initialises (before wails.Run).
//
// JavaScriptCore replaces whatever handler is on its signal with one that
// assumes the signal was sent by its own suspend code to one of its own
// threads: it does not use the alternate signal stack and it dereferences the
// thread being suspended without checking it. On SIGUSR1 it overrode Go's
// handler ("Overriding existing handler for signal 10"), so any SIGUSR1 that
// was not one of its suspend requests (sent to the process, or reaching a
// thread it did not expect, such as a Go thread) ran that handler on a
// goroutine stack and crashed the Go runtime (sigpanic, "unknown caller pc",
// "traceback did not unwind completely"). A private real-time signal is only
// ever sent by JavaScriptCore itself, and SIGUSR1 keeps Go's handler, which
// ignores a stray one.
//
// A JSC_SIGNAL_FOR_GC already in the environment is respected as is.
func configureWebKitSignals() {
	if sig, ok := jscSignalFromEnv(); ok {
		// Usually inherited from a parent OpenUAI (its terminals, or the
		// relaunch loop it started): Go's handler is on that signal again in
		// this process, and WebKit would override it. Hand it over clean,
		// as below, when it is one a program may give away.
		if sig == int(C.SIGUSR1) || sig == int(C.SIGUSR2) ||
			(sig >= int(C.openuai_sigrtmin()) && sig <= int(C.openuai_sigrtmax())) {
			C.openuai_set_jsc_signal(C.int(sig))
			C.openuai_signal_to_default(C.int(sig))
		}
		return
	}
	if _, ok := os.LookupEnv(jscSignalEnv); ok {
		return
	}
	sig := jscSignal(int(C.openuai_sigrtmax()))
	// Keep sigonstack_linux.c from adding SA_ONSTACK to JavaScriptCore's
	// handler on it.
	C.openuai_set_jsc_signal(C.int(sig))
	// Go installs its handler on every signal, real-time ones included.
	// Give this one back to the default disposition: WebKit then installs
	// its handler on a clean signal, and nothing of Go's is overridden.
	if C.openuai_signal_to_default(C.int(sig)) != 0 {
		fmt.Fprintf(os.Stderr, "openuai: cannot reset signal %d for WebKit; leaving %s unset\n", sig, jscSignalEnv)
		return
	}
	if err := os.Setenv(jscSignalEnv, strconv.Itoa(sig)); err != nil {
		fmt.Fprintf(os.Stderr, "openuai: cannot set %s: %v\n", jscSignalEnv, err)
	}
}

// sigrtmax returns SIGRTMAX (tests).
func sigrtmax() int { return int(C.openuai_sigrtmax()) }

// jscSignal returns the signal number to hand to JavaScriptCore.
func jscSignal(sigrtmax int) int {
	return sigrtmax - jscSignalOffset
}

// installCHandler installs a C handler without SA_ONSTACK on sig, the way
// GLib and WebKit do, and returns a function that puts the previous one back
// (tests).
func installCHandler(sig int) (restore func(), err error) {
	var old C.struct_sigaction
	if C.openuai_test_install(C.int(sig), &old) != 0 {
		return nil, fmt.Errorf("sigaction(%d) failed", sig)
	}
	return func() { C.openuai_test_restore(C.int(sig), &old) }, nil
}

// signalOnStack reports whether the handler on sig has SA_ONSTACK (tests).
func signalOnStack(sig int) bool {
	return C.openuai_signal_onstack(C.int(sig)) == 1
}

// setJSCSignal tells sigonstack_linux.c which signal is JavaScriptCore's
// (tests).
func setJSCSignal(sig int) {
	C.openuai_set_jsc_signal(C.int(sig))
}

// signalIsDefault reports whether sig has the default disposition (tests).
func signalIsDefault(sig int) bool {
	return C.openuai_signal_is_default(C.int(sig)) == 1
}

// jscSignalFromEnv parses the JSC_SIGNAL_FOR_GC value (tests).
func jscSignalFromEnv() (int, bool) {
	v, ok := os.LookupEnv(jscSignalEnv)
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	return n, err == nil
}
