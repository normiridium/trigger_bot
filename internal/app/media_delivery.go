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
	"regexp"
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

func sanitizeTelegramText(s string) string {
	return strings.ToValidUTF8(s, "")
}

type htmlOpenTag struct {
	name string
	open string
}

var telegramHTMLTokenRe = regexp.MustCompile(`(?s)<[^>]+>|[^<]+`)

func splitTelegramHTMLMessage(input string, maxRunes int) []string {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil
	}
	if maxRunes <= 0 {
		return []string{input}
	}
	if len([]rune(input)) <= maxRunes {
		return []string{input}
	}

	tokens := telegramHTMLTokenRe.FindAllString(input, -1)
	if len(tokens) == 0 {
		return []string{input}
	}

	stack := make([]htmlOpenTag, 0, 8)
	out := make([]string, 0, 2)
	var b strings.Builder
	curLen := 0
	hasPayload := false

	openPrefix := func() string {
		if len(stack) == 0 {
			return ""
		}
		var p strings.Builder
		for _, t := range stack {
			p.WriteString(t.open)
		}
		return p.String()
	}
	closeSuffix := func(st []htmlOpenTag) string {
		if len(st) == 0 {
			return ""
		}
		var p strings.Builder
		for i := len(st) - 1; i >= 0; i-- {
			p.WriteString("</")
			p.WriteString(st[i].name)
			p.WriteString(">")
		}
		return p.String()
	}
	flush := func() bool {
		if !hasPayload {
			return false
		}
		chunk := b.String() + closeSuffix(stack)
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			return false
		}
		out = append(out, chunk)
		b.Reset()
		curLen = 0
		hasPayload = false
		prefix := openPrefix()
		if prefix != "" {
			b.WriteString(prefix)
			curLen += len([]rune(prefix))
		}
		return true
	}

	prefix := openPrefix()
	if prefix != "" {
		b.WriteString(prefix)
		curLen += len([]rune(prefix))
	}

	for _, tok := range tokens {
		name, isOpen, isClose, isSelf := parseTelegramHTMLTag(tok)
		if isOpen || isClose {
			newStack := make([]htmlOpenTag, len(stack))
			copy(newStack, stack)
			if isOpen && !isSelf {
				newStack = append(newStack, htmlOpenTag{name: name, open: tok})
			} else if isClose {
				for i := len(newStack) - 1; i >= 0; i-- {
					if newStack[i].name == name {
						newStack = newStack[:i]
						break
					}
				}
			}
			tokenLen := len([]rune(tok))
			if curLen+tokenLen+len([]rune(closeSuffix(newStack))) > maxRunes {
				if flush() {
					// retry this token on fresh chunk with reopened tags
					name, isOpen, isClose, isSelf = parseTelegramHTMLTag(tok)
					newStack = make([]htmlOpenTag, len(stack))
					copy(newStack, stack)
					if isOpen && !isSelf {
						newStack = append(newStack, htmlOpenTag{name: name, open: tok})
					} else if isClose {
						for i := len(newStack) - 1; i >= 0; i-- {
							if newStack[i].name == name {
								newStack = newStack[:i]
								break
							}
						}
					}
				}
			}
			b.WriteString(tok)
			curLen += tokenLen
			hasPayload = true
			stack = newStack
			continue
		}

		rest := tok
		for rest != "" {
			suffixLen := len([]rune(closeSuffix(stack)))
			avail := maxRunes - curLen - suffixLen
			if avail <= 0 {
				if !flush() {
					// Degenerate case: cannot flush because payload is empty.
					// Fall back to hard split.
					r := []rune(rest)
					cut := maxRunes - curLen
					if cut <= 0 {
						cut = 1
					}
					if cut > len(r) {
						cut = len(r)
					}
					part := string(r[:cut])
					b.WriteString(part)
					curLen += len([]rune(part))
					hasPayload = true
					rest = string(r[cut:])
					_ = flush()
					continue
				}
				continue
			}
			r := []rune(rest)
			if len(r) <= avail {
				b.WriteString(rest)
				curLen += len(r)
				hasPayload = true
				rest = ""
				continue
			}
			part := string(r[:avail])
			b.WriteString(part)
			curLen += len([]rune(part))
			hasPayload = true
			rest = string(r[avail:])
			_ = flush()
		}
	}
	if hasPayload {
		chunk := strings.TrimSpace(b.String() + closeSuffix(stack))
		if chunk != "" {
			out = append(out, chunk)
		}
	}
	if len(out) == 0 {
		return []string{input}
	}
	return out
}

func parseTelegramHTMLTag(tok string) (name string, isOpen bool, isClose bool, isSelf bool) {
	t := strings.TrimSpace(tok)
	if len(t) < 3 || t[0] != '<' || t[len(t)-1] != '>' {
		return "", false, false, false
	}
	inner := strings.TrimSpace(t[1 : len(t)-1])
	if inner == "" {
		return "", false, false, false
	}
	if strings.HasPrefix(inner, "/") {
		rest := strings.TrimSpace(inner[1:])
		if rest == "" {
			return "", false, false, false
		}
		parts := strings.Fields(rest)
		if len(parts) == 0 {
			return "", false, false, false
		}
		return strings.ToLower(parts[0]), false, true, false
	}
	if strings.HasSuffix(inner, "/") {
		isSelf = true
		inner = strings.TrimSpace(strings.TrimSuffix(inner, "/"))
	}
	parts := strings.Fields(inner)
	if len(parts) == 0 {
		return "", false, false, false
	}
	name = strings.ToLower(parts[0])
	switch name {
	case "b", "strong", "i", "em", "u", "ins", "s", "strike", "del", "code", "pre", "blockquote", "a", "tg-spoiler", "tg-emoji":
		return name, true, false, isSelf
	default:
		return "", false, false, false
	}
}

