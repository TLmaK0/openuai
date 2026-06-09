package whisper

import (
	"fmt"
	"io"
	"net/http"
	"openuai/internal/logger"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	ghRepo       = "TLmaK0/openuai"
	defaultModel = "small"
	modelBaseURL = "https://huggingface.co/ggerganov/whisper.cpp/resolve/main"
)

// EnsureReady checks that whisper-cli and the default model are present.
// If missing, downloads them. whisperVersion comes from whisper-version.txt (embedded at build).
func EnsureReady(configDir, whisperVersion, model string) error {
	if model == "" {
		model = defaultModel
	}

	binDir := filepath.Join(configDir, "bin")
	modelDir := filepath.Join(configDir, "models")
	os.MkdirAll(binDir, 0o755)
	os.MkdirAll(modelDir, 0o755)

	// Check whisper-cli
	binName := "whisper-cli"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	binPath := filepath.Join(binDir, binName)

	// Re-download when the binary is missing, the version changed, or the
	// preferred variant changed (e.g. a GPU appeared since last run → switch the
	// CPU build for the CUDA one). The installed variant is recorded so we don't
	// re-download every launch.
	versionFile := filepath.Join(binDir, "whisper-version")
	variantFile := filepath.Join(binDir, "whisper-variant")
	variant := detectVariant()
	needDownload := false
	if _, err := os.Stat(binPath); os.IsNotExist(err) {
		needDownload = true
	} else if data, err := os.ReadFile(versionFile); err != nil || strings.TrimSpace(string(data)) != whisperVersion {
		needDownload = true
	} else if v, err := os.ReadFile(variantFile); err != nil || strings.TrimSpace(string(v)) != variant {
		needDownload = true
	}

	if needDownload {
		if err := downloadVariant(variant, whisperVersion, binPath); err != nil {
			return fmt.Errorf("download whisper-cli: %w", err)
		}
		// If we grabbed the CUDA build but it can't load (e.g. the CUDA runtime
		// isn't installed/compatible), fall back to the CPU build so STT keeps
		// working instead of breaking outright.
		if isCUDAVariant(variant) && !runnable(binPath) {
			cpu := variantName(false)
			logger.Error("Whisper: CUDA build %q won't run (CUDA runtime missing/incompatible?) — falling back to %s", variant, cpu)
			if err := downloadVariant(cpu, whisperVersion, binPath); err != nil {
				return fmt.Errorf("download whisper-cli (cpu fallback): %w", err)
			}
		}
		os.WriteFile(versionFile, []byte(whisperVersion), 0o644)
		os.WriteFile(variantFile, []byte(variant), 0o644) // record intent (avoids re-download loops; retried on version bump)
		logger.Info("Whisper: installed %s", binPath)
	}

	// Check model
	modelFile := fmt.Sprintf("ggml-%s.bin", model)
	modelPath := filepath.Join(modelDir, modelFile)
	if _, err := os.Stat(modelPath); os.IsNotExist(err) {
		url := fmt.Sprintf("%s/%s", modelBaseURL, modelFile)
		logger.Info("Whisper: downloading model %s from %s", modelFile, url)
		if err := downloadFile(url, modelPath); err != nil {
			return fmt.Errorf("download model: %w", err)
		}
		logger.Info("Whisper: model installed %s", modelPath)
	}

	return nil
}

// BinPath returns the path to whisper-cli in the config dir, or falls back to system PATH.
func BinPath(configDir string) string {
	binName := "whisper-cli"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	localPath := filepath.Join(configDir, "bin", binName)
	if _, err := os.Stat(localPath); err == nil {
		return localPath
	}
	if p, err := exec.LookPath(binName); err == nil {
		return p
	}
	return ""
}

// ModelPath returns the path to a whisper model, checking configDir first then legacy path.
func ModelPath(configDir, model string) string {
	if model == "" {
		model = defaultModel
	}
	p := filepath.Join(configDir, "models", "ggml-"+model+".bin")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	// Legacy fallback
	home, _ := os.UserHomeDir()
	legacy := filepath.Join(home, ".local", "share", "whisper-cpp", "ggml-"+model+".bin")
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	return p // return expected path even if missing
}

// ModelReady reports whether the given model file is present on disk.
func ModelReady(configDir, model string) bool {
	if model == "" {
		model = defaultModel
	}
	p := filepath.Join(configDir, "models", "ggml-"+model+".bin")
	if _, err := os.Stat(p); err == nil {
		return true
	}
	home, _ := os.UserHomeDir()
	legacy := filepath.Join(home, ".local", "share", "whisper-cpp", "ggml-"+model+".bin")
	_, err := os.Stat(legacy)
	return err == nil
}

// detectVariant returns the binary name to use: the CUDA build when an NVIDIA
// GPU is detected, otherwise the CPU build.
func detectVariant() string { return variantName(hasCUDASupport()) }

// variantName returns the release asset name for the current platform, picking
// the CUDA build when cuda is true (and one exists for the platform).
func variantName(cuda bool) string {
	goos := runtime.GOOS
	goarch := runtime.GOARCH
	switch {
	case goos == "linux" && goarch == "amd64" && cuda:
		return "whisper-cli-linux-amd64-cuda"
	case goos == "linux" && goarch == "amd64":
		return "whisper-cli-linux-amd64"
	case goos == "linux" && goarch == "arm64":
		return "whisper-cli-linux-arm64"
	case goos == "darwin":
		return "whisper-cli-macos-universal"
	case goos == "windows" && cuda:
		return "whisper-cli-windows-amd64-cuda.exe"
	case goos == "windows":
		return "whisper-cli-windows-amd64.exe"
	default:
		return fmt.Sprintf("whisper-cli-%s-%s", goos, goarch)
	}
}

// isCUDAVariant reports whether an asset name is a CUDA build.
func isCUDAVariant(variant string) bool { return strings.Contains(variant, "-cuda") }

// downloadVariant fetches a whisper-cli release asset to dest and makes it
// executable.
func downloadVariant(variant, whisperVersion, dest string) error {
	url := fmt.Sprintf("https://github.com/%s/releases/download/whisper-cpp-v%s/%s", ghRepo, whisperVersion, variant)
	logger.Info("Whisper: downloading %s from %s", variant, url)
	if err := downloadFile(url, dest); err != nil {
		return err
	}
	return os.Chmod(dest, 0o755)
}

// runnable reports whether the binary can actually start — i.e. its shared
// libraries resolve. Used to detect a CUDA build that can't load its runtime so
// we can fall back to the CPU build.
func runnable(binPath string) bool {
	out, err := exec.Command(binPath, "--help").CombinedOutput()
	if err == nil {
		return true
	}
	s := string(out)
	return !strings.Contains(s, "shared librar") && !strings.Contains(s, "cannot open shared object")
}

// hasCUDASupport checks if NVIDIA GPU drivers are available.
func hasCUDASupport() bool {
	_, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return false
	}
	// Verify nvidia-smi actually works (driver loaded)
	cmd := exec.Command("nvidia-smi", "--query-gpu=name", "--format=csv,noheader")
	out, err := cmd.Output()
	return err == nil && len(strings.TrimSpace(string(out))) > 0
}

// downloadFile downloads a URL to a local path atomically.
func downloadFile(url, destPath string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, url)
	}

	tmpPath := destPath + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return err
	}

	written, err := io.Copy(f, resp.Body)
	f.Close()
	if err != nil {
		os.Remove(tmpPath)
		return err
	}

	logger.Info("Whisper: downloaded %d bytes", written)
	return os.Rename(tmpPath, destPath)
}
