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
	if _, ok := os.LookupEnv(jscSignalEnv); ok {
		return
	}
	sig := jscSignal(int(C.openuai_sigrtmax()))
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

// jscSignal returns the signal number to hand to JavaScriptCore.
func jscSignal(sigrtmax int) int {
	return sigrtmax - jscSignalOffset
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
