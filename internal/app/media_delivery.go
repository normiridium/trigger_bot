package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"trigger-admin-bot/internal/mediadl"
	"trigger-admin-bot/internal/pick"
)

type sendContext struct {
	Bot     *tgbotapi.BotAPI
	ChatID  int64
	ReplyTo int
}

func (c sendContext) WithReply(replyTo int) sendContext {
	c.ReplyTo = replyTo
	return c
}

func reply(ctx sendContext, text string, preview bool) {
	rawText := strings.TrimSpace(text)
	m := tgbotapi.NewMessage(ctx.ChatID, text)
	m.DisableWebPagePreview = !preview
	if ctx.ReplyTo > 0 {
		m.ReplyToMessageID = ctx.ReplyTo
		m.AllowSendingWithoutReply = true
	}
	sent, err := ctx.Bot.Send(m)
	if err != nil {
		log.Printf("send failed: %v", err)
		reportChatFailure(ctx.Bot, ctx.ChatID, "ошибка отправки сообщения", err)
		return
	}
	if debugTriggerLogEnabled {
		log.Printf("send ok chat=%d msg=%d replyTo=%d text=%q", ctx.ChatID, sent.MessageID, ctx.ReplyTo, clipText(text, 120))
	}
	addOutgoingChatRecentMessage(ctx.ChatID, rawText)
}

func sendHTML(ctx sendContext, html string, preview bool) bool {
	html = normalizeTelegramLineBreaks(html)
	if strings.TrimSpace(html) == "" {
		if debugTriggerLogEnabled {
			log.Printf("send html skipped chat=%d replyTo=%d: empty text", ctx.ChatID, ctx.ReplyTo)
		}
		return false
	}
	m := tgbotapi.NewMessage(ctx.ChatID, html)
	m.ParseMode = "HTML"
	m.DisableWebPagePreview = !preview
	if ctx.ReplyTo > 0 {
		m.ReplyToMessageID = ctx.ReplyTo
		m.AllowSendingWithoutReply = true
	}
	if len(m.Text) > 4096 {
		m.Text = m.Text[:4096]
	}
	sent, err := ctx.Bot.Send(m)
	if err != nil {
		log.Printf("send html failed: %v", err)
		reportChatFailure(ctx.Bot, ctx.ChatID, "ошибка отправки HTML-сообщения", err)
		return false
	}
	if debugTriggerLogEnabled {
		log.Printf("send html ok chat=%d msg=%d replyTo=%d text=%q", ctx.ChatID, sent.MessageID, ctx.ReplyTo, clipText(m.Text, 120))
	}
	plain := strings.TrimSpace(htmlTagStripRe.ReplaceAllString(m.Text, " "))
	addOutgoingChatRecentMessage(ctx.ChatID, plain)
	return true
}

func sendMarkdownV2(ctx sendContext, text string, preview bool) bool {
	rawText := strings.TrimSpace(text)
	text = normalizeTelegramLineBreaks(text)
	text = escapeMarkdownV2PreservingFences(text)
	m := tgbotapi.NewMessage(ctx.ChatID, text)
	m.ParseMode = "MarkdownV2"
	m.DisableWebPagePreview = !preview
	if ctx.ReplyTo > 0 {
		m.ReplyToMessageID = ctx.ReplyTo
		m.AllowSendingWithoutReply = true
	}
	if len(m.Text) > 4096 {
		m.Text = m.Text[:4096]
	}
	sent, err := ctx.Bot.Send(m)
	if err != nil {
		log.Printf("send markdown failed: %v", err)
		reportChatFailure(ctx.Bot, ctx.ChatID, "ошибка отправки Markdown-сообщения", err)
		return false
	}
	if debugTriggerLogEnabled {
		log.Printf("send markdown ok chat=%d msg=%d replyTo=%d text=%q", ctx.ChatID, sent.MessageID, ctx.ReplyTo, clipText(m.Text, 120))
	}
	addOutgoingChatRecentMessage(ctx.ChatID, rawText)
	return true
}

func sendSticker(ctx sendContext, fileID string) bool {
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		reportChatFailure(ctx.Bot, ctx.ChatID, "ошибка отправки стикера", errors.New("empty sticker file_id"))
		return false
	}
	m := tgbotapi.NewSticker(ctx.ChatID, tgbotapi.FileID(fileID))
	if ctx.ReplyTo > 0 {
		m.ReplyToMessageID = ctx.ReplyTo
		m.AllowSendingWithoutReply = true
	}
	sent, err := ctx.Bot.Send(m)
	if err != nil {
		log.Printf("send sticker failed: %v", err)
		reportChatFailure(ctx.Bot, ctx.ChatID, "ошибка отправки стикера", err)
		return false
	}
	if debugTriggerLogEnabled {
		log.Printf("send sticker ok chat=%d msg=%d replyTo=%d file_id=%q", ctx.ChatID, sent.MessageID, ctx.ReplyTo, clipText(fileID, 120))
	}
	return true
}

