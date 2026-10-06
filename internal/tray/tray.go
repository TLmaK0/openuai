package tray

import (
	"fyne.io/systray"
)

// Config configures the system tray behavior.
type Config struct {
	OnShow func()
	OnQuit func()
	Icon   []byte
}

var (
	cfg         Config
	mNotify     *systray.MenuItem
	started     bool
	quitCh      chan struct{}
)

// Start initializes and runs the system tray.
func Start(c Config) {
	cfg = c
	quitCh = make(chan struct{})
	run(onReady, onExit)
	started = true
}

// Stop shuts down the system tray.
func Stop() {
	if started {
		quit()
		started = false
	}
}

// SetTooltip updates the tray icon tooltip text.
func SetTooltip(text string) {
	if started {
		systray.SetTooltip(text)
	}
}

func onReady() {
	systray.SetIcon(cfg.Icon)
	systray.SetTitle("OpenUAI")
	systray.SetTooltip("OpenUAI")

	mShow := systray.AddMenuItem("Show", "Show OpenUAI window")
	systray.AddSeparator()
	mNotify = systray.AddMenuItemCheckbox("Notifications", "Toggle notifications", IsEnabled())
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Quit OpenUAI")

	go func() {
		for {
			select {
			case <-mShow.ClickedCh:
				if cfg.OnShow != nil {
					cfg.OnShow()
				}
			case <-mNotify.ClickedCh:
				if mNotify.Checked() {
					mNotify.Uncheck()
					SetEnabled(false)
				} else {
					mNotify.Check()
					SetEnabled(true)
				}
			case <-mQuit.ClickedCh:
				if cfg.OnQuit != nil {
					cfg.OnQuit()
				}
			case <-quitCh:
				return
			}
		}
	}()
}

func onExit() {
	if quitCh != nil {
		close(quitCh)
	}
}

// SyncNotifyCheckbox updates the tray checkbox to match the current enabled state.
func SyncNotifyCheckbox() {
	if mNotify == nil {
		return
	}
	if IsEnabled() {
		mNotify.Check()
	} else {
		mNotify.Uncheck()
	}
}
