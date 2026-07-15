package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func sanitizeSecretText(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Reuse existing token redaction for Telegram bot tokens in URLs/text.
	s = redactTelegramToken(s)
	return s
}

func transcribeTelegramVoiceMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) (string, error) {
	if bot == nil || msg == nil || msg.Voice == nil {
		return "", errors.New("voice message is required")
	}
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		return "", errors.New("OPENAI_API_KEY is empty")
	}
	model := strings.TrimSpace(os.Getenv("AUDIO_TRANSCRIPTION_MODEL"))
	if model == "" {
		model = "whisper-1"
	}
	fileID := strings.TrimSpace(msg.Voice.FileID)
	if fileID == "" {
		return "", errors.New("voice file id is empty")
	}
	fileURL, err := getTelegramFileDirectURL(bot, fileID)
	if err != nil {
		return "", fmt.Errorf("telegram file url: %w", err)
	}
	audioBytes, fileName, err := downloadTelegramAudioForTranscription(fileURL)
	if err != nil {
		return "", err
	}
	text, err := transcribeAudioBytes(apiKey, model, fileName, audioBytes)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(text), nil
}

func downloadTelegramAudioForTranscription(fileURL string) ([]byte, string, error) {
	fileURL = strings.TrimSpace(fileURL)
	if fileURL == "" {
		return nil, "", errors.New("telegram file url is empty")
	}
	client := &http.Client{Timeout: 30 * time.Second}
	bodyBytes, status, err := fetchTelegramFileBytes(client, fileURL)
	if err != nil {
		return nil, "", err
	}
	// Local Bot API may return 404 for some forwarded/reposted files.
	// Retry via public Telegram endpoint to preserve legacy behavior.
	if status == http.StatusNotFound {
		if fallbackURL := swapToPublicTelegramFileURL(fileURL); fallbackURL != "" && fallbackURL != fileURL {
			if debugTriggerLogEnabled {
				log.Printf("voice transcription file retry via public telegram endpoint")
			}
			bodyBytes2, status2, err2 := fetchTelegramFileBytes(client, fallbackURL)
			if err2 == nil && status2 >= 200 && status2 < 300 && len(bodyBytes2) > 0 {
				bodyBytes = bodyBytes2
				status = status2
			}
		}
	}
	if status < 200 || status >= 300 {
		return nil, "", fmt.Errorf("telegram file status=%d body=%s", status, clipText(string(bodyBytes), 300))
	}
	if len(bodyBytes) == 0 {
		return nil, "", errors.New("telegram file is empty")
	}
	ext := ".ogg"
	if parsed, err := url.Parse(fileURL); err == nil {
		if p := strings.TrimSpace(parsed.Path); p != "" {
			if got := strings.TrimSpace(filepath.Ext(p)); got != "" {
				ext = got
			}
		}
	}
	return bodyBytes, "voice" + ext, nil
}

func fetchTelegramFileBytes(client *http.Client, fileURL string) ([]byte, int, error) {
	req, err := http.NewRequest(http.MethodGet, fileURL, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 30<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return bodyBytes, resp.StatusCode, nil
}

func swapToPublicTelegramFileURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil {
		return ""
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if host == "" {
		return ""
	}
	if host != "127.0.0.1" && host != "localhost" {
		return ""
	}
	u.Scheme = "https"
	u.Host = "api.telegram.org"
	return u.String()
}

func transcribeAudioBytes(apiKey, model, fileName string, audioBytes []byte) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", strings.TrimSpace(model)); err != nil {
		return "", err
	}
	part, err := writer.CreateFormFile("file", strings.TrimSpace(fileName))
	if err != nil {
		return "", err
	}
	if _, err := part.Write(audioBytes); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/audio/transcriptions", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("openai transcription status=%d body=%s", resp.StatusCode, clipText(string(raw), 600))
	}
	var payload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", err
	}
	return strings.TrimSpace(payload.Text), nil
}

