package app

// Прогресс-бар в виде HTML-виджета (HTMLBubbles, Bot API 10.4, beta).
//
// Кадры бара — те же, что у текстового прогресса: mediaProgressFrames()
// собирает строки из кастомных эмодзи (<tg-emoji emoji-id="…">), здесь они
// разбираются обратно на последовательности id, а сами картинки лежат в
// progress_assets/*.webp (первые кадры видеостикеров) и инлайнятся в HTML
// как data: — внешние запросы в песочнице запрещены.
//
// Бюджет: HTML ≤ 32 КБ и ≤ 4096 байт zlib -9. Шесть эмодзи в 48px дают
// ~2.5 КБ сжатыми, запас есть.

import (
	"bytes"
	"compress/zlib"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

//go:embed progress_assets/*.webp
var progressEmojiFS embed.FS

// progressEmojiTokenRe вырезает id и глиф из разметки кастомного эмодзи.
var progressEmojiTokenRe = regexp.MustCompile(`<tg-emoji emoji-id="(\d+)">([^<]+)</tg-emoji>`)

// widgetSizeLimit — предел сжатого HTML, который принимает сервер.
const widgetSizeLimit = 4096

// progressWidgetTemplate собирает виджет: состояние приходит в chatData как
// {"f": <кадр>, "s": "<этап>"}, картинки вшиты в HTML.
const progressWidgetTemplate = `<!doctype html>
<html><head><meta charset="utf-8">
<style>
html,body{margin:0;padding:0;background:transparent}
.w{padding:10px 12px;font:15px/1.3 -apple-system,system-ui,"Segoe UI",Roboto,sans-serif}
.bar{display:flex;align-items:center;line-height:0}
.bar img{width:22px;height:22px;margin-right:2px}
.stage{margin-top:7px;font-size:13px;font-style:italic;color:var(--tg-theme-hint-color,#707579)}
</style></head>
<body>
<div class="w"><div class="bar" id="bar"></div><div class="stage" id="st"></div></div>
<script>
var IMG=%s,FRAMES=%s;
(function(){
  var W=window.TelegramWidget||{},bar=document.getElementById('bar'),st=document.getElementById('st'),slots=[];
  for(var i=0;i<6;i++){var el=document.createElement('img');el.alt='';bar.appendChild(el);slots.push(el);}
  function state(){try{return JSON.parse(W.chatData||'{}')||{};}catch(e){return {};}}
  function render(){
    var s=state(),f=Number(s.f);if(!isFinite(f))f=0;
    f=Math.max(0,Math.min(FRAMES.length-1,f));
    var fr=FRAMES[f];
    for(var i=0;i<slots.length;i++)slots[i].src=IMG[fr[i]];
    var t=s.s||'';st.textContent=t;st.style.display=t?'block':'none';
  }
  if(typeof W.onEvent==='function')W.onEvent('chatDataChanged',render);
  render();
  if(typeof W.ready==='function')W.ready();
})();
</script>
</body></html>`

// widgetBlockedChats — чаты, где сервер отказал (INPUT_MEDIA_WIDGET_NOT_ALLOWED).
// Сейчас beta принимает виджеты только в личке, в супергруппах отвечает отказом;
// повторять вызов на каждой задаче бессмысленно, поэтому запоминаем чат.
var widgetBlockedChats sync.Map

var (
	progressWidgetOnce sync.Once
	progressWidgetHTML string
	progressWidgetOK   bool
	progressWidgetCli  *tgbotapi.BotAPI
)

// progressWidgetEnabled — переключатель (beta-функция, по умолчанию выкл).
func progressWidgetEnabled() bool {
	return envBool("MEDIA_PROGRESS_WIDGET", false)
}

// progressWidgetAPI — эндпоинт beta, где живут виджеты.
func progressWidgetAPI() string {
	endpoint := strings.TrimSpace(envOr("MEDIA_PROGRESS_WIDGET_API", "https://api.telegram.org/beta"))
	if !strings.Contains(endpoint, "%s") {
		endpoint = strings.TrimRight(endpoint, "/") + "/bot%s/%s"
	}
	return endpoint
}

// buildProgressWidget собирает HTML виджета из кадров прогресса.
// Возвращает false, если кадры не разобрались или не хватает картинок —
// тогда остаётся текстовый прогресс.
func buildProgressWidget() (string, bool) {
	frames := mediaProgressFrames()
	if len(frames) == 0 {
		return "", false
	}

	images := []string{}
	index := map[string]int{}
	order := []string{}
	sequences := make([][]int, 0, len(frames))
	for _, frame := range frames {
		tokens := progressEmojiTokenRe.FindAllStringSubmatch(frame, -1)
		if len(tokens) == 0 {
			return "", false
		}
		seq := make([]int, 0, len(tokens))
		for _, token := range tokens {
			emojiID, glyph := token[1], token[2]
			pos, seen := index[emojiID]
			if !seen {
				data, err := progressEmojiFS.ReadFile("progress_assets/" + emojiID + ".webp")
				if err != nil || len(data) == 0 {
					log.Printf("progress widget: нет картинки для эмодзи %s (%s)", emojiID, glyph)
					return "", false
				}
				pos = len(images)
				index[emojiID] = pos
				order = append(order, emojiID)
				images = append(images, "data:image/webp;base64,"+base64.StdEncoding.EncodeToString(data))
			}
			seq = append(seq, pos)
		}
		sequences = append(sequences, seq)
	}

	imagesJSON, err := json.Marshal(images)
	if err != nil {
		return "", false
	}
	framesJSON, err := json.Marshal(sequences)
	if err != nil {
		return "", false
	}
	html := fmt.Sprintf(progressWidgetTemplate, imagesJSON, framesJSON)

	var buf bytes.Buffer
	zw, err := zlib.NewWriterLevel(&buf, 9)
	if err != nil {
		return "", false
	}
	if _, err := zw.Write([]byte(html)); err != nil {
		return "", false
	}
	if err := zw.Close(); err != nil {
		return "", false
	}
	compressed := buf.Len()
	if compressed > widgetSizeLimit {
		log.Printf("progress widget: HTML сжат в %d байт при лимите %d — откат на текст", compressed, widgetSizeLimit)
		return "", false
	}
	log.Printf("progress widget: готов, %d эмодзи, html=%d байт, сжато=%d байт", len(order), len(html), compressed)
	return html, true
}

// progressWidgetReady лениво собирает HTML и клиент beta.
func progressWidgetReady() (string, *tgbotapi.BotAPI, bool) {
	progressWidgetOnce.Do(func() {
		if !progressWidgetEnabled() {
			return
		}
		html, ok := buildProgressWidget()
		if !ok {
			return
		}
		token := strings.TrimSpace(envOr("TELEGRAM_BOT_TOKEN", ""))
		if token == "" {
			log.Printf("progress widget: нет TELEGRAM_BOT_TOKEN")
			return
		}
		client, err := tgbotapi.NewBotAPIWithAPIEndpoint(token, progressWidgetAPI())
		if err != nil {
			log.Printf("progress widget: beta-клиент не поднялся: %v", err)
			return
		}
		progressWidgetHTML, progressWidgetOK, progressWidgetCli = html, true, client
	})
	if !progressWidgetOK {
		return "", nil, false
	}
	return progressWidgetHTML, progressWidgetCli, true
}

// widgetProgressHandle — прогресс в виджете: состояние пишет только бот,
// поэтому шаги — это editMessageWidgetData с новым chat_data.
type widgetProgressHandle struct {
	client    *tgbotapi.BotAPI
	chatID    int64
	messageID int

	mu       sync.Mutex
	frame    int
	stage    string
	sent     string
	lastEdit time.Time
}

type widgetProgressState struct {
	Frame int    `json:"f"`
	Stage string `json:"s"`
}

// startWidgetProgress отправляет виджет-прогресс; false — использовать текст.
func startWidgetProgress(task mediaDownloadTask) (*widgetProgressHandle, func()) {
	noop := func() {}
	html, client, ok := progressWidgetReady()
	if !ok || client == nil {
		return nil, noop
	}
	chatID := task.SendCtx.ChatID
	if _, blocked := widgetBlockedChats.Load(chatID); blocked {
		return nil, noop
	}
	if chatID == 0 || html == "" {
		return nil, noop
	}

	initial, err := json.Marshal(widgetProgressState{Frame: 0, Stage: "Подготовка"})
	if err != nil {
		return nil, noop
	}
	chatData := string(initial)
	cfg := tgbotapi.NewSendWidget(chatID, html)
	cfg.ChatData = &chatData
	// подпись видят старые клиенты (ниже 13.2) вместо заглушки "Widget"
	cfg.Caption = "Обработка медиа…"
	if replyTo := task.SendCtx.ReplyTo; replyTo > 0 {
		cfg.ReplyParameters = &tgbotapi.ReplyParameters{
			MessageID:                replyTo,
			AllowSendingWithoutReply: true,
		}
	}

	sent, err := client.Send(cfg)
	if err != nil {
		if strings.Contains(err.Error(), "INPUT_MEDIA_WIDGET_NOT_ALLOWED") {
			widgetBlockedChats.Store(chatID, true)
			log.Printf("progress widget: чат %d виджеты не принимает, дальше только текст", chatID)
			return nil, noop
		}
		log.Printf("progress widget: sendWidget не удался chat=%d, остаётся текст: %v", chatID, err)
		return nil, noop
	}

	handle := &widgetProgressHandle{
		client:    client,
		chatID:    sent.Chat.ID,
		messageID: sent.MessageID,
		stage:     "Подготовка",
		sent:      chatData,
		lastEdit:  time.Now(),
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_, _ = client.Request(tgbotapi.DeleteMessageConfig{ChatID: handle.chatID, MessageID: handle.messageID})
		})
	}
	return handle, stop
}

