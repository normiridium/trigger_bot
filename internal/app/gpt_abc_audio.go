package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"trigger-admin-bot/internal/bottmp"
)

var (
	gptResponseABCFenceRe = regexp.MustCompile("(?is)```[ \t]*abc[ \t]*\n(.*?)```")
	gptResponseABCBareRe  = regexp.MustCompile(`(?m)^X:[ \t]*\d+`)
	gptABCBadCharsRe      = regexp.MustCompile(`(?i)(javascript:|data:|file:|@import|https?://)`)
)

// extractFirstABCFromGPTResponse returns the first ABC notation block from a
// GPT reply: either a fenced ```abc block or, when the model forgot the fence,
// the bare text starting at the X: header line.
func extractFirstABCFromGPTResponse(text string) (string, bool) {
	if m := gptResponseABCFenceRe.FindStringSubmatch(text); m != nil {
		if src := strings.TrimSpace(m[1]); src != "" {
			return src, true
		}
	}
	idx := gptResponseABCBareRe.FindStringIndex(text)
	if idx == nil {
		return "", false
	}
	rest := text[idx[0]:]
	maxBytes := envInt("GPT_ABC_RENDER_MAX_BYTES", 16*1024)
	if len(rest) > maxBytes {
		rest = rest[:maxBytes]
	}
	return strings.TrimSpace(rest), true
}

// renderGPTABCResponseToAudio renders an ABC tune into an OGG/Opus voice-note
// file and returns its path. The file lives under the bot temp dir and is
// reaped by the regular temp cleanup.
func renderGPTABCResponseToAudio(abcSource string) (string, error) {
	abcSource = strings.TrimSpace(abcSource)
	if abcSource == "" {
		return "", errors.New("empty abc source")
	}
	maxBytes := envInt("GPT_ABC_RENDER_MAX_BYTES", 16*1024)
	if len(abcSource) > maxBytes {
		return "", fmt.Errorf("abc source too large (%d bytes)", len(abcSource))
	}
	if !gptResponseABCBareRe.MatchString(abcSource) {
		return "", errors.New("abc source has no X: header")
	}
	if gptABCBadCharsRe.MatchString(abcSource) {
		return "", errors.New("abc source contains forbidden content")
	}

	abc2midiBin, err := systemDiagramBin("TRIGGER_BOT_ABC2MIDI_BIN", "abc2midi")
	if err != nil {
		return "", err
	}
	timidityBin, err := systemDiagramBin("TRIGGER_BOT_TIMIDITY_BIN", "timidity")
	if err != nil {
		return "", err
	}

	workDir, err := bottmp.MkdirTemp("gpt-abc-audio-*")
	if err != nil {
		return "", fmt.Errorf("create abc audio temp dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(workDir) }

	abcPath := filepath.Join(workDir, "input.abc")
	if err := os.WriteFile(abcPath, []byte(abcSource), 0o600); err != nil {
		cleanup()
		return "", fmt.Errorf("write abc input: %w", err)
	}

	timeout := time.Duration(envInt("GPT_ABC_RENDER_TIMEOUT_SEC", 30)) * time.Second
	if timeout < 10*time.Second {
		timeout = 10 * time.Second
	}
	if timeout > 120*time.Second {
		timeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Step 1: ABC -> MIDI.
	midBase := filepath.Join(workDir, "out.mid")
	cmd := exec.CommandContext(ctx, abc2midiBin, abcPath, "-o", midBase)
	cmd.Dir = workDir
	cmd.Env = richArticleRendererEnv(workDir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		cleanup()
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("abc2midi timed out after %s", timeout)
		}
		return "", fmt.Errorf("abc2midi failed: %w: %s", err, clipText(stderr.String(), 900))
	}
	midPath := firstExistingFile(midBase, workDir, "out1.mid")
	if midPath == "" {
		cleanup()
		return "", errors.New("abc2midi produced no midi file")
	}

	// Step 2: MIDI -> WAV (timidity with the system soundfont).
	wavPath := filepath.Join(workDir, "out.wav")
	cmd2 := exec.CommandContext(ctx, timidityBin, "-Ow", "-o", wavPath, midPath)
	cmd2.Dir = workDir
	cmd2.Env = richArticleRendererEnv(workDir)
	var stderr2 bytes.Buffer
	cmd2.Stderr = &stderr2
	if err := cmd2.Run(); err != nil {
		cleanup()
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("timidity timed out after %s", timeout)
		}
		return "", fmt.Errorf("timidity failed: %w: %s", err, clipText(stderr2.String(), 900))
	}
	if st, err := os.Stat(wavPath); err != nil || st.Size() == 0 {
		cleanup()
		return "", errors.New("timidity produced no audio")
	}

	// Step 3: WAV -> OGG/Opus voice note, capped at 120 seconds.
	oggPath := filepath.Join(workDir, "out.ogg")
	cmd3 := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error", "-y",
		"-i", wavPath,
		"-t", "120",
		"-c:a", "libopus", "-b:a", "64k", "-ac", "1",
		oggPath,
	)
	cmd3.Dir = workDir
	if err := cmd3.Run(); err != nil {
		cleanup()
		return "", fmt.Errorf("ffmpeg ogg encode failed: %w", err)
	}
	if st, err := os.Stat(oggPath); err != nil || st.Size() == 0 {
		cleanup()
		return "", errors.New("empty ogg output")
	}
	return oggPath, nil
}

func firstExistingFile(candidates ...string) string {
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Size() > 0 {
			return p
		}
	}
	return ""
}
