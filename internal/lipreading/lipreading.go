package lipreading

import (
	"encoding/base64"
	"fmt"
	"openuai/internal/logger"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	modelFilename = "vsr_trlrs2lrs3vox2avsp_base.pth"
	gdFileID      = "1r1kx7l9sWnDOCnaFHIGvOtzuhFyFA88_"
)

// ModelDir returns the lipreading model directory inside configDir.
func ModelDir(configDir string) string {
	return filepath.Join(configDir, "lipreading")
}

// ModelPath returns the full path to the VSR model.
func ModelPath(configDir string) string {
	return filepath.Join(ModelDir(configDir), modelFilename)
}

// IsModelReady returns true if the model file exists.
func IsModelReady(configDir string) bool {
	info, err := os.Stat(ModelPath(configDir))
	return err == nil && info.Size() > 100_000_000 // >100MB sanity check
}

// DownloadModel downloads the VSR model from Google Drive using gdown (handles auth/cookies).
// onProgress is called periodically with bytes downloaded so far.
func DownloadModel(configDir string, onProgress func(downloaded int64)) error {
	dir := ModelDir(configDir)
	os.MkdirAll(dir, 0o755)

	destPath := ModelPath(configDir)

	logger.Info("Lipreading: downloading model to %s via gdown", destPath)

	// Remove partial downloads
	os.Remove(destPath)

	// Start gdown in background
	cmd := exec.Command("python3", "-c", fmt.Sprintf(
		`import gdown; gdown.download(id="%s", output="%s", quiet=False)`,
		gdFileID, destPath,
	))
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start gdown: %w", err)
	}

	// Monitor file size in a goroutine for progress
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case err := <-done:
			if err != nil {
				logger.Error("Lipreading: gdown failed: %s", err)
				return fmt.Errorf("download failed: %w", err)
			}
			// Final progress update
			if info, err := os.Stat(destPath); err == nil && onProgress != nil {
				onProgress(info.Size())
			}
			// Verify
			info, err := os.Stat(destPath)
			if err != nil || info.Size() < 100_000_000 {
				os.Remove(destPath)
				return fmt.Errorf("download too small or missing")
			}
			logger.Info("Lipreading: model downloaded (%d MB)", info.Size()/1024/1024)
			return nil
		case <-ticker.C:
			if onProgress == nil {
				continue
			}
			// gdown writes to a .part file during download, then renames
			var best int64
			if info, err := os.Stat(destPath); err == nil {
				best = info.Size()
			}
			// Scan for .part files
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				if strings.HasSuffix(e.Name(), ".part") {
					if info, err := e.Info(); err == nil && info.Size() > best {
						best = info.Size()
					}
				}
			}
			if best > 0 {
				onProgress(best)
			}
		}
	}
}

// Recorder captures video from the webcam using ffmpeg.
type Recorder struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	tmpFile string
	active  bool
}

// NewRecorder creates a new video recorder.
func NewRecorder() *Recorder {
	return &Recorder{}
}

// Start begins recording video from the default webcam.
func (r *Recorder) Start() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.active {
		return fmt.Errorf("already recording")
	}

	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return fmt.Errorf("ffmpeg not found (required for lip reading)")
	}

	r.tmpFile = filepath.Join(os.TempDir(), "openuai_lipreading.mp4")
	os.Remove(r.tmpFile)

	// Record from /dev/video0 at 25fps, 640x480, no audio
	r.cmd = exec.Command(ffmpeg,
		"-y",
		"-f", "v4l2",
		"-framerate", "25",
		"-video_size", "640x480",
		"-i", "/dev/video0",
		"-c:v", "libx264",
		"-preset", "ultrafast",
		"-an",
		r.tmpFile,
	)

	if err := r.cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}

	r.active = true
	logger.Info("Lipreading: video recording started (pid %d)", r.cmd.Process.Pid)
	return nil
}

// Stop ends recording and returns video as base64.
func (r *Recorder) Stop() (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.active || r.cmd == nil || r.cmd.Process == nil {
		return "", fmt.Errorf("not recording")
	}

	// Send SIGINT to ffmpeg so it writes the trailer
	r.cmd.Process.Signal(os.Interrupt)
	done := make(chan struct{})
	go func() { r.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		r.cmd.Process.Kill()
		<-done
	}
	r.active = false

	logger.Info("Lipreading: video recording stopped")

	data, err := os.ReadFile(r.tmpFile)
	if err != nil {
		return "", fmt.Errorf("read recording: %w", err)
	}
	os.Remove(r.tmpFile)

	if len(data) < 1000 {
		return "", fmt.Errorf("recording too short")
	}

	logger.Info("Lipreading: recorded %d bytes", len(data))
	return base64.StdEncoding.EncodeToString(data), nil
}

// IsRecording returns whether recording is active.
func (r *Recorder) IsRecording() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active
}

