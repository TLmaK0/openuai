//go:build !linux

package main

// configureWebKitSignals is only needed with WebKitGTK on Linux: macOS uses
// Mach exceptions instead of a suspend signal and Windows uses WebView2.
func configureWebKitSignals() {}
