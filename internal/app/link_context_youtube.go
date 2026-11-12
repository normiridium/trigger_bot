package app

// Контекст из ссылки на YouTube: вместо HTML-страницы (там нет текста, только
// скрипты) забираем субтитры через yt-dlp и отдаём их модели как текст.

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"trigger-admin-bot/internal/bottmp"
)

// Порядок предпочтения языков субтитров, если скачано несколько дорожек.
var youTubeSubLangOrder = []string{"ru", "ru-orig", "en", "en-orig"}

// Попытки скачать субтитры: YouTube отвечает 429 на часть дорожек, поэтому
// первые две пробуют ru/en, последняя берёт любые доступные языки — модели
// всё равно, на каком языке текст.
var youTubeSubAttempts = []struct {
	Delay time.Duration
	Langs string
}{
	{0, "ru.*,en.*"},
	{3 * time.Second, "ru.*,en.*"},
	{8 * time.Second, "all"},
}

// Разделитель полей в выводе yt-dlp --print (unit separator, в тексте не встречается).
const youTubePrintSep = "\x1f"

var youTubeSubTagRe = regexp.MustCompile(`<[^>]+>`)
var youTubeVTTTimestampRe = regexp.MustCompile(`^\d{1,2}:\d{2}:\d{2}[.,]\d{3}\s*-->`)
var youTubeSubSpaceRe = regexp.MustCompile(`[ \t]+`)

func isYouTubeContextURL(u *url.URL) bool {
	if u == nil {
		return false
	}
	host := strings.ToLower(strings.TrimPrefix(u.Hostname(), "www."))
	switch host {
	case "youtube.com", "m.youtube.com", "music.youtube.com", "youtu.be", "youtube-nocookie.com":
		return true
	}
	return false
}

func youTubeBin() string {
	bin := strings.TrimSpace(os.Getenv("YTDLP_BIN"))
	if bin == "" {
		return "yt-dlp"
	}
	return bin
}

// youTubeRun запускает yt-dlp с общими флагами.
func youTubeRun(ctx context.Context, timeout time.Duration, args []string) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := exec.CommandContext(runCtx, youTubeBin(), args...).CombinedOutput()
	return string(output), err
}

// youTubeMetadata берёт заголовок и описание отдельным вызовом: он не трогает
// субтитры, поэтому не попадает под 429.
func youTubeMetadata(ctx context.Context, rawURL string) (string, string, error) {
	args := []string{
		"--skip-download",
		"--no-playlist",
		"--no-warnings",
		"--no-progress",
		"--socket-timeout", "20",
		"--print", "%(title)s" + youTubePrintSep + "%(description)s",
		rawURL,
	}
	output, err := youTubeRun(ctx, 30*time.Second, args)
	if err != nil && strings.TrimSpace(output) == "" {
		return "", "", fmt.Errorf("youtube metadata: %w", err)
	}
	title, description := parseYouTubePrintOutput(output)
	return title, description, nil
}

// parseYouTubePrintOutput разбирает вывод --print "%(title)s<US>%(description)s".
func parseYouTubePrintOutput(output string) (string, string) {
	output = strings.TrimSpace(output)
	if output == "" {
		return "", ""
	}
	// yt-dlp печатает по строке на видео; берём первую.
	line := output
	if idx := strings.IndexByte(output, '\n'); idx >= 0 {
		line = output[:idx]
	}
	parts := strings.SplitN(line, youTubePrintSep, 2)
	title := strings.TrimSpace(parts[0])
	description := ""
	if len(parts) == 2 {
		description = strings.TrimSpace(parts[1])
	}
	return title, description
}

// youTubeTranscript скачивает субтитры (включая авто-сгенерированные) и
// превращает их в плоский текст. Пустая строка — субтитров нет.
func youTubeTranscript(ctx context.Context, rawURL, dir, langs string) (string, error) {
	args := []string{
		"--skip-download",
		// Без этого флага 429 на одной языковой дорожке обрывает всю загрузку.
		"--ignore-errors",
		"--write-auto-subs",
		"--write-subs",
		"--sub-langs", langs,
		"--sub-format", "vtt",
		"--no-playlist",
		"--no-warnings",
		"--no-progress",
		"--socket-timeout", "20",
		"--retries", "2",
		"-o", filepath.Join(dir, "%(id)s.%(ext)s"),
		rawURL,
	}
	output, err := youTubeRun(ctx, 45*time.Second, args)
	transcript := readYouTubeSubtitle(dir)
	if transcript != "" {
		return transcript, nil
	}
	if err != nil {
		return "", fmt.Errorf("youtube subtitles: %w (%s)", err, clipLogText(string(output), 200))
	}
	return "", errors.New("youtube: субтитров нет")
}

