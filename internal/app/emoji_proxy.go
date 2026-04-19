package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type emojiProxyService struct {
	Token  string
	Client *http.Client
}

type emojiItem struct {
	CustomEmojiID string `json:"custom_emoji_id"`
	Emoji         string `json:"emoji"`
	SetName       string `json:"set_name"`
	FileID        string `json:"file_id"`
	ThumbFileID   string `json:"thumb_file_id"`
}

type emojiSet struct {
	SetName string      `json:"set_name"`
	Title   string      `json:"title"`
	Items   []emojiItem `json:"items"`
}

type tgSticker struct {
	CustomEmojiID string `json:"custom_emoji_id"`
	SetName       string `json:"set_name"`
	Emoji         string `json:"emoji"`
	FileID        string `json:"file_id"`
	Thumb         *struct {
		FileID string `json:"file_id"`
	} `json:"thumb"`
}

type tgStickerSet struct {
	Name     string      `json:"name"`
	Title    string      `json:"title"`
	Stickers []tgSticker `json:"stickers"`
}

type tgResp[T any] struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
	Result      T      `json:"result"`
}

func (s emojiProxyService) Enabled() bool {
	return strings.TrimSpace(s.Token) != ""
}

func (s emojiProxyService) ResolveSetByEmojiID(ctx context.Context, id string) (emojiSet, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return emojiSet{}, errors.New("emoji id is empty")
	}
	if !s.Enabled() {
		return emojiSet{}, errors.New("telegram token is empty")
	}
	stickers, err := s.getCustomEmojiStickers(ctx, []string{id})
	if err != nil {
		return emojiSet{}, err
	}
	if len(stickers) == 0 {
		return emojiSet{}, errors.New("emoji not found")
	}
	setName := strings.TrimSpace(stickers[0].SetName)
	if setName == "" {
		item := mapSticker(stickers[0])
		return emojiSet{Items: []emojiItem{item}}, nil
	}
	set, err := s.getStickerSet(ctx, setName)
	if err != nil {
		item := mapSticker(stickers[0])
		item.SetName = setName
		return emojiSet{SetName: setName, Title: setName, Items: []emojiItem{item}}, nil
	}
	items := make([]emojiItem, 0, len(set.Stickers))
	for _, st := range set.Stickers {
		if strings.TrimSpace(st.CustomEmojiID) == "" {
			continue
		}
		items = append(items, mapSticker(st))
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].CustomEmojiID < items[j].CustomEmojiID
	})
	return emojiSet{
		SetName: strings.TrimSpace(set.Name),
		Title:   strings.TrimSpace(set.Title),
		Items:   items,
	}, nil
}

func mapSticker(st tgSticker) emojiItem {
	out := emojiItem{
		CustomEmojiID: strings.TrimSpace(st.CustomEmojiID),
		Emoji:         strings.TrimSpace(st.Emoji),
		SetName:       strings.TrimSpace(st.SetName),
		FileID:        strings.TrimSpace(st.FileID),
	}
	if st.Thumb != nil {
		out.ThumbFileID = strings.TrimSpace(st.Thumb.FileID)
	}
	return out
}

func (s emojiProxyService) getCustomEmojiStickers(ctx context.Context, ids []string) ([]tgSticker, error) {
	payload := map[string]any{"custom_emoji_ids": ids}
	var out []tgSticker
	if err := s.call(ctx, "getCustomEmojiStickers", payload, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s emojiProxyService) getStickerSet(ctx context.Context, name string) (tgStickerSet, error) {
	payload := map[string]any{"name": strings.TrimSpace(name)}
	var out tgStickerSet
	if err := s.call(ctx, "getStickerSet", payload, &out); err != nil {
		return tgStickerSet{}, err
	}
	return out, nil
}

func (s emojiProxyService) ResolveFileURL(ctx context.Context, fileID string) (string, error) {
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return "", errors.New("file id is empty")
	}
	var out struct {
		FilePath string `json:"file_path"`
	}
	if err := s.call(ctx, "getFile", map[string]any{"file_id": fileID}, &out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.FilePath) == "" {
		return "", errors.New("telegram returned empty file_path")
	}
	return fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", strings.TrimSpace(s.Token), strings.TrimLeft(out.FilePath, "/")), nil
}

func (s emojiProxyService) FetchFile(ctx context.Context, fileID string) ([]byte, string, error) {
	u, err := s.ResolveFileURL(ctx, fileID)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, "", fmt.Errorf("telegram file status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, "", err
	}
	ctype := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	return body, ctype, nil
}

