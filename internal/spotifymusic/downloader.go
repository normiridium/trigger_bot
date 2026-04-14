package spotifymusic

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Downloader struct {
	YTDLPBin      string
	ProxySocks    string
	AudioFormat   string
	AudioQuality  string
	ExtractorArgs string
}

func (d Downloader) DownloadByQuery(ctx context.Context, query string) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", errors.New("empty query")
	}
	tmpDir, err := os.MkdirTemp("", "spotify-audio-*")
	if err != nil {
		return "", err
	}
	outTpl := filepath.Join(tmpDir, "%(title)s.%(ext)s")
	args := []string{
		"-f", "bestaudio[ext=m4a]/bestaudio/best",
		"-x",
		"--audio-format", d.audioFormat(),
		"--audio-quality", d.audioQuality(),
		"--no-playlist",
		"--extractor-args", d.extractorArgs(),
		"--print", "after_move:filepath",
		"-o", outTpl,
		"ytsearch1:" + query,
	}
	if proxy := strings.TrimSpace(d.ProxySocks); proxy != "" {
		args = append([]string{"--proxy", "socks5://" + proxy}, args...)
	}
	bin := strings.TrimSpace(d.YTDLPBin)
	if bin == "" {
		bin = "yt-dlp"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), "PYTHONUNBUFFERED=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	var path string
	outScan := bufio.NewScanner(stdout)
	for outScan.Scan() {
		line := strings.TrimSpace(outScan.Text())
		if line != "" {
			path = line
		}
	}
	errBuf := new(strings.Builder)
	errScan := bufio.NewScanner(stderr)
	for errScan.Scan() {
		errBuf.WriteString(errScan.Text())
		errBuf.WriteString("\n")
	}
	if err := cmd.Wait(); err != nil {
		return "", fmt.Errorf("yt-dlp failed: %w: %s", err, strings.TrimSpace(errBuf.String()))
	}
	if path == "" {
		return "", errors.New("yt-dlp returned empty output path")
	}
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("downloaded file is missing: %w", err)
	}
	return path, nil
}

func (d Downloader) audioFormat() string {
	if v := strings.TrimSpace(d.AudioFormat); v != "" {
		return v
	}
	return "mp3"
}

func (d Downloader) audioQuality() string {
	if v := strings.TrimSpace(d.AudioQuality); v != "" {
		return v
	}
	return "320K"
}

func (d Downloader) extractorArgs() string {
	if v := strings.TrimSpace(d.ExtractorArgs); v != "" {
		return v
	}
	return "youtube:player_client=android,web"
}