type generatedImage struct {
	URL   string
	Bytes []byte
}

func sendPhoto(ctx sendContext, img generatedImage, caption string, spoiler bool) bool {
	if spoiler {
		if err := sendPhotoWithSpoilerAPI(ctx, img, caption); err != nil {
			log.Printf("send photo (spoiler) failed: %v", err)
			reportChatFailure(ctx.Bot, ctx.ChatID, "ошибка отправки картинки", err)
			return false
		}
		if debugTriggerLogEnabled {
			log.Printf("send photo (spoiler) ok chat=%d replyTo=%d", ctx.ChatID, ctx.ReplyTo)
		}
		return true
	}

	var file tgbotapi.RequestFileData
	switch {
	case strings.TrimSpace(img.URL) != "":
		file = tgbotapi.FileURL(strings.TrimSpace(img.URL))
	case len(img.Bytes) > 0:
		file = tgbotapi.FileBytes{Name: "generated.png", Bytes: img.Bytes}
	default:
		reportChatFailure(ctx.Bot, ctx.ChatID, "ошибка отправки картинки", errors.New("empty image payload"))
		return false
	}

	m := tgbotapi.NewPhoto(ctx.ChatID, file)
	if ctx.ReplyTo > 0 {
		m.ReplyToMessageID = ctx.ReplyTo
		m.AllowSendingWithoutReply = true
	}
	m.Caption = strings.TrimSpace(caption)
	sent, err := ctx.Bot.Send(m)
	if err != nil {
		log.Printf("send photo failed: %v", err)
		reportChatFailure(ctx.Bot, ctx.ChatID, "ошибка отправки картинки", err)
		return false
	}
	if debugTriggerLogEnabled {
		if strings.TrimSpace(img.URL) != "" {
			log.Printf("send photo ok chat=%d msg=%d replyTo=%d source=url", ctx.ChatID, sent.MessageID, ctx.ReplyTo)
		} else {
			log.Printf("send photo ok chat=%d msg=%d replyTo=%d source=bytes size=%d", ctx.ChatID, sent.MessageID, ctx.ReplyTo, len(img.Bytes))
		}
	}
	return true
}

func sendAudioFromURL(ctx sendContext, audioURL, performer, title string) error {
	audioURL = strings.TrimSpace(audioURL)
	if ctx.Bot == nil || ctx.ChatID == 0 || audioURL == "" {
		return errors.New("invalid audio send params")
	}
	tmpPath, err := downloadAudioToTempFile(audioURL)
	if err != nil {
		return err
	}
	defer func() {
		if rmErr := os.Remove(tmpPath); rmErr != nil && debugTriggerLogEnabled {
			log.Printf("audio temp cleanup failed path=%q err=%v", tmpPath, rmErr)
		}
	}()

	m := tgbotapi.NewAudio(ctx.ChatID, tgbotapi.FilePath(tmpPath))
	if ctx.ReplyTo > 0 {
		m.ReplyToMessageID = ctx.ReplyTo
		m.AllowSendingWithoutReply = true
	}
	m.Performer = strings.TrimSpace(performer)
	m.Title = strings.TrimSpace(title)
	if caption := buildAudioCaption(tmpPath, "", audioURL); caption != "" {
		m.Caption = caption
		m.ParseMode = "HTML"
	}
	sent, err := ctx.Bot.Send(m)
	if err != nil {
		return err
	}
	if debugTriggerLogEnabled {
		log.Printf("send audio ok chat=%d msg=%d replyTo=%d performer=%q title=%q", ctx.ChatID, sent.MessageID, ctx.ReplyTo, m.Performer, m.Title)
	}
	return nil
}

func sendAudioFromFile(ctx sendContext, filePath, performer, title string) error {
	return sendAudioFromFileWithMeta(ctx, filePath, performer, title, "", "")
}