func searchImageInSerpAPI(query string) (generatedImage, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return generatedImage{}, errors.New("empty search query")
	}

	apiKey := strings.TrimSpace(os.Getenv("SERPAPI_KEY"))
	if apiKey == "" {
		return generatedImage{}, errors.New("SERPAPI_KEY is required for search_image")
	}
	engine := strings.TrimSpace(os.Getenv("SERPAPI_ENGINE"))
	if engine == "" {
		engine = "google_images"
	}

	params := url.Values{}
	params.Set("api_key", apiKey)
	params.Set("engine", engine)
	params.Set("q", query)
	params.Set("hl", "ru")
	params.Set("gl", "ru")
	params.Set("num", "10")
	params.Set("safe", "active")

	endpoint := "https://serpapi.com/search.json?" + params.Encode()
	client := &http.Client{Timeout: 20 * time.Second}
	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return generatedImage{}, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return generatedImage{}, err
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return generatedImage{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return generatedImage{}, fmt.Errorf("serpapi status=%d body=%s", resp.StatusCode, clipText(string(bodyBytes), 600))
	}

	var payload struct {
		Error        string            `json:"error"`
		ImagesResult []serpImageResult `json:"images_results"`
	}
	if err := json.Unmarshal(bodyBytes, &payload); err != nil {
		return generatedImage{}, err
	}
	if strings.TrimSpace(payload.Error) != "" {
		return generatedImage{}, errors.New(payload.Error)
	}
	if len(payload.ImagesResult) == 0 {
		return generatedImage{}, errors.New("nothing found")
	}

	candidates := collectSerpImageCandidateURLs(payload.ImagesResult)
	if len(candidates) == 0 {
		return generatedImage{}, errors.New("image URL is empty")
	}

	perm := rand.Perm(len(candidates))
	var lastErr error
	for _, idx := range perm {
		u := candidates[idx]
		imgBytes, err := fetchImageBytes(u)
		if err != nil {
			lastErr = err
			continue
		}
		return generatedImage{Bytes: imgBytes}, nil
	}
	if lastErr != nil {
		return generatedImage{}, fmt.Errorf("all image links failed: %w", lastErr)
	}
	return generatedImage{}, errors.New("image URL is empty")
}

func searchImageInSerpAPIWithRetryQueries(ctx templateContext, primaryQuery string) (generatedImage, error) {
	queries := imageSearchRetryQueries(ctx, primaryQuery)
	if len(queries) == 0 {
		return generatedImage{}, errors.New("empty search query")
	}
	var lastErr error
	for i, query := range queries {
		img, err := searchImageInSerpAPI(query)
		if err == nil {
			if i > 0 {
				log.Printf("search image retry ok primary=%q query=%q", clipText(queries[0], 160), clipText(query, 160))
			}
			return img, nil
		}
		lastErr = err
		if !isImageSearchNoResults(err) {
			return generatedImage{}, err
		}
		log.Printf("search image no results query=%q: %v", clipText(query, 160), err)
	}
	if lastErr != nil {
		return generatedImage{}, lastErr
	}
	return generatedImage{}, errors.New("nothing found")
}

func imageSearchRetryQueries(ctx templateContext, primaryQuery string) []string {
	queries := make([]string, 0, 3)
	seen := make(map[string]struct{}, 3)
	appendQuery := func(raw string) {
		q := strings.TrimSpace(raw)
		if q == "" {
			return
		}
		key := strings.ToLower(q)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		queries = append(queries, q)
	}
	appendQuery(primaryQuery)
	appendQuery(ctx.CapturingText)
	if ctx.Msg != nil {
		replacements := buildMessageTemplateReplacements(ctx.Bot, ctx.Msg)
		appendQuery(replacements["{{message}}"])
	}
	return queries
}

func isImageSearchNoResults(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return msg == "nothing found" || strings.Contains(msg, "hasn't returned any results") || strings.Contains(msg, "no results")
}

