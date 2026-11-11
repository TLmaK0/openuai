//go:build linux

// SA_ONSTACK on every signal handler the C libraries install (#86).
//
// The Go runtime requires any handler installed by non-Go code to use
// SA_ONSTACK: without it, a signal that lands on a Go thread runs the C
// handler on the small goroutine stack and corrupts it. GLib, GTK and WebKit
// install theirs without the flag, and Wails only adds it afterwards, from a
// GLib timer that runs every 50 ms during the first 5 seconds and again at
// DomReady. That leaves windows of tens of milliseconds at startup with such
// handlers (SIGCHLD from GLib, SIGXCPU, SIGSEGV and SIGBUS from JavaScriptCore),
// and none of them is covered when a library installs a handler later on.
//
// This definition of sigaction in the executable takes precedence over the C
// library's for every shared library (and for the Go runtime, which installs
// its own handlers through it when cgo is in use): the flag is added as the
// handler is installed, so there is no window at all.
//
// Two signals are left as they are: SIGUSR1 and JavaScriptCore's thread
// suspend signal (JSC_SIGNAL_FOR_GC). That handler must run on the stack of
// the thread it suspends, which it scans for the garbage collector; on the
// alternate signal stack the scan breaks (the reason Wails leaves SIGUSR1
// alone too).

#include <signal.h>
#include <stddef.h>

// The C library's own sigaction, which this one wraps.
extern int __sigaction(int sig, const struct sigaction *act, struct sigaction *old);

// JavaScriptCore's suspend signal, set from Go before WebKit starts.
volatile int openuai_jsc_signal = 0;

// openuai_wants_onstack reports whether a handler being installed on sig
// with act needs SA_ONSTACK added.
int openuai_wants_onstack(int sig, const struct sigaction *act) {
	if (act == NULL || sig == SIGUSR1 || sig == openuai_jsc_signal) {
		return 0;
	}
	if (act->sa_handler == SIG_DFL || act->sa_handler == SIG_IGN) {
		return 0;
	}
	return (act->sa_flags & SA_ONSTACK) == 0;
}

int sigaction(int sig, const struct sigaction *act, struct sigaction *old) {
	if (openuai_wants_onstack(sig, act)) {
		struct sigaction fixed = *act;
		fixed.sa_flags |= SA_ONSTACK;
		return __sigaction(sig, &fixed, old);
	}
	return __sigaction(sig, act, old);
}
