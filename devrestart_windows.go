//go:build windows

package main

import "errors"

// startDetached is not available on Windows: dev.sh and the relaunch loop are
// bash scripts. Start the app with scripts/dev-relaunch.sh instead.
func startDetached(script, target, logPath string) error {
	return errors.New("automatic relaunch is not supported on Windows; start the app with scripts/dev-relaunch.sh")
}
