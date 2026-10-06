//go:build !darwin

package tray

import (
	"runtime"

	"fyne.io/systray"
)

// run starts the tray with its own event loop in a goroutine. The goroutine
// stays on one OS thread: on Windows the tray window only gets its messages
// on the thread that created it.
func run(onReady, onExit func()) {
	go func() {
		runtime.LockOSThread()
		systray.Run(onReady, onExit)
	}()
}

func quit() {
	systray.Quit()
}