func sendAudioFromFileWithMeta(ctx sendContext, filePath, performer, title, sourceURL, service string) error {
	filePath = strings.TrimSpace(filePath)
	if ctx.Bot == nil || ctx.ChatID == 0 || filePath == "" {
		return errors.New("invalid audio file send params")
	}
	if err := ensureTelegramUploadLimit(filePath); err != nil {
		return err
	}
	m := tgbotapi.NewAudio(ctx.ChatID, tgbotapi.FilePath(filePath))
	if ctx.ReplyTo > 0 {
		m.ReplyToMessageID = ctx.ReplyTo
		m.AllowSendingWithoutReply = true
	}
	m.Performer = strings.TrimSpace(performer)
	m.Title = strings.TrimSpace(title)
	if caption := buildAudioCaption(filePath, service, sourceURL); caption != "" {
		m.Caption = caption
		m.ParseMode = "HTML"
	}
	sent, err := ctx.Bot.Send(m)
	if err != nil {
		return err
	}
	if debugTriggerLogEnabled {
		log.Printf("send audio file ok chat=%d msg=%d replyTo=%d performer=%q title=%q service=%q", ctx.ChatID, sent.MessageID, ctx.ReplyTo, m.Performer, m.Title, service)
	}
	return nil
}

func sendVideoFromFile(ctx sendContext, filePath, caption string) error {
	filePath = strings.TrimSpace(filePath)
	if ctx.Bot == nil || ctx.ChatID == 0 || filePath == "" {
		return errors.New("invalid video file send params")
	}
	if err := ensureTelegramUploadLimit(filePath); err != nil {
		return err
	}
	m := tgbotapi.NewVideo(ctx.ChatID, tgbotapi.FilePath(filePath))
	if ctx.ReplyTo > 0 {
		m.ReplyToMessageID = ctx.ReplyTo
		m.AllowSendingWithoutReply = true
	}
	m.SupportsStreaming = true
	caption = strings.TrimSpace(caption)
	if caption != "" {
		m.Caption = clipText(caption, 1024)
		m.ParseMode = "HTML"
	}
	sent, err := ctx.Bot.Send(m)
	if err != nil {
		return err
	}
	if debugTriggerLogEnabled {
		log.Printf("send video file ok chat=%d msg=%d replyTo=%d caption=%q", ctx.ChatID, sent.MessageID, ctx.ReplyTo, clipText(m.Caption, 120))
	}
	return nil
}

func sendPhotoFromFile(ctx sendContext, filePath, caption string) error {
	filePath = strings.TrimSpace(filePath)
	if ctx.Bot == nil || ctx.ChatID == 0 || filePath == "" {
		return errors.New("invalid photo file send params")
	}
	if err := ensureTelegramUploadLimit(filePath); err != nil {
		return err
	}
	m := tgbotapi.NewPhoto(ctx.ChatID, tgbotapi.FilePath(filePath))
	if ctx.ReplyTo > 0 {
		m.ReplyToMessageID = ctx.ReplyTo
		m.AllowSendingWithoutReply = true
	}
	caption = strings.TrimSpace(caption)
	if caption != "" {
		m.Caption = clipText(caption, 1024)
		m.ParseMode = "HTML"
	}
	sent, err := ctx.Bot.Send(m)
	if err != nil {
		return err
	}
	if debugTriggerLogEnabled {
		log.Printf("send photo file ok chat=%d msg=%d replyTo=%d caption=%q", ctx.ChatID, sent.MessageID, ctx.ReplyTo, clipText(m.Caption, 120))
	}
	return nil
}

func buildMediaAudioTitle(title, sourceURL, service string) string {
	title = strings.TrimSpace(title)
	_ = sourceURL
	_ = service
	return clipText(title, 120)
}

func mediaServiceEmoji(service, mode string) string {
	service = strings.ToLower(strings.TrimSpace(service))
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == mediadl.ModeVideo {
		switch service {
		case "instagram":
			return `<tg-emoji emoji-id="5463238270693416950">📹</tg-emoji>`
		case "tiktok":
			return `<tg-emoji emoji-id="5465416081105493315">📹</tg-emoji>`
		case "x":
			return `<tg-emoji emoji-id="5463206079913533096">📹</tg-emoji>`
		case "soundcloud":
			return `<tg-emoji emoji-id="5359614685664523140">🎉</tg-emoji>`
		default:
			return `<tg-emoji emoji-id="5463206079913533096">📹</tg-emoji>`
		}
	}
	switch service {
	case "youtube":
		return `<tg-emoji emoji-id="5463206079913533096">📹</tg-emoji>`
	case "instagram":
		return `<tg-emoji emoji-id="5463238270693416950">📹</tg-emoji>`
	case "tiktok":
		return `<tg-emoji emoji-id="5465416081105493315">📹</tg-emoji>`
	case "x":
		return `<tg-emoji emoji-id="5463206079913533096">📹</tg-emoji>`
	default:
		return `<tg-emoji emoji-id="5359614685664523140">🎉</tg-emoji>`
	}
}