// youTubeContextParts достаёт заголовок, описание и текст субтитров.
func youTubeContextParts(ctx context.Context, rawURL string, maxChars int) (string, string, string, error) {
	title, description, metaErr := youTubeMetadata(ctx, rawURL)

	dir, err := bottmp.MkdirTemp("yt-context-*")
	if err != nil {
		return "", "", "", err
	}
	defer os.RemoveAll(dir)

	var transcript string
	var subErr error
	for attempt, step := range youTubeSubAttempts {
		if attempt > 0 {
			select {
			case <-time.After(step.Delay):
			case <-ctx.Done():
				return "", "", "", ctx.Err()
			}
		}
		transcript, subErr = youTubeTranscript(ctx, rawURL, dir, step.Langs)
		if transcript != "" {
			break
		}
	}

	if transcript == "" && description == "" {
		reason := subErr
		if reason == nil {
			reason = metaErr
		}
		if reason == nil {
			reason = errors.New("нет ни субтитров, ни описания")
		}
		return "", "", "", reason
	}

	title = strings.TrimSpace(title)
	if title == "" {
		title = rawURL
	}

	var text, extra string
	if transcript != "" {
		text = clipRunes(transcript, maxChars)
		extra = clipRunes(description, maxChars/2)
	} else {
		// Субтитров нет — рассуждаем по описанию под видео.
		text = clipRunes(description, maxChars)
	}
	return title, text, extra, nil
}

// readYouTubeSubtitle выбирает файл субтитров по приоритету языка.
func readYouTubeSubtitle(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var files []string
	for _, entry := range entries {
		if !strings.HasSuffix(strings.ToLower(entry.Name()), ".vtt") {
			continue
		}
		files = append(files, entry.Name())
	}
	if len(files) == 0 {
		return ""
	}
	rank := func(name string) int {
		lower := strings.ToLower(name)
		for i, lang := range youTubeSubLangOrder {
			if strings.Contains(lower, "."+lang+".") {
				return i
			}
		}
		return len(youTubeSubLangOrder)
	}
	sort.SliceStable(files, func(i, j int) bool { return rank(files[i]) < rank(files[j]) })
	raw, err := os.ReadFile(filepath.Join(dir, files[0]))
	if err != nil {
		return ""
	}
	return vttToPlainText(string(raw))
}

// vttToPlainText превращает WebVTT в обычный текст: без служебных строк,
// тайм-кодов и повторов, которые появляются в авто-субтитрах.
func vttToPlainText(raw string) string {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	kept := make([]string, 0, len(lines))
	prev := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "webvtt") || strings.HasPrefix(lower, "kind:") ||
			strings.HasPrefix(lower, "language:") || strings.HasPrefix(lower, "note") {
			continue
		}
		if youTubeVTTTimestampRe.MatchString(trimmed) || strings.Contains(trimmed, "-->") {
			continue
		}
		if _, err := strconv.Atoi(trimmed); err == nil {
			continue // номер реплики
		}
		noTags := youTubeSubTagRe.ReplaceAllString(trimmed, "")
		cleaned := strings.TrimSpace(html.UnescapeString(noTags))
		if cleaned == "" {
			continue
		}
		normalized := strings.ToLower(youTubeSubSpaceRe.ReplaceAllString(cleaned, " "))
		if normalized == prev {
			continue
		}
		// Авто-субтитры «досказывают» строку целиком: предыдущая содержит текущую.
		if prev != "" && strings.HasPrefix(prev, normalized) {
			continue
		}
		prev = normalized
		kept = append(kept, cleaned)
	}
	return strings.TrimSpace(strings.Join(kept, " "))
}