func (s emojiProxyService) FetchPreviewImage(ctx context.Context, fileID string) ([]byte, string, error) {
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return nil, "", errors.New("file id is empty")
	}
	fileURL, err := s.ResolveFileURL(ctx, fileID)
	if err != nil {
		return nil, "", err
	}
	ext := strings.ToLower(strings.TrimSpace(filepath.Ext(fileURL)))
	switch ext {
	case ".webp", ".png", ".jpg", ".jpeg", ".gif":
		return s.FetchFile(ctx, fileID)
	case ".webm", ".mp4", ".mov":
		return s.convertVideoURLToAnimatedWEBP(ctx, fileURL)
	case ".tgs":
		return s.convertTGSToWEBP(ctx, fileID)
	default:
		return s.FetchFile(ctx, fileID)
	}
}

func (s emojiProxyService) convertVideoURLToAnimatedWEBP(ctx context.Context, fileURL string) ([]byte, string, error) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, "", errors.New("ffmpeg not found for animated preview conversion")
	}
	tctx := ctx
	if tctx == nil {
		tctx = context.Background()
	}
	if _, hasDeadline := tctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		tctx, cancel = context.WithTimeout(tctx, 12*time.Second)
		defer cancel()
	}
	cmd := exec.CommandContext(tctx,
		"ffmpeg",
		"-hide_banner",
		"-loglevel", "error",
		"-i", fileURL,
		"-an",
		"-t", "1.8",
		"-vf", "fps=12,scale=96:-1:flags=lanczos:force_original_aspect_ratio=decrease",
		"-loop", "0",
		"-quality", "70",
		"-compression_level", "6",
		"-preset", "picture",
		"-f", "webp",
		"pipe:1",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, "", fmt.Errorf("ffmpeg preview conversion failed: %v", err)
	}
	if len(out) == 0 {
		return nil, "", errors.New("ffmpeg preview conversion returned empty output")
	}
	return out, "image/webp", nil
}

func (s emojiProxyService) convertTGSToWEBP(ctx context.Context, fileID string) ([]byte, string, error) {
	tool, _ := findFirstTool("lottie_to_webp", "lottie_to_webp.sh")
	if strings.TrimSpace(tool) == "" {
		return nil, "", errors.New("missing lottie_to_webp tool")
	}
	raw, _, err := s.FetchFile(ctx, fileID)
	if err != nil {
		return nil, "", err
	}
	inFile, err := os.CreateTemp("", "emoji-*.tgs")
	if err != nil {
		return nil, "", err
	}
	inPath := inFile.Name()
	_ = inFile.Close()
	defer os.Remove(inPath)
	if err := os.WriteFile(inPath, raw, 0o600); err != nil {
		return nil, "", err
	}

	outFile, err := os.CreateTemp("", "emoji-*.webp")
	if err != nil {
		return nil, "", err
	}
	outPath := outFile.Name()
	_ = outFile.Close()
	defer os.Remove(outPath)

	tctx := ctx
	if tctx == nil {
		tctx = context.Background()
	}
	if _, hasDeadline := tctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		tctx, cancel = context.WithTimeout(tctx, 12*time.Second)
		defer cancel()
	}
	cmd := exec.CommandContext(tctx, tool,
		"--width", "96",
		"--height", "96",
		"--fps", "12",
		"--quality", "70",
		"--output", outPath,
		inPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, "", fmt.Errorf("tgs->webp via %s failed: %v: %s", tool, err, strings.TrimSpace(string(out)))
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		return nil, "", err
	}
	if len(data) == 0 {
		return nil, "", errors.New("tgs converter returned empty output")
	}
	return data, "image/webp", nil
}

func findFirstTool(names ...string) (string, error) {
	for _, name := range names {
		if p, err := exec.LookPath(name); err == nil && strings.TrimSpace(p) != "" {
			return name, nil
		}
	}
	return "", fmt.Errorf("missing tool: %s", strings.Join(names, " or "))
}

func (s emojiProxyService) call(ctx context.Context, method string, payload map[string]any, out any) error {
	if !s.Enabled() {
		return errors.New("telegram token is empty")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
	}
	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/%s", strings.TrimSpace(s.Token), strings.TrimSpace(method))
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("telegram %s status=%d: %s", method, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed tgResp[json.RawMessage]
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return err
	}
	if !parsed.OK {
		if strings.TrimSpace(parsed.Description) == "" {
			parsed.Description = "telegram api error"
		}
		return errors.New(parsed.Description)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(parsed.Result, out)
}

func (s emojiProxyService) httpClient() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{Timeout: 15 * time.Second}
}
