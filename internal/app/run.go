package app

import (
	"fmt"
	"io/fs"
	"os"
	"strings"

	"openuai/internal/config"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

// Options carries what only the main package can provide: the build version
// and the files it embeds from the repository root.
type Options struct {
	Version        string // set at build time via -ldflags "-X main.Version=vX.Y.Z"
	Assets         fs.FS  // the built frontend (frontend/dist)
	AppIcon        []byte // build/appicon.png
	WhisperVersion string // whisper-version.txt
}

// appIcon is the window and tray icon, handed over by Run.
var appIcon []byte

// Run starts the desktop app and returns when it exits.
func Run(opts Options) {
	appIcon = opts.AppIcon

	// Keep fatal errors and unrecovered panics on disk too, and pick up the
	// trace of a crash of the previous run (#86). Not in the run `wails build`
	// makes to generate bindings: it would take the previous crash for itself.
	var crashes *crashRun
	if dir, err := config.Dir(); err == nil && !bindingsBuild {
		lastCrashTrace, crashes = startCrashLog(dir)
		if lastCrashTrace != "" {
			fmt.Fprintf(os.Stderr, "openuai: the previous run crashed; trace saved in %s\n", lastCrashTrace)
		}
	}
	defer crashes.finish()

	// Before WebKit starts: keep JavaScriptCore off Go's SIGUSR1 (#86).
	configureWebKitSignals()

	app := NewApp()
	app.whisperVersion = strings.TrimSpace(opts.WhisperVersion)
	app.version = opts.Version

	err := wails.Run(&options.App{
		Title:  "OpenUAI",
		Width:  1024,
		Height: 768,
		AssetServer: &assetserver.Options{
			Assets: opts.Assets,
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
