package whisper

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"openuai/internal/logger"
	"openuai/internal/sysproc"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
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

	// Model first — the binary's GPU smoke test below needs it.
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
		// The CUDA build is only usable if it actually transcribes here: the
		// runtime may be missing, or — even when the GPU is found — the driver can
		// be too old for the build's CUDA toolchain (PTX "unsupported toolchain"),
		// which crashes mid-inference. A real smoke test catches all of these, so
		// fall back to the CPU build instead of leaving STT broken.
		if isCUDAVariant(variant) && !canTranscribe(binPath, modelPath) {
			cpu := variantName(false)
			logger.Error("Whisper: CUDA build %q can't transcribe (runtime missing or driver too old for its CUDA toolchain) — falling back to %s", variant, cpu)
			if err := downloadVariant(cpu, whisperVersion, binPath); err != nil {
				return fmt.Errorf("download whisper-cli (cpu fallback): %w", err)
			}
		}
		os.WriteFile(versionFile, []byte(whisperVersion), 0o644)
		os.WriteFile(variantFile, []byte(variant), 0o644) // record intent (avoids re-download loops; retried on version bump)
		logger.Info("Whisper: installed %s", binPath)
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

// canTranscribe reports whether the binary can actually run a transcription end
// to end. A bare --help check isn't enough for CUDA: the binary can load its
// libraries and detect the GPU yet still abort mid-inference when the driver is
// too old for the build's CUDA toolchain. So we run a real (tiny, silent)
// transcription and require a clean exit.
func canTranscribe(binPath, modelPath string) bool {
	wav := filepath.Join(os.TempDir(), "openuai_whisper_smoketest.wav")
	if err := os.WriteFile(wav, silentWAV(8000), 0o600); err != nil { // 0.5s @16kHz mono
		return true // can't write the probe — don't block install, assume usable
	}
	defer os.Remove(wav)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binPath, "-m", modelPath, "-f", wav, "-l", "en", "-nt", "-np")
	sysproc.HideConsole(cmd)
	err := cmd.Run()
	if err != nil {
		logger.Info("Whisper: smoke test failed for %s: %v", filepath.Base(binPath), err)
	}
	return err == nil
}

// silentWAV builds a minimal 16 kHz mono 16-bit PCM WAV of n silent samples.
func silentWAV(n int) []byte {
	dataLen := n * 2
	buf := make([]byte, 44+dataLen)
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:8], uint32(36+dataLen))
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:20], 16) // fmt chunk size
	binary.LittleEndian.PutUint16(buf[20:22], 1)  // PCM
	binary.LittleEndian.PutUint16(buf[22:24], 1)  // mono
	binary.LittleEndian.PutUint32(buf[24:28], 16000)
	binary.LittleEndian.PutUint32(buf[28:32], 16000*2) // byte rate
	binary.LittleEndian.PutUint16(buf[32:34], 2)       // block align
	binary.LittleEndian.PutUint16(buf[34:36], 16)      // bits
	copy(buf[36:40], "data")
	binary.LittleEndian.PutUint32(buf[40:44], uint32(dataLen))
	return buf // samples already zero (silence)
}

// hasCUDASupport checks if NVIDIA GPU drivers are available.
func hasCUDASupport() bool {
	_, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return false
	}
	// Verify nvidia-smi actually works (driver loaded)
	cmd := exec.Command("nvidia-smi", "--query-gpu=name", "--format=csv,noheader")
	sysproc.HideConsole(cmd)
	out, err := cmd.Output()
	return err == nil && len(strings.TrimSpace(string(out))) > 0
}

// downloadFile downloads a URL to a local path atomically.
func downloadFile(url, destPath string) error {
	// Retry: the CUDA binary is ~100MB and transient TLS/connection timeouts are
	// common on large assets.
	var lastErr error
	for attempt := 1; attempt <= 4; attempt++ {
		if attempt > 1 {
			logger.Info("Whisper: download retry %d", attempt)
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}
		lastErr = tryDownloadFile(url, destPath)
		if lastErr == nil {
			return nil
		}
	}
	return lastErr
}

func tryDownloadFile(url, destPath string) error {
	client := &http.Client{Timeout: 5 * time.Minute} // generous: large CUDA asset
	resp, err := client.Get(url)
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