type serpImageResult struct {
	Original  string `json:"original"`
	Link      string `json:"link"`
	Thumbnail string `json:"thumbnail"`
}

func collectSerpImageCandidateURLs(results []serpImageResult) []string {
	candidates := make([]string, 0, len(results)*2)
	seen := make(map[string]struct{}, len(results)*2)
	appendURL := func(raw string) {
		u := strings.TrimSpace(raw)
		if u == "" {
			return
		}
		if _, ok := seen[u]; ok {
			return
		}
		seen[u] = struct{}{}
		candidates = append(candidates, u)
	}
	for _, it := range results {
		appendURL(it.Original)
		appendURL(it.Thumbnail)
		appendURL(it.Link)
	}
	return candidates
}

func fetchImageBytes(imageURL string) ([]byte, error) {
	u, err := validateExternalImageURL(imageURL)
	if err != nil {
		return nil, err
	}
	return fetchImageBytesFromURL(u, false)
}

func fetchTrustedTelegramImageBytes(imageURL string) ([]byte, error) {
	u, err := validateTrustedImageURL(imageURL)
	if err != nil {
		return nil, err
	}
	return fetchImageBytesFromURL(u, true)
}

func fetchImageBytesFromURL(u *url.URL, allowPrivateRedirect bool) ([]byte, error) {
	imageURL := u.String()
	client := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 5 {
				return errors.New("too many redirects")
			}
			if allowPrivateRedirect {
				_, err := validateTrustedImageURL(req.URL.String())
				return err
			}
			_, err := validateExternalImageURL(req.URL.String())
			return err
		},
	}
	req, err := http.NewRequest("GET", u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Accept", "image/*,*/*;q=0.8")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 12<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("download status=%d url=%s", resp.StatusCode, clipText(imageURL, 140))
	}
	if len(bodyBytes) == 0 {
		return nil, fmt.Errorf("downloaded empty body url=%s", clipText(imageURL, 140))
	}
	headerType := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	detectedType := strings.ToLower(strings.TrimSpace(http.DetectContentType(bodyBytes)))
	if !strings.HasPrefix(detectedType, "image/") {
		if headerType != "" {
			return nil, fmt.Errorf("not an image content-type=%s detected=%s url=%s", headerType, detectedType, clipText(imageURL, 140))
		}
		return nil, fmt.Errorf("not an image detected=%s url=%s", detectedType, clipText(imageURL, 140))
	}
	return bodyBytes, nil
}

func validateTrustedImageURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("empty image url")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("bad image url: %w", err)
	}
	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	if scheme != "http" && scheme != "https" {
		return nil, errors.New("unsupported image url scheme")
	}
	if strings.TrimSpace(u.Hostname()) == "" {
		return nil, errors.New("empty image host")
	}
	return u, nil
}

func validateExternalImageURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("empty image url")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("bad image url: %w", err)
	}
	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	if scheme != "http" && scheme != "https" {
		return nil, errors.New("unsupported image url scheme")
	}
	host := strings.TrimSpace(u.Hostname())
	if host == "" {
		return nil, errors.New("empty image host")
	}
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".local") {
		return nil, errors.New("local image hosts are not allowed")
	}

	if ip := net.ParseIP(host); ip != nil {
		if isBlockedImageHostIP(ip) {
			return nil, errors.New("private image host ip is not allowed")
		}
		return u, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("image host resolve failed: %w", err)
	}
	if len(addrs) == 0 {
		return nil, errors.New("image host resolve returned no ip")
	}
	for _, addr := range addrs {
		if isBlockedImageHostIP(addr.IP) {
			return nil, errors.New("private image host ip is not allowed")
		}
	}
	return u, nil
}

func isBlockedImageHostIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	if cidrContainsIP("100.64.0.0/10", ip) {
		return true
	}
	return false
}

