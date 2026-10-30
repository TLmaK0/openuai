//go:build bindings

package main

// bindingsBuild is true in the binary `wails build` runs to generate the
// frontend bindings: that run must not touch the crash traces.
const bindingsBuild = true
