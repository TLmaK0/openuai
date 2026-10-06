//go:build windows

package main

import "errors"

// startDetached is not available on Windows: dev.sh and its relaunch loop are
// a bash script. Start the app with ./dev.sh from git-bash instead.
func startDetached(script, target, logPath string) error {
	return errors.New("automatic relaunch is not supported on Windows; start the app with ./dev.sh")
}
