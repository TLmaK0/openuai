// Command openuai is the OpenUAI desktop app. The app itself lives in
// internal/app; this package stays at the repository root because Wails builds
// the root package and //go:embed cannot reach files above its own directory.
package main

import (
	"embed"
	"os"

	"openuai/internal/app"
)

// Version is set at build time via -ldflags "-X main.Version=vX.Y.Z"
var Version = "dev"

//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/appicon.png
var appIcon []byte

//go:embed whisper-version.txt
var whisperVersion string

func main() {
	// `openuai mcp`: serve the running app as an MCP server over stdio.
	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		os.Exit(app.RunMCP(Version, os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}

	app.Run(app.Options{
		Version:        Version,
		Assets:         assets,
		AppIcon:        appIcon,
		WhisperVersion: whisperVersion,
	})
}
