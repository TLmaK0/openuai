//go:build !((linux || freebsd || netbsd || openbsd || illumos) && !nodbus)

package tray

import "github.com/gen2brain/beeep"

// notifySilent shows a notification without sound. On Windows and macOS a
// regular (non-alert) beeep notification is already silent.
func notifySilent(title, message, icon string) error {
	return beeep.Notify(title, message, icon)
}
