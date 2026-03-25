package main

import (
	"embed"
	"strings"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/appicon.png
var appIcon []byte

//go:embed whisper-version.txt
var whisperVersion string

func main() {
	// Create an instance of the app structure
	app := NewApp()
	app.whisperVersion = strings.TrimSpace(whisperVersion)

	// Create application with options
	err := wails.Run(&options.App{
		Title:  "OpenUAI",
		Width:  1024,
		Height: 768,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour:   &options.RGBA{R: 27, G: 38, B: 54, A: 1},
		HideWindowOnClose:  true,
		OnStartup:          app.startup,
		OnShutdown:         app.shutdown,
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
