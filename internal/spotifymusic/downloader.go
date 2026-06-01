package spotifymusic

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"trigger-admin-bot/internal/bottmp"
)

type Downloader struct {
	YTDLPBin           string
	ProxySocks         string
	AudioFormat        string
	AudioQuality       string
	ExtractorArgs      string
	CookiesFile        string
	CookiesFromBrowser string
}

func (d Downloader) DownloadByQuery(ctx context.Context, query string) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", errors.New("empty query")
	}
	tmpDir, err := bottmp.MkdirTemp("spotify-audio-*")
	if err != nil {
		return "", err
	}
	outTpl := filepath.Join(tmpDir, "%(title)s.%(ext)s")
	var lastErr error
	for _, extractorArgs := range d.extractorArgCandidates() {
		path, err := d.downloadByQueryWithExtractor(ctx, query, outTpl, extractorArgs)
		if err == nil {
			return path, nil
		}
		lastErr = err
	}
	_ = os.RemoveAll(tmpDir)
	if lastErr != nil {
		return "", lastErr
	}
	return "", errors.New("yt-dlp candidates were not attempted")
}

func (d Downloader) downloadByQueryWithExtractor(ctx context.Context, query, outTpl, extractorArgs string) (string, error) {
	args := []string{
		"-f", "bestaudio[ext=m4a]/bestaudio/best",
		"-x",
		"--audio-format", d.audioFormat(),
		"--audio-quality", d.audioQuality(),
		"--no-playlist",
		"--quiet",
		"--no-warnings",
		"--extractor-args", extractorArgs,
		"--print", "after_move:filepath",
		"-o", outTpl,
		"ytsearch1:" + query,
	}
	args = append(d.ytDLPAuthArgs(), args...)
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
	errBuf := new(strings.Builder)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		raw, _ := io.ReadAll(stderr)
		errBuf.Write(raw)
	}()
	outScan := bufio.NewScanner(stdout)
	for outScan.Scan() {
		line := strings.TrimSpace(outScan.Text())
		if line != "" {
			path = line
		}
	}
	wg.Wait()
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

func (d Downloader) extractorArgCandidates() []string {
	seen := make(map[string]bool)
	candidates := []string{
		d.extractorArgs(),
		"youtube:player_client=android",
		"youtube:player_client=android,ios,web",
		"youtube:player_client=web",
	}
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true
		out = append(out, candidate)
	}
	return out
}

func (d Downloader) ytDLPAuthArgs() []string {
	if v := strings.TrimSpace(d.CookiesFromBrowser); v != "" {
		return []string{"--cookies-from-browser", v}
	}
	if v := strings.TrimSpace(d.CookiesFile); v != "" {
		return []string{"--cookies", v}
	}
	return nil
}
