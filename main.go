package main

import (
	"embed"
	"os"
	"strings"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
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
		os.Exit(runMCPCommand(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}

	// Create an instance of the app structure
	app := NewApp()
	app.whisperVersion = strings.TrimSpace(whisperVersion)
	app.version = Version

	// Create application with options
	err := wails.Run(&options.App{
		Title:  "OpenUAI",
		Width:  1024,
		Height: 768,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour:  &options.RGBA{R: 27, G: 38, B: 54, A: 1},
		HideWindowOnClose: true,
		OnStartup:         app.startup,
		OnDomReady:        app.domReady,
		OnShutdown:        app.shutdown,
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