func cidrContainsIP(cidr string, ip net.IP) bool {
	_, n, err := net.ParseCIDR(cidr)
	if err != nil || n == nil || ip == nil {
		return false
	}
	return n.Contains(ip)
}

type openAITokenUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

type chatGPTReplyResult struct {
	Text  string
	Usage openAITokenUsage
}

func generateChatGPTReply(ctx templateContext, promptTemplate string, recentContext string) (chatGPTReplyResult, error) {
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		return chatGPTReplyResult{}, errors.New("OPENAI_API_KEY is empty")
	}
	model := strings.TrimSpace(os.Getenv("OPENAI_MODEL"))
	if model == "" {
		model = "gpt-5-mini"
	}

	prompt := buildPromptFromMessage(ctx, promptTemplate)
	if strings.TrimSpace(recentContext) != "" {
		prompt = prompt + "\n\nБлижайший контекст чата (последние сообщения):\n" + recentContext
	}
	if linkCtx := strings.TrimSpace(buildLinkContextForMessage(ctx.Msg)); linkCtx != "" {
		prompt += "\n\nКонтекст по ссылкам (режим чтения):\n" + linkCtx
	}
	if debugGPTLogEnabled {
		log.Printf("gpt request model=%s prompt=%q", model, clipLogText(prompt, 200))
	}

	userMessage := map[string]interface{}{"role": "user", "content": prompt}
	if imageURL, ok := resolveMessageImageURL(ctx.Bot, ctx.Msg); ok {
		openAIImageURL, err := buildOpenAITelegramImageDataURL(imageURL)
		if err != nil {
			return chatGPTReplyResult{}, fmt.Errorf("image context failed: %w", err)
		}
		userMessage["content"] = []map[string]interface{}{
			{"type": "text", "text": prompt},
			{
				"type": "image_url",
				"image_url": map[string]string{
					"url": openAIImageURL,
				},
			},
		}
		if debugGPTLogEnabled {
			log.Printf("gpt request multimodal image_url=%q", clipLogText(sanitizeSecretText(imageURL), 200))
		}
	}
	payload := map[string]interface{}{
		"model": model,
		"messages": []map[string]interface{}{
			userMessage,
		},
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return chatGPTReplyResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 25 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return chatGPTReplyResult{}, err
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return chatGPTReplyResult{}, err
	}
	if debugGPTLogEnabled {
		log.Printf("gpt response status=%d body=%q", resp.StatusCode, clipLogText(sanitizeSecretText(string(bodyBytes)), 200))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return chatGPTReplyResult{}, fmt.Errorf("openai status=%d body=%s", resp.StatusCode, clipText(sanitizeSecretText(string(bodyBytes)), 600))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return chatGPTReplyResult{}, err
	}
	if len(result.Choices) == 0 {
		return chatGPTReplyResult{}, errors.New("empty choices")
	}
	out := strings.TrimSpace(result.Choices[0].Message.Content)
	if out == "" {
		return chatGPTReplyResult{}, errors.New("empty answer")
	}
	usage := openAITokenUsage{
		PromptTokens:     result.Usage.PromptTokens,
		CompletionTokens: result.Usage.CompletionTokens,
		TotalTokens:      result.Usage.TotalTokens,
	}
	if usage.TotalTokens <= 0 {
		usage.PromptTokens = estimateTextTokens(prompt)
		usage.CompletionTokens = estimateTextTokens(out)
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	if debugGPTLogEnabled {
		log.Printf("gpt usage prompt_tokens=%d completion_tokens=%d total_tokens=%d", usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens)
	}
	return chatGPTReplyResult{Text: out, Usage: usage}, nil
}

func estimateTextTokens(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	runes := len([]rune(s))
	words := len(strings.Fields(s))
	byRunes := (runes + 2) / 3
	byWords := words * 2
	if byWords > byRunes {
		return byWords
	}
	if byRunes < 1 {
		return 1
	}
	return byRunes
}

