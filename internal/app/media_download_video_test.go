package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestVideoHasVideoTrackAcceptsSilentVideo(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is not installed")
	}

	path := filepath.Join(t.TempDir(), "silent.mp4")
	cmd := exec.Command(
		"ffmpeg",
		"-nostdin", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "color=c=black:s=64x64:r=1",
		"-t", "1", "-an", "-c:v", "libx264", "-pix_fmt", "yuv420p",
		path,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create silent video: %v: %s", err, out)
	}
	if !videoHasVideoTrack(path) {
		t.Fatal("silent video with a valid video stream was rejected")
	}
}

func TestVideoHasVideoTrackRejectsNonVideoFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-video.txt")
	if err := os.WriteFile(path, []byte("not a video"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if videoHasVideoTrack(path) {
		t.Fatal("non-video file was accepted as video")
	}
}
