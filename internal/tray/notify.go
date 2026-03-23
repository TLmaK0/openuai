package tray

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/gen2brain/beeep"
)

var (
	enabled  = true
	enableMu sync.RWMutex

	iconPath     string
	iconPathOnce sync.Once
	iconBytes    []byte
)

// SetIconBytes stores the embedded icon for use in notifications.
// Must be called before the first Notify call.
func SetIconBytes(data []byte) {
	iconBytes = data
}

// SetEnabled sets whether notifications are shown.
func SetEnabled(on bool) {
	enableMu.Lock()
	enabled = on
	enableMu.Unlock()
}

// IsEnabled returns whether notifications are enabled.
func IsEnabled() bool {
	enableMu.RLock()
	defer enableMu.RUnlock()
	return enabled
}

// Notify shows a native OS notification if notifications are enabled.
func Notify(title, message string) {
	if !IsEnabled() {
		return
	}
	icon := getIconPath()
	_ = beeep.Notify(title, message, icon)
}

// getIconPath writes the embedded icon to a temp file on first call and returns the path.
func getIconPath() string {
	iconPathOnce.Do(func() {
		if len(iconBytes) == 0 {
			return
		}
		tmp := filepath.Join(os.TempDir(), "openuai-icon.png")
		if err := os.WriteFile(tmp, iconBytes, 0o644); err == nil {
			iconPath = tmp
		}
	})
	return iconPath
}