func reply(ctx sendContext, text string, preview bool) {
	rawText := strings.TrimSpace(text)
	text = sanitizeTelegramText(text)
	// text = truncateRunes(text, 4096) // временно отключено
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
	html = canonicalizeTGEmojiTags(html)
	html = normalizeTelegramLineBreaks(html)
	html = sanitizeTelegramText(html)
	if strings.TrimSpace(html) == "" {
		if debugTriggerLogEnabled {
			log.Printf("send html skipped chat=%d replyTo=%d: empty text", ctx.ChatID, ctx.ReplyTo)
		}
		return false
	}
	parts := splitTelegramHTMLMessage(html, 4096)
	if len(parts) == 0 {
		return false
	}
	replyTo := ctx.ReplyTo
	for i, part := range parts {
		m := tgbotapi.NewMessage(ctx.ChatID, part)
		m.ParseMode = "HTML"
		m.DisableWebPagePreview = !preview
		if i == 0 && replyTo > 0 {
			m.ReplyToMessageID = replyTo
			m.AllowSendingWithoutReply = true
		}
		sent, err := ctx.Bot.Send(m)
		if err != nil {
			// Fallback for stale/invalid custom emoji IDs: keep text, drop tg-emoji tags.
			if strings.Contains(strings.ToLower(err.Error()), "invalid custom emoji identifier") {
				fallback := replaceTGEmojiTagsWithFallback(part)
				m2 := tgbotapi.NewMessage(ctx.ChatID, fallback)
				m2.ParseMode = "HTML"
				m2.DisableWebPagePreview = !preview
				if i == 0 && replyTo > 0 {
					m2.ReplyToMessageID = replyTo
					m2.AllowSendingWithoutReply = true
				}
				sent, err = ctx.Bot.Send(m2)
				if err == nil {
					part = fallback
				}
			}
		}
		if err != nil {
			log.Printf("send html failed part=%d/%d: %v", i+1, len(parts), err)
			reportChatFailure(ctx.Bot, ctx.ChatID, "ошибка отправки HTML-сообщения", err)
			return false
		}
		if debugTriggerLogEnabled {
			log.Printf("send html ok chat=%d msg=%d replyTo=%d part=%d/%d text=%q",
				ctx.ChatID, sent.MessageID, replyTo, i+1, len(parts), clipText(part, 120))
		}
		plain := strings.TrimSpace(htmlTagStripRe.ReplaceAllString(part, " "))
		addOutgoingChatRecentMessage(ctx.ChatID, plain)
	}
	return true
}

func sendMarkdownV2(ctx sendContext, text string, preview bool) bool {
	rawText := strings.TrimSpace(text)
	text = normalizeTelegramLineBreaks(text)
	text = sanitizeTelegramText(text)
	text = escapeMarkdownV2PreservingFences(text)
	m := tgbotapi.NewMessage(ctx.ChatID, text)
	m.ParseMode = "MarkdownV2"
	m.DisableWebPagePreview = !preview
	if ctx.ReplyTo > 0 {
		m.ReplyToMessageID = ctx.ReplyTo
		m.AllowSendingWithoutReply = true
	}
	// m.Text = truncateRunes(m.Text, 4096) // временно отключено
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

func sendGIF(ctx sendContext, fileID, caption string) bool {
	fileID = strings.TrimSpace(fileID)
	caption = strings.TrimSpace(caption)
	if fileID == "" {
		reportChatFailure(ctx.Bot, ctx.ChatID, "ошибка отправки GIF", errors.New("empty gif file_id"))
		return false
	}
	m := tgbotapi.NewAnimation(ctx.ChatID, tgbotapi.FileID(fileID))
	if ctx.ReplyTo > 0 {
		m.ReplyToMessageID = ctx.ReplyTo
		m.AllowSendingWithoutReply = true
	}
	m.Caption = caption
	sent, err := ctx.Bot.Send(m)
	if err != nil {
		log.Printf("send gif failed: %v", err)
		reportChatFailure(ctx.Bot, ctx.ChatID, "ошибка отправки GIF", err)
		return false
	}
	if debugTriggerLogEnabled {
		log.Printf("send gif ok chat=%d msg=%d replyTo=%d file_id=%q", ctx.ChatID, sent.MessageID, ctx.ReplyTo, clipText(fileID, 120))
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

func sendDocumentFromFile(ctx sendContext, filePath, caption string) error {
	filePath = strings.TrimSpace(filePath)
	if ctx.Bot == nil || ctx.ChatID == 0 || filePath == "" {
		return errors.New("invalid document file send params")
	}
	if err := ensureTelegramUploadLimit(filePath); err != nil {
		return err
	}
	m := tgbotapi.NewDocument(ctx.ChatID, tgbotapi.FilePath(filePath))
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
		log.Printf("send document file ok chat=%d msg=%d replyTo=%d file=%q", ctx.ChatID, sent.MessageID, ctx.ReplyTo, clipText(filePath, 160))
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
		case "coub":
			return `<tg-emoji emoji-id="5197233100463039550">🤘</tg-emoji>`
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
	case "coub":
		return `<tg-emoji emoji-id="5197233100463039550">🤘</tg-emoji>`
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