func generateParticipantPortrait(oldPortrait string, messages []string) (string, error) {
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		return "", errors.New("OPENAI_API_KEY is empty")
	}
	model := strings.TrimSpace(os.Getenv("OPENAI_MODEL"))
	if model == "" {
		model = "gpt-5-mini"
	}
	cleanMessages := make([]string, 0, len(messages))
	for _, message := range messages {
		val := strings.TrimSpace(message)
		if val == "" {
			continue
		}
		cleanMessages = append(cleanMessages, clipText(val, 900))
	}
	if len(cleanMessages) == 0 {
		return "", errors.New("empty message batch")
	}
	var batch strings.Builder
	for i, message := range cleanMessages {
		fmt.Fprintf(&batch, "%d) %s\n", i+1, message)
	}
	var userPrompt strings.Builder
	if strings.TrimSpace(oldPortrait) == "" {
		userPrompt.WriteString("Составь краткий портрет участника чата по его последним сообщениям.\n")
		userPrompt.WriteString("Верни только сам портрет на русском языке, без вводных и дисклеймеров.\n")
	} else {
		userPrompt.WriteString("Обнови портрет участника чата.\n")
		userPrompt.WriteString("Учитывай старый портрет и новые сообщения.\n")
		userPrompt.WriteString("Верни только обновленный портрет на русском языке, без вводных и дисклеймеров.\n\n")
		userPrompt.WriteString("Старый портрет:\n")
		userPrompt.WriteString(strings.TrimSpace(oldPortrait))
		userPrompt.WriteString("\n\n")
	}
	userPrompt.WriteString("Новые сообщения участника:\n")
	userPrompt.WriteString(strings.TrimSpace(batch.String()))

	systemPrompt := "Ты анализируешь стиль общения участника чата. " +
		"Пиши мягко и нейтрально, без категоричности. " +
		"Формат: 4-8 коротких предложений про манеру общения, интересы, эмоциональные реакции и предпочтительный стиль ответа."
	payload := map[string]interface{}{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt.String()},
		},
		"temperature": 0.3,
		"max_tokens":  500,
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("openai portrait status=%d body=%s", resp.StatusCode, clipText(string(bodyBytes), 600))
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return "", err
	}
	if len(result.Choices) == 0 {
		return "", errors.New("empty portrait choices")
	}
	out := strings.TrimSpace(result.Choices[0].Message.Content)
	if out == "" {
		return "", errors.New("empty portrait answer")
	}
	return out, nil
}

func generateChatSummary(messages []string) (string, error) {
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		return "", errors.New("OPENAI_API_KEY is empty")
	}
	model := strings.TrimSpace(os.Getenv("OPENAI_MODEL"))
	if model == "" {
		model = "gpt-5-mini"
	}
	cleanMessages := make([]string, 0, len(messages))
	for _, message := range messages {
		val := strings.TrimSpace(message)
		if val == "" {
			continue
		}
		cleanMessages = append(cleanMessages, clipText(val, 900))
	}
	if len(cleanMessages) == 0 {
		return "", errors.New("empty message batch")
	}
	var batch strings.Builder
	for i, message := range cleanMessages {
		fmt.Fprintf(&batch, "%d) %s\n", i+1, message)
	}
	var userPrompt strings.Builder
	userPrompt.WriteString("Составь новую краткую сводку переписки чата только по этим сообщениям.\n")
	userPrompt.WriteString("Не используй никакие прошлые сводки или внешний контекст.\n")
	userPrompt.WriteString("Верни только сводку на русском, без вводных фраз и без дисклеймеров.\n")
	userPrompt.WriteString("Новые сообщения чата:\n")
	userPrompt.WriteString(strings.TrimSpace(batch.String()))

	systemPrompt := "Ты делаешь сжатую полезную сводку чата. " +
		"Пиши нейтрально и бережно. " +
		"Формат: 6-12 коротких пунктов с ключевыми темами, решениями и договоренностями без лишних деталей."
	payload := map[string]interface{}{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt.String()},
		},
		"temperature": 0.2,
		"max_tokens":  700,
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 35 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("openai summary status=%d body=%s", resp.StatusCode, clipText(string(bodyBytes), 600))
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return "", err
	}
	if len(result.Choices) == 0 {
		return "", errors.New("empty summary choices")
	}
	out := strings.TrimSpace(result.Choices[0].Message.Content)
	if out == "" {
		return "", errors.New("empty summary answer")
	}
	return out, nil
}

