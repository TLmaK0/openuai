package updater

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"openuai/internal/logger"
)

const (
	repoOwner = "tlmak0"
	repoName  = "openuai"
	apiURL    = "https://api.github.com/repos/" + repoOwner + "/" + repoName + "/releases/latest"
)

// Release holds info about a GitHub release.
type Release struct {
	TagName string `json:"tag_name"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
	Assets  []Asset `json:"assets"`
}

// Asset is a downloadable file in a release.
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// UpdateInfo is emitted to the frontend when a new version is available.
type UpdateInfo struct {
	CurrentVersion string `json:"current_version"`
	NewVersion     string `json:"new_version"`
	ReleaseNotes   string `json:"release_notes"`
	ReleaseURL     string `json:"release_url"`
	DownloadURL    string `json:"download_url"`
	DownloadSize   int64  `json:"download_size"`
}

// CheckForUpdate queries GitHub for the latest release and returns UpdateInfo
// if a newer version is available. Returns nil if up-to-date or on error.
func CheckForUpdate(currentVersion, skippedVersion string) *UpdateInfo {
	if currentVersion == "" || currentVersion == "dev" {
		logger.Info("Updater: skipping check (dev build)")
		return nil
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(apiURL)
	if err != nil {
		logger.Error("Updater: failed to check for updates: %s", err.Error())
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		logger.Error("Updater: GitHub API returned %d", resp.StatusCode)
		return nil
	}

	var release Release
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		logger.Error("Updater: failed to parse release: %s", err.Error())
		return nil
	}

	latest := release.TagName
	if !isNewer(currentVersion, latest) {
		logger.Info("Updater: up to date (%s)", currentVersion)
		return nil
	}

	if skippedVersion != "" && latest == skippedVersion {
		logger.Info("Updater: %s is skipped by user", latest)
		return nil
	}

	asset := findAsset(release.Assets)
	if asset == nil {
		logger.Error("Updater: no matching asset for %s/%s", runtime.GOOS, runtime.GOARCH)
		return nil
	}

	logger.Info("Updater: new version available: %s → %s", currentVersion, latest)
	return &UpdateInfo{
		CurrentVersion: currentVersion,
		NewVersion:     latest,
		ReleaseNotes:   release.Body,
		ReleaseURL:     release.HTMLURL,
		DownloadURL:    asset.BrowserDownloadURL,
		DownloadSize:   asset.Size,
	}
}

// DownloadAndApply downloads the new binary and replaces the current one.
// Returns nil on success. The caller should restart the app.
func DownloadAndApply(downloadURL string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot find current executable: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("cannot resolve executable path: %w", err)
	}

	logger.Info("Updater: downloading %s", downloadURL)
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(downloadURL)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}

	// Write to temp file in the same directory (ensures same filesystem for rename)
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, "openuai-update-*")
	if err != nil {
		return fmt.Errorf("cannot create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("download write failed: %w", err)
	}
	tmp.Close()

	// Make executable
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("chmod failed: %w", err)
	}

	// Rename: old binary → .old backup, new → current
	backupPath := exe + ".old"
	os.Remove(backupPath) // clean up any previous backup

	if err := os.Rename(exe, backupPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("cannot backup current binary: %w", err)
	}

	if err := os.Rename(tmpPath, exe); err != nil {
		// Try to restore backup
		os.Rename(backupPath, exe)
		return fmt.Errorf("cannot install new binary: %w", err)
	}

	// Clean up backup
	os.Remove(backupPath)

	logger.Info("Updater: update applied successfully to %s", exe)
	return nil
}

// isNewer returns true if latest is a higher semver than current.
// Both should be in "vX.Y.Z" format.
func isNewer(current, latest string) bool {
	cur := parseVersion(current)
	lat := parseVersion(latest)
	if cur == nil || lat == nil {
		return false
	}
	for i := 0; i < 3; i++ {
		if lat[i] > cur[i] {
			return true
		}
		if lat[i] < cur[i] {
			return false
		}
	}
	return false
}

func parseVersion(v string) []int {
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 {
		return nil
	}
	nums := make([]int, 3)
	for i, p := range parts {
		// Strip any pre-release suffix
		p = strings.SplitN(p, "-", 2)[0]
		n := 0
		for _, ch := range p {
			if ch < '0' || ch > '9' {
				return nil
			}
			n = n*10 + int(ch-'0')
		}
		nums[i] = n
	}
	return nums
}

// findAsset returns the asset matching the current OS/arch.
func findAsset(assets []Asset) *Asset {
	target := assetName()
	if target == "" {
		return nil
	}
	for i, a := range assets {
		if a.Name == target {
			return &assets[i]
		}
	}
	return nil
}

// assetName returns the expected filename for the current platform.
func assetName() string {
	switch {
	case runtime.GOOS == "linux" && runtime.GOARCH == "amd64":
		return "openuai-linux-amd64"
	case runtime.GOOS == "linux" && runtime.GOARCH == "arm64":
		return "openuai-linux-arm64"
	case runtime.GOOS == "darwin":
		return "openuai-macos-universal.zip"
	case runtime.GOOS == "windows" && runtime.GOARCH == "amd64":
		return "openuai-windows-amd64.exe"
	default:
		return ""
	}
}