// Transcribe runs the lip reading inference on a base64-encoded video.
// Returns the transcript text or an error.
func Transcribe(videoBase64, configDir string) (string, error) {
	modelPath := ModelPath(configDir)
	if !IsModelReady(configDir) {
		return "", fmt.Errorf("lip reading model not downloaded")
	}

	// Write video to temp file
	videoBytes, err := base64.StdEncoding.DecodeString(videoBase64)
	if err != nil {
		return "", fmt.Errorf("decode base64: %w", err)
	}

	tmpDir := os.TempDir()
	videoFile := filepath.Join(tmpDir, "openuai_lipreading.webm")
	if err := os.WriteFile(videoFile, videoBytes, 0o600); err != nil {
		return "", fmt.Errorf("write temp video: %w", err)
	}
	defer os.Remove(videoFile)

	// Convert to mp4 with ffmpeg (mediapipe needs proper container)
	mp4File := filepath.Join(tmpDir, "openuai_lipreading.mp4")
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return "", fmt.Errorf("ffmpeg not found (required for lip reading)")
	}
	convCmd := exec.Command(ffmpeg, "-y", "-i", videoFile, "-r", "25", "-c:v", "libx264", "-an", mp4File)
	if out, err := convCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("ffmpeg conversion failed: %s: %w", string(out), err)
	}
	defer os.Remove(mp4File)

	logger.Info("Lipreading: running inference on %d bytes video", len(videoBytes))

	// Run Python inference script
	scriptPath := filepath.Join(ModelDir(configDir), "infer.py")
	if err := ensureInferScript(scriptPath); err != nil {
		return "", err
	}

	cmd := exec.Command("python3", scriptPath, mp4File, modelPath)
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("inference failed: %s\n%s", err, string(exitErr.Stderr))
		}
		return "", fmt.Errorf("inference failed: %w", err)
	}

	transcript := strings.TrimSpace(string(output))
	logger.Info("Lipreading: transcript → %q", transcript)
	return transcript, nil
}

// EnsureRepo clones the auto_avsr repo if not present.
func EnsureRepo(configDir string) error {
	repoDir := filepath.Join(ModelDir(configDir), "auto_avsr")
	if _, err := os.Stat(filepath.Join(repoDir, "lightning.py")); err == nil {
		return nil // already cloned
	}
	logger.Info("Lipreading: cloning auto_avsr repo")
	cmd := exec.Command("git", "clone", "--depth=1", "https://github.com/mpc001/auto_avsr.git", repoDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone failed: %s: %w", string(out), err)
	}
	logger.Info("Lipreading: auto_avsr repo ready")
	return nil
}

// EnsurePythonDeps installs required Python packages if missing.
func EnsurePythonDeps() error {
	// Check if key packages are importable
	cmd := exec.Command("python3", "-c", "import mediapipe, sentencepiece, skimage, gdown")
	if cmd.Run() == nil {
		return nil
	}
	logger.Info("Lipreading: installing Python dependencies")
	pip := exec.Command("pip3", "install", "sentencepiece", "av", "mediapipe==0.10.14", "scikit-image", "gdown")
	if out, err := pip.CombinedOutput(); err != nil {
		return fmt.Errorf("pip install failed: %s: %w", string(out), err)
	}
	return nil
}

// ensureInferScript writes the Python inference script to disk if missing.
func ensureInferScript(path string) error {
	// Always overwrite to keep up-to-date
	return os.WriteFile(path, []byte(inferScript), 0o644)
}

const inferScript = `#!/usr/bin/env python3
"""Lip reading inference — called by OpenUAI Go backend."""
import sys, os, argparse
import numpy as np
import torch
import torchvision

# Add auto_avsr repo to path (expect it cloned alongside model)
repo_dir = os.path.join(os.path.dirname(__file__), "auto_avsr")
sys.path.insert(0, repo_dir)

from lightning import ModelModule
from datamodule.transforms import VideoTransform
from preparation.detectors.mediapipe.detector import LandmarksDetector
from preparation.detectors.mediapipe.video_process import VideoProcess

def main():
    video_path = sys.argv[1]
    model_path = sys.argv[2]

    # Load video
    video = torchvision.io.read_video(os.path.abspath(video_path), pts_unit="sec")[0].numpy()

    # Detect face and crop mouth
    ld = LandmarksDetector()
    landmarks = ld.detect(video, ld.full_range_detector)
    detected = sum(1 for l in landmarks if l is not None)
    if detected == 0:
        landmarks = ld.detect(video, ld.short_range_detector)
        detected = sum(1 for l in landmarks if l is not None)
    if detected == 0:
        print("", end="")
        sys.exit(0)

    vp = VideoProcess(convert_gray=False)
    video = vp(video, landmarks)
    video = torch.tensor(video).permute((0, 3, 1, 2))
    video = VideoTransform(subset="test")(video)

    # Load model and run inference
    parser = argparse.ArgumentParser()
    args, _ = parser.parse_known_args(args=[])
    args.modality = "video"
    ckpt = torch.load(model_path, map_location="cpu")
    mm = ModelModule(args)
    mm.model.load_state_dict(ckpt)
    mm.eval()

    with torch.no_grad():
        transcript = mm(video)

    print(transcript, end="")

if __name__ == "__main__":
    main()
`
