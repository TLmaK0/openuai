//go:build !darwin

package tray

import "fyne.io/systray"

// run starts the tray with its own event loop in a goroutine.
func run(onReady, onExit func()) {
	go systray.Run(onReady, onExit)
}

func quit() {
	systray.Quit()
}
