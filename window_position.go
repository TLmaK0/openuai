package main

import (
	"context"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"openuai/internal/logger"
)

// The window reopens where it was: its position (absolute screen
// coordinates, so it works across monitors) is saved when the app quits or
// restarts and restored on start.

// domReady restores the saved window position.
func (a *App) domReady(ctx context.Context) {
	if a.cfg == nil {
		return
	}
	x, y, ok := a.cfg.WindowPosition()
	if !ok {
		return
	}
	moveWindowTo(ctx, x, y)
	logger.Info("Window restored at %d,%d", x, y)
}

// moveWindowTo places the window at absolute screen coordinates. Wails'
// WindowSetPosition is relative to the monitor the window is on, so the
// origin of that monitor is found first.
func moveWindowTo(ctx context.Context, x, y int) {
	wailsRuntime.WindowSetPosition(ctx, 0, 0)
	time.Sleep(50 * time.Millisecond)
	ox, oy := wailsRuntime.WindowGetPosition(ctx)
	wailsRuntime.WindowSetPosition(ctx, x-ox, y-oy)
}

// saveWindowPosition remembers where the window is now.
func (a *App) saveWindowPosition() {
	if a.ctx == nil || a.cfg == nil {
		return
	}
	x, y := wailsRuntime.WindowGetPosition(a.ctx)
	a.cfg.SetWindowPosition(x, y)
	_ = a.cfg.Save()
}