func buildMediaVideoCaption(path, title, sourceURL, service string) string {
	title = strings.TrimSpace(title)
	sourceURL = strings.TrimSpace(sourceURL)
	sizeMB := 0.0
	if st, err := os.Stat(path); err == nil && st != nil {
		sizeMB = float64(st.Size()) / 1_000_000.0
	}
	dur := ""
	if d, err := probeMediaDurationSec(path); err == nil && d > 0 {
		dur = pick.FormatDuration(d)
	}
	emoji := mediaServiceEmoji(service, mediadl.ModeVideo)
	stats := ""
	if dur != "" && sizeMB > 0 {
		stats = fmt.Sprintf("%s %s | %.2fMB", emoji, dur, sizeMB)
	} else if sizeMB > 0 {
		stats = fmt.Sprintf("%s %.2fMB", emoji, sizeMB)
	} else {
		stats = emoji
	}
	head := strings.TrimSpace(title)
	if sourceURL != "" {
		if head == "" {
			head = buildSourceLinkHTML(sourceURL, "ссылка")
		} else {
			head = buildSourceLinkHTML(sourceURL, head)
		}
	} else {
		head = html.EscapeString(head)
	}
	if strings.TrimSpace(head) == "" {
		return stats
	}
	return head + "\n" + stats
}

func buildMediaPhotoCaption(path, title, sourceURL, service string) string {
	title = strings.TrimSpace(title)
	sourceURL = strings.TrimSpace(sourceURL)
	sizeMB := 0.0
	if st, err := os.Stat(path); err == nil && st != nil {
		sizeMB = float64(st.Size()) / 1_000_000.0
	}
	emoji := mediaServiceEmoji(service, mediadl.ModeVideo)
	stats := emoji
	if sizeMB > 0 {
		stats = fmt.Sprintf("%s %.2fMB", emoji, sizeMB)
	}
	head := strings.TrimSpace(title)
	if sourceURL != "" {
		if head == "" {
			head = buildSourceLinkHTML(sourceURL, "ссылка")
		} else {
			head = buildSourceLinkHTML(sourceURL, head)
		}
	} else {
		head = html.EscapeString(head)
	}
	if strings.TrimSpace(head) == "" {
		return stats
	}
	return head + "\n" + stats
}

func buildSourceLinkHTML(rawURL, label string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	label = strings.TrimSpace(label)
	if label == "" {
		label = "ссылка"
	}
	return `<a href="` + html.EscapeString(rawURL) + `">` + html.EscapeString(label) + `</a>`
}

func ensureTelegramUploadLimit(path string) error {
	maxMB := envInt("TELEGRAM_UPLOAD_MAX_MB", 50)
	if maxMB <= 0 {
		return nil
	}
	st, err := os.Stat(strings.TrimSpace(path))
	if err != nil {
		return err
	}
	maxBytes := int64(maxMB) * 1024 * 1024
	if st.Size() <= maxBytes {
		return nil
	}
	return fmt.Errorf("%w: %d bytes > %d MB limit", errTelegramUploadTooLarge, st.Size(), maxMB)
}

func sendPhotoWithSpoilerAPI(ctx sendContext, img generatedImage, caption string) error {
	if ctx.Bot == nil || strings.TrimSpace(ctx.Bot.Token) == "" {
		return errors.New("bot token is empty")
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("chat_id", strconv.FormatInt(ctx.ChatID, 10))
	if ctx.ReplyTo > 0 {
		_ = writer.WriteField("reply_to_message_id", strconv.Itoa(ctx.ReplyTo))
		_ = writer.WriteField("allow_sending_without_reply", "true")
	}
	if strings.TrimSpace(caption) != "" {
		_ = writer.WriteField("caption", strings.TrimSpace(caption))
	}
	_ = writer.WriteField("has_spoiler", "true")

	switch {
	case strings.TrimSpace(img.URL) != "":
		_ = writer.WriteField("photo", strings.TrimSpace(img.URL))
	case len(img.Bytes) > 0:
		part, err := writer.CreateFormFile("photo", "generated.png")
		if err != nil {
			_ = writer.Close()
			return err
		}
		if _, err := part.Write(img.Bytes); err != nil {
			_ = writer.Close()
			return err
		}
	default:
		_ = writer.Close()
		return errors.New("empty image payload")
	}

	if err := writer.Close(); err != nil {
		return err
	}

	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/sendPhoto", strings.TrimSpace(ctx.Bot.Token))
	req, err := http.NewRequest("POST", endpoint, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{Timeout: 35 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("telegram status=%d body=%s", resp.StatusCode, clipText(string(respBody), 600))
	}
	var tg struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(respBody, &tg); err != nil {
		return nil
	}
	if !tg.OK {
		return fmt.Errorf("telegram response not ok: %s", clipText(string(respBody), 600))
	}
	return nil
}