// SetFrame переключает кадр бара.
func (h *widgetProgressHandle) SetFrame(frame int) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.frame = frame
	h.mu.Unlock()
	h.push()
}

// SetStage меняет подпись под баром.
func (h *widgetProgressHandle) SetStage(stage string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.stage = strings.TrimSpace(stage)
	h.mu.Unlock()
	h.push()
}

// push отправляет состояние, не чаще MEDIA_PROGRESS_MIN_EDIT_INTERVAL_MS.
func (h *widgetProgressHandle) push() {
	h.mu.Lock()
	payload, err := json.Marshal(widgetProgressState{Frame: h.frame, Stage: h.stage})
	if err != nil {
		h.mu.Unlock()
		return
	}
	chatData := string(payload)
	if chatData == h.sent {
		h.mu.Unlock()
		return
	}
	minInterval := time.Duration(envInt("MEDIA_PROGRESS_MIN_EDIT_INTERVAL_MS", 1000)) * time.Millisecond
	if minInterval > 0 && !h.lastEdit.IsZero() && time.Since(h.lastEdit) < minInterval {
		h.mu.Unlock()
		return
	}
	h.sent = chatData
	h.lastEdit = time.Now()
	client, chatID, messageID := h.client, h.chatID, h.messageID
	h.mu.Unlock()

	if client == nil || chatID == 0 || messageID == 0 {
		return
	}
	if _, err := client.Request(tgbotapi.NewEditMessageWidgetData(chatID, messageID, chatData)); err != nil {
		if debugTriggerLogEnabled {
			log.Printf("progress widget edit failed chat=%d msg=%d err=%v", chatID, messageID, err)
		}
	}
}