func generateChatGPTImage(ctx templateContext, promptTemplate string) (generatedImage, error) {
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		return generatedImage{}, errors.New("OPENAI_API_KEY is empty")
	}
	model := "gpt-image-1"
	prompt := buildPromptFromMessage(ctx, promptTemplate)
	size, err := chooseImageSizeWithChatGPT(apiKey, prompt)
	if err != nil {
		size = "1024x1024"
		log.Printf("gpt image orientation pick failed, fallback size=%s err=%v", size, err)
	}

	if debugGPTLogEnabled {
		log.Printf("gpt image request model=%s size=%s prompt=%q", model, size, clipLogText(prompt, 200))
	}

	payload := map[string]interface{}{
		"model":  model,
		"prompt": prompt,
		"size":   size,
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", "https://api.openai.com/v1/images/generations", bytes.NewReader(body))
	if err != nil {
		return generatedImage{}, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return generatedImage{}, err
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return generatedImage{}, err
	}
	if debugGPTLogEnabled {
		log.Printf("gpt image response status=%d body=%q", resp.StatusCode, clipLogText(string(bodyBytes), 200))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return generatedImage{}, fmt.Errorf("openai images status=%d body=%s", resp.StatusCode, clipText(string(bodyBytes), 600))
	}

	var result struct {
		Data []struct {
			URL     string `json:"url"`
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return generatedImage{}, err
	}
	if len(result.Data) == 0 {
		return generatedImage{}, errors.New("empty images data")
	}

	if u := strings.TrimSpace(result.Data[0].URL); u != "" {
		return generatedImage{URL: u}, nil
	}
	if b64 := strings.TrimSpace(result.Data[0].B64JSON); b64 != "" {
		decoded, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return generatedImage{}, fmt.Errorf("decode b64 image failed: %w", err)
		}
		if len(decoded) == 0 {
			return generatedImage{}, errors.New("decoded image is empty")
		}
		return generatedImage{Bytes: decoded}, nil
	}

	return generatedImage{}, errors.New("image payload has neither url nor b64_json")
}

func chooseImageSizeWithChatGPT(apiKey, prompt string) (string, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return "", errors.New("OPENAI_API_KEY is empty")
	}
	model := strings.TrimSpace(os.Getenv("OPENAI_ORIENTATION_MODEL"))
	if model == "" {
		model = strings.TrimSpace(os.Getenv("OPENAI_MODEL"))
	}
	if model == "" {
		model = "gpt-5-mini"
	}
	systemPrompt := "Ты классификатор ориентации для генерации изображения. " +
		"Верни только одно слово: portrait, landscape или square. " +
		"portrait — если в фокусе человек/персонаж/лицо; landscape — если сцена/город/пейзаж/панорама; square — иначе."
	userPrompt := "Запрос пользователя:\n" + strings.TrimSpace(prompt)
	payload := map[string]interface{}{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
		"temperature": 0,
		"max_tokens":  8,
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("orientation pick status=%d body=%s", resp.StatusCode, clipText(string(bodyBytes), 600))
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return "", err
	}
	if len(result.Choices) == 0 {
		return "", errors.New("empty orientation choices")
	}
	orientation := parseOrientationChoice(result.Choices[0].Message.Content)
	switch orientation {
	case "portrait":
		return "1024x1536", nil
	case "landscape":
		return "1536x1024", nil
	default:
		return "1024x1024", nil
	}
}

