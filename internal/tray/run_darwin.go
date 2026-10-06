//go:build darwin

package tray

/*
void trayDispatchStart(void);
*/
import "C"

import "fyne.io/systray"

var nativeStart func()

// run attaches the tray to the application's own event loop. On macOS the
// loop belongs to Wails: systray.Run would replace its NSApplication delegate
// and spin a second [NSApp run] off the main thread, which leaves the window
// hidden. The status item is created on the main thread instead.
func run(onReady, onExit func()) {
	nativeStart, _ = systray.RunWithExternalLoop(onReady, onExit)
	C.trayDispatchStart()
}

// quit is a no-op: systray.Quit would stop the Wails event loop, which is
// already shutting the application down.
func quit() {}

//export trayStartOnMain
func trayStartOnMain() {
	nativeStart()
}
