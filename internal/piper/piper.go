// Package piper provides local neural text-to-speech via the Piper engine.
// The Piper binary and per-language voice models are downloaded on demand into
// the config dir (mirroring internal/whisper), so synthesis runs fully offline
// with no API usage. Voices are far more natural than espeak-ng, which remains
// the fallback when Piper is unavailable for the platform.
package piper

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"openuai/internal/logger"
	"openuai/internal/sysproc"
)

const piperVersion = "2023.11.14-2"
const voicesBaseURL = "https://huggingface.co/rhasspy/piper-voices/resolve/main"
const voicesJSONURL = voicesBaseURL + "/voices.json"

// Voice is a selectable Piper voice, derived from the online catalog.
type Voice struct {
	Code     string `json:"code"`     // unique id / model stem, e.g. es_ES-davefx-medium
	Name     string `json:"name"`     // speaker name
	Language string `json:"language"` // English language name
	Native   string `json:"native"`   // native language name
	Quality  string `json:"quality"`  // x_low | low | medium | high
	Speakers int    `json:"speakers"`
	Onnx     string `json:"-"` // repo-relative path to the .onnx
	Cfg      string `json:"-"` // repo-relative path to the .onnx.json
}

var (
	catalogMu    sync.Mutex
	catalogCache []Voice
)

// Catalog returns the full list of Piper voices, fetched online from the
// piper-voices repo and cached (in memory + on disk). Falls back to the disk
// cache when offline.
func Catalog(configDir string) ([]Voice, error) {
	catalogMu.Lock()
	defer catalogMu.Unlock()
	if catalogCache != nil {
		return catalogCache, nil
	}
	cachePath := filepath.Join(dirPath(configDir), "voices.json")
	raw, err := httpGetBytes(voicesJSONURL)
	if err != nil {
		if cached, rerr := os.ReadFile(cachePath); rerr == nil {
			logger.Info("Piper: using cached voices catalog (%v)", err)
			raw = cached
		} else {
			return nil, fmt.Errorf("fetch voices catalog: %w", err)
		}
	} else {
		os.MkdirAll(dirPath(configDir), 0o755)
		os.WriteFile(cachePath, raw, 0o644)
	}
	voices, err := parseCatalog(raw)
	if err != nil {
		return nil, err
	}
	catalogCache = voices
	return voices, nil
}