func parseOrientationChoice(s string) string {
	v := strings.ToLower(strings.TrimSpace(s))
	switch {
	case strings.Contains(v, "portrait"), strings.Contains(v, "портрет"):
		return "portrait"
	case strings.Contains(v, "landscape"), strings.Contains(v, "land"), strings.Contains(v, "пейзаж"), strings.Contains(v, "панорам"):
		return "landscape"
	default:
		return "square"
	}
}

func getChatMemberTagRaw(token string, chatID, userID int64) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	u := fmt.Sprintf(
		"https://api.telegram.org/bot%s/getChatMember?chat_id=%s&user_id=%s",
		token,
		url.QueryEscape(strconv.FormatInt(chatID, 10)),
		url.QueryEscape(strconv.FormatInt(userID, 10)),
	)
	client := &http.Client{Timeout: 6 * time.Second}
	resp, err := client.Get(u)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var payload struct {
		OK     bool `json:"ok"`
		Result struct {
			Tag         string `json:"tag"`
			CustomTitle string `json:"custom_title"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return ""
	}
	if !payload.OK {
		return ""
	}
	tag := strings.TrimSpace(payload.Result.Tag)
	if tag != "" {
		return tag
	}
	return strings.TrimSpace(payload.Result.CustomTitle)
}

func reportChatFailure(bot *tgbotapi.BotAPI, chatID int64, context string, err error) {
	if !chatErrorLogEnabled || bot == nil || chatID == 0 || err == nil {
		return
	}
	// Never expose raw upstream errors to chat (can leak sensitive internals/tokens).
	text := fmt.Sprintf("⚠️ %s. Подробности в логах.", strings.TrimSpace(context))
	m := tgbotapi.NewMessage(chatID, text)
	_, _ = bot.Send(m)
}

func buildOpenAIImageDataURL(imageURL string) (string, error) {
	imageURL = strings.TrimSpace(imageURL)
	if imageURL == "" {
		return "", errors.New("image url is empty")
	}
	imgBytes, err := fetchImageBytes(imageURL)
	if err != nil {
		return "", err
	}
	if maxMB := gptImageContextMaxMB(); maxMB > 0 && len(imgBytes) > maxMB<<20 {
		return "", fmt.Errorf("image is too large for GPT context: %.2f MB > %d MB", float64(len(imgBytes))/(1024*1024), maxMB)
	}
	ctype := strings.ToLower(strings.TrimSpace(http.DetectContentType(imgBytes)))
	if !strings.HasPrefix(ctype, "image/") {
		ctype = "image/jpeg"
	}
	enc := base64.StdEncoding.EncodeToString(imgBytes)
	return "data:" + ctype + ";base64," + enc, nil
}

func buildOpenAITelegramImageDataURL(imageURL string) (string, error) {
	imageURL = strings.TrimSpace(imageURL)
	if imageURL == "" {
		return "", errors.New("image url is empty")
	}
	imgBytes, err := fetchTrustedTelegramImageBytes(imageURL)
	if err != nil {
		return "", err
	}
	if maxMB := gptImageContextMaxMB(); maxMB > 0 && len(imgBytes) > maxMB<<20 {
		return "", fmt.Errorf("image is too large for GPT context: %.2f MB > %d MB", float64(len(imgBytes))/(1024*1024), maxMB)
	}
	ctype := strings.ToLower(strings.TrimSpace(http.DetectContentType(imgBytes)))
	if !strings.HasPrefix(ctype, "image/") {
		ctype = "image/jpeg"
	}
	enc := base64.StdEncoding.EncodeToString(imgBytes)
	return "data:" + ctype + ";base64," + enc, nil
}

func clipText(s string, max int) string {
	s = strings.TrimSpace(strings.ToValidUTF8(s, ""))
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return strings.TrimSpace(string(r[:max])) + "..."
}