// parseCatalog turns the voices.json document into a sorted []Voice.
func parseCatalog(raw []byte) ([]Voice, error) {
	var m map[string]struct {
		Key      string `json:"key"`
		Name     string `json:"name"`
		Language struct {
			Code        string `json:"code"`
			NameEnglish string `json:"name_english"`
			NameNative  string `json:"name_native"`
		} `json:"language"`
		Quality     string                     `json:"quality"`
		NumSpeakers int                        `json:"num_speakers"`
		Files       map[string]json.RawMessage `json:"files"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse voices.json: %w", err)
	}
	out := make([]Voice, 0, len(m))
	for key, e := range m {
		var onnx, cfg string
		for f := range e.Files {
			switch {
			case strings.HasSuffix(f, ".onnx.json"):
				cfg = f
			case strings.HasSuffix(f, ".onnx"):
				onnx = f
			}
		}
		if onnx == "" || cfg == "" {
			continue
		}
		code := e.Key
		if code == "" {
			code = key
		}
		out = append(out, Voice{
			Code: code, Name: e.Name, Language: e.Language.NameEnglish,
			Native: e.Language.NameNative, Quality: e.Quality,
			Speakers: e.NumSpeakers, Onnx: onnx, Cfg: cfg,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Language != out[j].Language {
			return out[i].Language < out[j].Language
		}
		if out[i].Quality != out[j].Quality {
			return qualityRank(out[i].Quality) < qualityRank(out[j].Quality)
		}
		return out[i].Code < out[j].Code
	})
	return out, nil
}

func qualityRank(q string) int {
	switch q {
	case "high":
		return 0
	case "medium":
		return 1
	case "low":
		return 2
	case "x_low":
		return 3
	}
	return 4
}

func findVoice(configDir, code string) (Voice, error) {
	cat, err := Catalog(configDir)
	if err != nil {
		return Voice{}, err
	}
	for _, v := range cat {
		if v.Code == code {
			return v, nil
		}
	}
	return Voice{}, fmt.Errorf("unknown voice %q", code)
}

func httpGetBytes(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func dirPath(configDir string) string  { return filepath.Join(configDir, "piper-tts") }
func voiceDir(configDir string) string { return filepath.Join(dirPath(configDir), "voices") }

func binPath(configDir string) string {
	name := "piper"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(dirPath(configDir), "piper", name)
}

// platformAsset returns the release asset filename and whether it's a zip.
// Returns ("", false) for unsupported platforms.
func platformAsset() (string, bool) {
	switch runtime.GOOS {
	case "linux":
		if runtime.GOARCH == "arm64" {
			return "piper_linux_aarch64.tar.gz", false
		}
		return "piper_linux_x86_64.tar.gz", false
	case "darwin":
		if runtime.GOARCH == "arm64" {
			return "piper_macos_aarch64.tar.gz", false
		}
		return "piper_macos_x64.tar.gz", false
	case "windows":
		return "piper_windows_amd64.zip", true
	}
	return "", false
}

// Supported reports whether Piper has a binary for this platform.
func Supported() bool { a, _ := platformAsset(); return a != "" }

// Available reports whether the Piper binary is already installed.
func Available(configDir string) bool {
	_, err := os.Stat(binPath(configDir))
	return err == nil
}

// VoiceInstalled reports whether a given voice's model is present on disk.
// Voice files are named after their code (e.g. es_ES-davefx-medium.onnx).
func VoiceInstalled(configDir, code string) bool {
	_, err := os.Stat(filepath.Join(voiceDir(configDir), code+".onnx"))
	return err == nil
}

// EnsureBinary downloads and extracts the Piper binary if missing.
func EnsureBinary(configDir string) error {
	if Available(configDir) {
		return nil
	}
	asset, isZip := platformAsset()
	if asset == "" {
		return fmt.Errorf("piper not supported on %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if err := os.MkdirAll(dirPath(configDir), 0o755); err != nil {
		return err
	}
	url := fmt.Sprintf("https://github.com/rhasspy/piper/releases/download/%s/%s", piperVersion, asset)
	logger.Info("Piper: downloading binary from %s", url)
	archivePath := filepath.Join(dirPath(configDir), asset)
	if err := downloadFile(url, archivePath); err != nil {
		return fmt.Errorf("download piper: %w", err)
	}
	defer os.Remove(archivePath)

	logger.Info("Piper: extracting %s", asset)
	var err error
	if isZip {
		err = extractZip(archivePath, dirPath(configDir))
	} else {
		err = extractTarGz(archivePath, dirPath(configDir))
	}
	if err != nil {
		return fmt.Errorf("extract piper: %w", err)
	}
	os.Chmod(binPath(configDir), 0o755)
	if !Available(configDir) {
		return fmt.Errorf("piper binary missing after extraction")
	}
	logger.Info("Piper: installed %s", binPath(configDir))
	return nil
}

// EnsureVoice downloads a voice's model + config if missing.
func EnsureVoice(configDir, code string) error {
	v, err := findVoice(configDir, code)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(voiceDir(configDir), 0o755); err != nil {
		return err
	}
	onnx := filepath.Join(voiceDir(configDir), filepath.Base(v.Onnx))
	cfg := filepath.Join(voiceDir(configDir), filepath.Base(v.Cfg))

	if _, err := os.Stat(onnx); os.IsNotExist(err) {
		logger.Info("Piper: downloading voice %s", code)
		if err := downloadFile(voicesBaseURL+"/"+v.Onnx, onnx); err != nil {
			return fmt.Errorf("download voice model: %w", err)
		}
	}
	if _, err := os.Stat(cfg); os.IsNotExist(err) {
		if err := downloadFile(voicesBaseURL+"/"+v.Cfg, cfg); err != nil {
			return fmt.Errorf("download voice config: %w", err)
		}
	}
	return nil
}

// Speak synthesizes text with the given voice, returning 22kHz mono WAV bytes.
// It ensures the binary and the voice are present (downloading on demand).
func Speak(configDir, code, text string) ([]byte, error) {
	if err := EnsureBinary(configDir); err != nil {
		return nil, err
	}
	if err := EnsureVoice(configDir, code); err != nil {
		return nil, err
	}
	model := filepath.Join(voiceDir(configDir), code+".onnx")
	outFile := filepath.Join(os.TempDir(), "openuai_piper.wav")
	defer os.Remove(outFile)

	piperRoot := filepath.Dir(binPath(configDir)) // .../piper-tts/piper
	// --sentence_silence: pause between sentences. This build defaults to ~0, so
	// punctuation alone produces run-on speech (lists read with no gaps); set a
	// natural pause explicitly so each sentence / list item is separated.
	cmd := exec.Command(binPath(configDir), "-m", model, "--sentence_silence", "0.3", "-f", outFile)
	cmd.Dir = piperRoot
	// Piper loads its bundled shared libs and espeak-ng-data from its own dir.
	cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH="+piperRoot, "DYLD_LIBRARY_PATH="+piperRoot)
	cmd.Stdin = strings.NewReader(text)
	sysproc.HideConsole(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("piper synthesis failed: %v — %s", err, string(out))
	}
	return os.ReadFile(outFile)
}

// downloadFile downloads a URL to a local path atomically (follows redirects).
// Retries a few times: GitHub/CDN occasionally returns transient 5xx/timeouts.
func downloadFile(url, destPath string) error {
	var lastErr error
	for attempt := 1; attempt <= 4; attempt++ {
		if attempt > 1 {
			logger.Info("Piper: download retry %d for %s", attempt, url)
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}
		lastErr = tryDownload(url, destPath)
		if lastErr == nil {
			return nil
		}
	}
	return lastErr
}

func tryDownload(url, destPath string) error {
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
	_, err = io.Copy(f, resp.Body)
	f.Close()
	if err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, destPath)
}

// extractTarGz extracts a .tar.gz into destDir.
func extractTarGz(archivePath, destDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		target, err := safeJoin(destDir, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		case tar.TypeSymlink:
			// Piper's shared libs ship as soname symlinks (e.g.
			// libpiper_phonemize.so.1 -> ...so.1.2.0); without these the
			// binary fails to load its libraries at runtime.
			os.MkdirAll(filepath.Dir(target), 0o755)
			os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		case tar.TypeLink:
			os.MkdirAll(filepath.Dir(target), 0o755)
			linkTarget, err := safeJoin(destDir, hdr.Linkname)
			if err != nil {
				return err
			}
			os.Remove(target)
			if err := os.Link(linkTarget, target); err != nil {
				return err
			}
		}
	}
	return nil
}

// extractZip extracts a .zip into destDir.
func extractZip(archivePath, destDir string) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, zf := range r.File {
		target, err := safeJoin(destDir, zf.Name)
		if err != nil {
			return err
		}
		if zf.FileInfo().IsDir() {
			os.MkdirAll(target, 0o755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, zf.Mode())
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		out.Close()
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// safeJoin prevents path traversal (zip/tar slip) from archive entry names.
func safeJoin(base, name string) (string, error) {
	target := filepath.Join(base, name)
	if !strings.HasPrefix(target, filepath.Clean(base)+string(os.PathSeparator)) && target != filepath.Clean(base) {
		return "", fmt.Errorf("unsafe archive path: %s", name)
	}
	return target, nil
}
