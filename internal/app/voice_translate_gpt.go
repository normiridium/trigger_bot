package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type openAITranscriptSegment struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

type openAITranscriptResult struct {
	Text     string                    `json:"text"`
	Segments []openAITranscriptSegment `json:"segments"`
}

func openAIGPTTranslateCacheProvider() string {
	parts := []string{
		"openai-gpt",
		"timed-dubbing-v4",
		openAIGPTTranslateTranscribeModel(),
		openAIGPTTranslateModel(),
		openAIGPTTranslateTTSModel(),
		openAIGPTTranslateTTSVoice(),
	}
	return strings.Join(parts, ":")
}

func openAIGPTTranslateModel() string {
	if v := strings.TrimSpace(os.Getenv("GPT_TRANSLATE_MODEL")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("OPENAI_MODEL")); v != "" {
		return v
	}
	return "gpt-4.1"
}

func openAIGPTTranslateTranscribeModel() string {
	if v := strings.TrimSpace(os.Getenv("GPT_TRANSLATE_TRANSCRIBE_MODEL")); v != "" {
		return strings.Trim(v, `"'`)
	}
	if v := strings.TrimSpace(os.Getenv("AUDIO_TRANSCRIPTION_MODEL")); v != "" {
		return strings.Trim(v, `"'`)
	}
	return "whisper-1"
}

func openAIGPTTranslateTTSModel() string {
	if v := strings.TrimSpace(os.Getenv("GPT_TRANSLATE_TTS_MODEL")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("OPENAI_TTS_MODEL")); v != "" {
		return v
	}
	return "gpt-4o-mini-tts"
}

func openAIGPTTranslateTTSVoice() string {
	if v := strings.TrimSpace(os.Getenv("GPT_TRANSLATE_TTS_VOICE")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("OPENAI_TTS_VOICE")); v != "" {
		return v
	}
	return "marin"
}

func openAIGPTTranslateTimeout() time.Duration {
	sec := envInt("GPT_TRANSLATE_TIMEOUT_SEC", envInt("VOICE_TRANSLATE_TIMEOUT_SEC", 500))
	if sec < 60 {
		sec = 60
	}
	return time.Duration(sec) * time.Second
}

func openAILanguageName(code string) string {
	switch normalizeVOTLang(code) {
	case "ru":
		return "Russian"
	case "en":
		return "English"
	case "nl":
		return "Dutch"
	case "uk":
		return "Ukrainian"
	case "be":
		return "Belarusian"
	case "pl":
		return "Polish"
	case "de":
		return "German"
	case "fr":
		return "French"
	case "es":
		return "Spanish"
	case "it":
		return "Italian"
	case "pt":
		return "Portuguese"
	case "tr":
		return "Turkish"
	case "ja":
		return "Japanese"
	case "ko":
		return "Korean"
	case "zh":
		return "Chinese"
	case "ar":
		return "Arabic"
	case "he":
		return "Hebrew"
	case "fa":
		return "Persian"
	case "hi":
		return "Hindi"
	case "id":
		return "Indonesian"
	case "vi":
		return "Vietnamese"
	case "th":
		return "Thai"
	case "kk":
		return "Kazakh"
	case "lt":
		return "Lithuanian"
	case "lv":
		return "Latvian"
	case "et":
		return "Estonian"
	case "fi":
		return "Finnish"
	case "sv":
		return "Swedish"
	case "no":
		return "Norwegian"
	case "da":
		return "Danish"
	case "cs":
		return "Czech"
	case "sk":
		return "Slovak"
	case "ro":
		return "Romanian"
	case "hu":
		return "Hungarian"
	case "el":
		return "Greek"
	case "ka":
		return "Georgian"
	case "hy":
		return "Armenian"
	case "az":
		return "Azerbaijani"
	default:
		return strings.ToUpper(strings.TrimSpace(code))
	}
}

func extractAudioForOpenAI(sourcePath, outPath string) error {
	cmd := exec.Command(
		"ffmpeg",
		"-hide_banner",
		"-loglevel", "error",
		"-y",
		"-i", sourcePath,
		"-map", "0:a:0?",
		"-vn",
		"-ac", "1",
		"-ar", "16000",
		"-b:a", "96k",
		outPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg extract audio for openai failed: %v (%s)", err, clipText(strings.TrimSpace(string(out)), 600))
	}
	return nil
}

func runOpenAITranscriptionLocal(sourcePath, workDir, srcLang string) (openAITranscriptResult, error) {
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		return openAITranscriptResult{}, errors.New("OPENAI_API_KEY is empty")
	}
	audioPath := filepath.Join(workDir, "openai_source_audio.mp3")
	if err := extractAudioForOpenAI(sourcePath, audioPath); err != nil {
		return openAITranscriptResult{}, err
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", openAIGPTTranslateTranscribeModel()); err != nil {
		return openAITranscriptResult{}, err
	}
	if v := normalizeVOTLang(srcLang); v != "" && v != "auto" {
		if err := writer.WriteField("language", v); err != nil {
			return openAITranscriptResult{}, err
		}
	}
	if err := writer.WriteField("response_format", "verbose_json"); err != nil {
		return openAITranscriptResult{}, err
	}
	if err := writer.WriteField("timestamp_granularities[]", "segment"); err != nil {
		return openAITranscriptResult{}, err
	}
	part, err := writer.CreateFormFile("file", filepath.Base(audioPath))
	if err != nil {
		return openAITranscriptResult{}, err
	}
	fd, err := os.Open(audioPath)
	if err != nil {
		return openAITranscriptResult{}, err
	}
	if _, err := io.Copy(part, fd); err != nil {
		_ = fd.Close()
		return openAITranscriptResult{}, err
	}
	_ = fd.Close()
	if err := writer.Close(); err != nil {
		return openAITranscriptResult{}, err
	}

	req, err := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/audio/transcriptions", &body)
	if err != nil {
		return openAITranscriptResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	client := &http.Client{Timeout: openAIGPTTranslateTimeout()}
	resp, err := client.Do(req)
	if err != nil {
		return openAITranscriptResult{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return openAITranscriptResult{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return openAITranscriptResult{}, fmt.Errorf("openai transcription status=%d body=%s", resp.StatusCode, clipText(sanitizeSecretText(string(raw)), 900))
	}
	var out openAITranscriptResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return openAITranscriptResult{}, err
	}
	out.Text = strings.TrimSpace(out.Text)
	for i := range out.Segments {
		out.Segments[i].Text = strings.TrimSpace(out.Segments[i].Text)
	}
	if out.Text == "" {
		return openAITranscriptResult{}, errors.New("openai transcription returned empty text")
	}
	return out, nil
}

func callOpenAIChatCompletionText(systemPrompt, userPrompt string, jsonObject bool) (string, error) {
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		return "", errors.New("OPENAI_API_KEY is empty")
	}
	payload := map[string]any{
		"model":       openAIGPTTranslateModel(),
		"temperature": 0.15,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
	}
	if jsonObject {
		payload["response_format"] = map[string]string{"type": "json_object"}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: openAIGPTTranslateTimeout()}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("openai chat status=%d body=%s", resp.StatusCode, clipText(sanitizeSecretText(string(raw)), 900))
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", err
	}
	if len(parsed.Choices) == 0 || strings.TrimSpace(parsed.Choices[0].Message.Content) == "" {
		return "", errors.New("openai chat returned empty translation")
	}
	return strings.TrimSpace(parsed.Choices[0].Message.Content), nil
}

func translateTranscriptTextWithOpenAI(text, srcLang, resLang string, compactForDubbing bool) (string, error) {
	targetName := openAILanguageName(resLang)
	sourceName := "auto-detected language"
	if v := normalizeVOTLang(srcLang); v != "" && v != "auto" {
		sourceName = openAILanguageName(v)
	}
	mode := "Translate faithfully. Keep the full meaning."
	if compactForDubbing {
		mode = "Translate for voice dubbing. Keep it natural and compact enough to be spoken in roughly the same time."
	}
	systemPrompt := fmt.Sprintf("You are a media dubbing translator. Source language: %s. Target language: %s. %s Translate profanity and insults by meaning; do not transliterate offensive words. Do not add explanations. Do not use markdown. Return only the translated text.", sourceName, targetName, mode)
	return callOpenAIChatCompletionText(systemPrompt, text, false)
}

func translateTranscriptSegmentsWithOpenAI(segments []openAITranscriptSegment, srcLang, resLang string) ([]string, error) {
	if len(segments) == 0 {
		return nil, errors.New("openai transcription returned no subtitle segments")
	}
	type segmentForRequest struct {
		Index int    `json:"index"`
		Text  string `json:"text"`
	}
	items := make([]segmentForRequest, 0, len(segments))
	for i, s := range segments {
		if strings.TrimSpace(s.Text) == "" {
			continue
		}
		items = append(items, segmentForRequest{Index: i, Text: strings.TrimSpace(s.Text)})
	}
	if len(items) == 0 {
		return nil, errors.New("openai transcription returned no subtitle segments")
	}
	payload, _ := json.Marshal(map[string]any{
		"source_language": openAILanguageName(srcLang),
		"target_language": openAILanguageName(resLang),
		"segments":        items,
	})
	systemPrompt := "You translate subtitle segments. Return JSON only in this exact shape: {\"translations\":[{\"index\":0,\"text\":\"...\"}]}. Preserve every index exactly once. Translate profanity and insults by meaning; do not transliterate offensive words. Do not add explanations."
	raw, err := callOpenAIChatCompletionText(systemPrompt, string(payload), true)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Translations []struct {
			Index int    `json:"index"`
			Text  string `json:"text"`
		} `json:"translations"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("openai subtitle translation returned invalid json: %w", err)
	}
	out := make([]string, len(segments))
	for _, tr := range parsed.Translations {
		if tr.Index < 0 || tr.Index >= len(out) {
			return nil, fmt.Errorf("openai subtitle translation returned invalid index %d", tr.Index)
		}
		out[tr.Index] = strings.TrimSpace(tr.Text)
	}
	for _, item := range items {
		if strings.TrimSpace(out[item.Index]) == "" {
			return nil, fmt.Errorf("openai subtitle translation missing segment %d", item.Index)
		}
	}
	return out, nil
}

func writeOpenAISRT(path string, segments []openAITranscriptSegment, translations []string) error {
	var b strings.Builder
	idx := 1
	for i, s := range segments {
		text := ""
		if i < len(translations) {
			text = strings.TrimSpace(translations[i])
		}
		if text == "" {
			continue
		}
		end := s.End
		if end <= s.Start {
			end = s.Start + 1
		}
		b.WriteString(fmt.Sprintf("%d\n%s --> %s\n%s\n\n", idx, fmtSRTTime(s.Start), fmtSRTTime(end), text))
		idx++
	}
	if idx == 1 {
		return errors.New("empty srt")
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

type openAITimedDubbingGroup struct {
	Index int     `json:"index"`
	Start float64 `json:"start_sec"`
	End   float64 `json:"end_sec"`
	Text  string  `json:"text"`
}

func (g openAITimedDubbingGroup) duration() float64 {
	if g.End <= g.Start {
		return 1
	}
	return g.End - g.Start
}

func groupTranscriptSegmentsForDubbing(segments []openAITranscriptSegment) []openAITimedDubbingGroup {
	maxGroupSec := envFloat("GPT_TRANSLATE_DUB_GROUP_MAX_SEC", 14)
	if maxGroupSec < 4 {
		maxGroupSec = 4
	}
	if maxGroupSec > 30 {
		maxGroupSec = 30
	}
	hardMaxGroupSec := maxGroupSec * 1.45
	if hardMaxGroupSec > 36 {
		hardMaxGroupSec = 36
	}
	maxGapSec := envFloat("GPT_TRANSLATE_DUB_GROUP_MAX_GAP_SEC", 1.2)
	if maxGapSec < 0 {
		maxGapSec = 0
	}
	strongGapSec := envFloat("GPT_TRANSLATE_DUB_GROUP_STRONG_GAP_SEC", 2.4)
	if strongGapSec < maxGapSec {
		strongGapSec = maxGapSec
	}
	maxRunes := envInt("GPT_TRANSLATE_DUB_GROUP_MAX_CHARS", 520)
	if maxRunes < 160 {
		maxRunes = 160
	}
	if maxRunes > 1200 {
		maxRunes = 1200
	}
	minGroupSec := envFloat("GPT_TRANSLATE_DUB_GROUP_MIN_SEC", 2.2)
	if minGroupSec < 0.5 {
		minGroupSec = 0.5
	}
	if minGroupSec > maxGroupSec {
		minGroupSec = maxGroupSec
	}
	minRunes := envInt("GPT_TRANSLATE_DUB_GROUP_MIN_CHARS", 28)
	if minRunes < 8 {
		minRunes = 8
	}
	if minRunes > maxRunes {
		minRunes = maxRunes
	}

	var groups []openAITimedDubbingGroup
	var cur openAITimedDubbingGroup
	have := false
	flush := func() {
		if !have {
			return
		}
		cur.Index = len(groups)
		cur.Text = strings.TrimSpace(cur.Text)
		if cur.Text != "" {
			groups = append(groups, cur)
		}
		cur = openAITimedDubbingGroup{}
		have = false
	}
	for _, s := range segments {
		text := strings.TrimSpace(s.Text)
		if text == "" {
			continue
		}
		start := s.Start
		if start < 0 {
			start = 0
		}
		end := s.End
		if end <= start {
			end = start + 1
		}
		if !have {
			cur = openAITimedDubbingGroup{Start: start, End: end, Text: text}
			have = true
			continue
		}
		gap := start - cur.End
		if gap < 0 {
			gap = 0
		}
		nextEnd := end
		if nextEnd < cur.End {
			nextEnd = cur.End
		}
		nextText := strings.TrimSpace(cur.Text + " " + text)
		curRunes := len([]rune(cur.Text))
		nextRunes := len([]rune(nextText))
		curDur := cur.duration()
		nextDur := nextEnd - cur.Start
		curHasEnoughSpeech := curDur >= minGroupSec || curRunes >= minRunes
		hardBoundary := gap >= strongGapSec
		tooLong := nextDur > maxGroupSec && curHasEnoughSpeech
		tooHardLong := nextDur > hardMaxGroupSec
		tooManyChars := nextRunes > maxRunes && curHasEnoughSpeech
		naturalBoundary := gap > maxGapSec && curHasEnoughSpeech
		if !naturalBoundary && gap > 0.18 && curHasEnoughSpeech && endsOpenAIDubbingPhrase(cur.Text) && nextDur >= minGroupSec {
			naturalBoundary = true
		}
		if hardBoundary || tooHardLong || tooLong || tooManyChars || naturalBoundary {
			flush()
			cur = openAITimedDubbingGroup{Start: start, End: end, Text: text}
			have = true
			continue
		}
		cur.End = nextEnd
		cur.Text = nextText
	}
	flush()
	return groups
}

func endsOpenAIDubbingPhrase(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	runes := []rune(text)
	for i := len(runes) - 1; i >= 0; i-- {
		switch runes[i] {
		case '.', '!', '?', ';', ':', '…':
			return true
		case '"', '\'', ')', ']', '}', '»':
			continue
		default:
			return false
		}
	}
	return false
}

func translateTranscriptGroupsWithOpenAI(groups []openAITimedDubbingGroup, srcLang, resLang string) ([]string, error) {
	if len(groups) == 0 {
		return nil, errors.New("openai transcription returned no subtitle segments")
	}
	type groupForRequest struct {
		Index       int     `json:"index"`
		StartSec    float64 `json:"start_sec"`
		EndSec      float64 `json:"end_sec"`
		DurationSec float64 `json:"duration_sec"`
		Text        string  `json:"text"`
	}
	items := make([]groupForRequest, 0, len(groups))
	for _, group := range groups {
		text := strings.TrimSpace(group.Text)
		if text == "" {
			continue
		}
		items = append(items, groupForRequest{
			Index:       group.Index,
			StartSec:    group.Start,
			EndSec:      group.End,
			DurationSec: group.duration(),
			Text:        text,
		})
	}
	if len(items) == 0 {
		return nil, errors.New("openai transcription returned no subtitle segments")
	}
	payload, _ := json.Marshal(map[string]any{
		"source_language": openAILanguageName(srcLang),
		"target_language": openAILanguageName(resLang),
		"segments":        items,
	})
	systemPrompt := "You translate timed voice-over groups for dubbing. Treat all groups as one continuous transcript with shared context; do not translate isolated short sounds or repeated vowels as separate staccato words. Return JSON only in this exact shape: {\"translations\":[{\"index\":0,\"text\":\"...\"}]}. Preserve every index exactly once. Translate profanity and insults by meaning; do not transliterate offensive words. Keep each translation natural, compact, and speakable within duration_sec. Do not add explanations. Do not use markdown."
	raw, err := callOpenAIChatCompletionText(systemPrompt, string(payload), true)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Translations []struct {
			Index int    `json:"index"`
			Text  string `json:"text"`
		} `json:"translations"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("openai timed translation returned invalid json: %w", err)
	}
	out := make([]string, len(groups))
	for _, tr := range parsed.Translations {
		if tr.Index < 0 || tr.Index >= len(out) {
			return nil, fmt.Errorf("openai timed translation returned invalid index %d", tr.Index)
		}
		out[tr.Index] = strings.TrimSpace(tr.Text)
	}
	for _, item := range items {
		if strings.TrimSpace(out[item.Index]) == "" {
			return nil, fmt.Errorf("openai timed translation missing segment %d", item.Index)
		}
	}
	return out, nil
}

func runOpenAITTSLocal(text, workDir string) (string, error) {
	return runOpenAITTSLocalToPath(text, filepath.Join(workDir, "openai_translated.mp3"))
}

func runOpenAITTSLocalToPath(text, outPath string) (string, error) {
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		return "", errors.New("OPENAI_API_KEY is empty")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("empty text for openai tts")
	}
	payload := map[string]any{
		"model":           openAIGPTTranslateTTSModel(),
		"voice":           openAIGPTTranslateTTSVoice(),
		"input":           text,
		"response_format": "mp3",
		"instructions":    "Speak naturally in the target language as calm voice-over dubbing for a short video. Preserve profanity as part of translation. Keep the tempo close to the original, without theatrical delivery or extra pauses.",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/audio/speech", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: openAIGPTTranslateTimeout()}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		return "", fmt.Errorf("openai tts status=%d body=%s", resp.StatusCode, clipText(sanitizeSecretText(string(raw)), 900))
	}
	fd, err := os.Create(outPath)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(fd, resp.Body); err != nil {
		_ = fd.Close()
		return "", err
	}
	if err := fd.Close(); err != nil {
		return "", err
	}
	return outPath, nil
}

func fitOpenAITTSClipToWindow(inputPath, outPath string, windowSec float64) error {
	if windowSec <= 0 {
		return errors.New("empty timed dubbing window")
	}
	filter := fmt.Sprintf("apad,atrim=0:%.3f", windowSec)
	clipDur := audioDurationSec(inputPath)
	targetSpeechDur := windowSec * 0.96
	if clipDur > 0 && targetSpeechDur > 0.2 && clipDur > targetSpeechDur {
		filter = atempoFilterForRatio(clipDur/targetSpeechDur) + fmt.Sprintf(",apad,atrim=0:%.3f", windowSec)
	}
	cmd := exec.Command(
		"ffmpeg",
		"-hide_banner",
		"-loglevel", "error",
		"-y",
		"-i", inputPath,
		"-map", "0:a:0?",
		"-vn",
		"-af", filter,
		"-ac", "2",
		"-ar", "48000",
		"-c:a", "pcm_s16le",
		outPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg openai tts clip fit failed: %v (%s)", err, clipText(strings.TrimSpace(string(out)), 600))
	}
	return nil
}

func openAITTSWindowForGroup(groups []openAITimedDubbingGroup, index int, sourceDur float64) float64 {
	if index < 0 || index >= len(groups) {
		return 0
	}
	group := groups[index]
	windowEnd := group.End
	if index+1 < len(groups) {
		nextStart := groups[index+1].Start
		if nextStart > group.Start {
			windowEnd = nextStart
		}
	} else if sourceDur > group.Start {
		windowEnd = sourceDur
	}
	window := windowEnd - group.Start
	if window < group.duration() {
		window = group.duration()
	}
	return window
}

func assembleOpenAITimedDubbingTrack(outPath string, sourceDur float64, groups []openAITimedDubbingGroup, clipPaths []string) error {
	if sourceDur <= 0 {
		return errors.New("empty source duration for timed dubbing")
	}
	if len(groups) == 0 || len(groups) != len(clipPaths) {
		return errors.New("invalid timed dubbing clips")
	}
	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-y",
		"-f", "lavfi",
		"-t", fmt.Sprintf("%.3f", sourceDur),
		"-i", "anullsrc=channel_layout=stereo:sample_rate=48000",
	}
	for _, path := range clipPaths {
		args = append(args, "-i", path)
	}
	var filter strings.Builder
	filter.WriteString("[0:a]volume=0[base];")
	labels := []string{"[base]"}
	for i, group := range groups {
		delayMS := int(group.Start*1000 + 0.5)
		if delayMS < 0 {
			delayMS = 0
		}
		filter.WriteString(fmt.Sprintf("[%d:a]adelay=%d|%d[d%d];", i+1, delayMS, delayMS, i))
		labels = append(labels, fmt.Sprintf("[d%d]", i))
	}
	filter.WriteString(strings.Join(labels, ""))
	filter.WriteString(fmt.Sprintf("amix=inputs=%d:duration=first:dropout_transition=0,volume=%d,alimiter=limit=0.96[mix]", len(labels), len(labels)))
	args = append(args,
		"-filter_complex", filter.String(),
		"-map", "[mix]",
		"-t", fmt.Sprintf("%.3f", sourceDur),
		"-c:a", "libmp3lame",
		"-b:a", "160k",
		outPath,
	)
	cmd := exec.Command("ffmpeg", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg openai timed dubbing assembly failed: %v (%s)", err, clipText(strings.TrimSpace(string(out)), 600))
	}
	return nil
}

func formatOpenAITimedTranslationsText(groups []openAITimedDubbingGroup, translations []string) string {
	var b strings.Builder
	for i, group := range groups {
		if i >= len(translations) {
			continue
		}
		text := strings.TrimSpace(translations[i])
		if text == "" {
			continue
		}
		b.WriteString(fmt.Sprintf("%s --> %s\n%s\n\n", fmtSRTTime(group.Start), fmtSRTTime(group.End), text))
	}
	return strings.TrimSpace(b.String())
}

func buildOpenAITimedDubbingTrack(transcript openAITranscriptResult, sourcePath, workDir, srcLang, resLang string) (string, string, error) {
	sourceDur := audioDurationSec(sourcePath)
	groups := groupTranscriptSegmentsForDubbing(transcript.Segments)
	if len(groups) == 0 {
		return "", "", errors.New("openai transcription returned no subtitle segments")
	}
	if sourceDur <= 0 {
		sourceDur = groups[len(groups)-1].End
	}
	if sourceDur <= 0 {
		return "", "", errors.New("empty source duration for timed dubbing")
	}
	for i := range groups {
		if groups[i].End > sourceDur {
			groups[i].End = sourceDur
		}
		if groups[i].End <= groups[i].Start {
			groups[i].End = groups[i].Start + 0.75
			if groups[i].End > sourceDur {
				groups[i].End = sourceDur
			}
		}
	}
	translations, err := translateTranscriptGroupsWithOpenAI(groups, srcLang, resLang)
	if err != nil {
		return "", "", err
	}
	clipPaths := make([]string, 0, len(groups))
	for i := range groups {
		text := strings.TrimSpace(translations[i])
		if text == "" {
			return "", "", fmt.Errorf("openai timed translation missing segment %d", i)
		}
		rawPath := filepath.Join(workDir, fmt.Sprintf("openai_tts_group_%03d.mp3", i))
		if _, err := runOpenAITTSLocalToPath(text, rawPath); err != nil {
			return "", "", err
		}
		fitPath := filepath.Join(workDir, fmt.Sprintf("openai_tts_group_%03d.wav", i))
		windowSec := openAITTSWindowForGroup(groups, i, sourceDur)
		if err := fitOpenAITTSClipToWindow(rawPath, fitPath, windowSec); err != nil {
			return "", "", err
		}
		clipPaths = append(clipPaths, fitPath)
	}
	outPath := filepath.Join(workDir, "openai_translated_timed.mp3")
	if err := assembleOpenAITimedDubbingTrack(outPath, sourceDur, groups, clipPaths); err != nil {
		return "", "", err
	}
	return outPath, formatOpenAITimedTranslationsText(groups, translations), nil
}

func atempoFilterForRatio(ratio float64) string {
	if ratio <= 0 {
		return "atempo=1"
	}
	parts := []string{}
	for ratio > 2.0 {
		parts = append(parts, "atempo=2")
		ratio /= 2.0
	}
	for ratio < 0.5 {
		parts = append(parts, "atempo=0.5")
		ratio /= 0.5
	}
	parts = append(parts, fmt.Sprintf("atempo=%.4f", ratio))
	return strings.Join(parts, ",")
}

func fitOpenAITranslatedAudioToSource(sourcePath, translatedPath, workDir string) (string, error) {
	sourceDur := audioDurationSec(sourcePath)
	translatedDur := audioDurationSec(translatedPath)
	if sourceDur <= 0 || translatedDur <= 0 || translatedDur <= sourceDur*1.03 {
		return translatedPath, nil
	}
	ratio := translatedDur / sourceDur
	outPath := filepath.Join(workDir, "openai_translated_fit.mp3")
	filter := atempoFilterForRatio(ratio) + fmt.Sprintf(",apad,atrim=0:%.3f", sourceDur)
	cmd := exec.Command(
		"ffmpeg",
		"-hide_banner",
		"-loglevel", "error",
		"-y",
		"-i", translatedPath,
		"-af", filter,
		"-ac", "2",
		"-ar", "48000",
		"-c:a", "libmp3lame",
		"-b:a", "160k",
		outPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("ffmpeg openai tts time-fit failed: %v (%s)", err, clipText(strings.TrimSpace(string(out)), 600))
	}
	return outPath, nil
}

func copyOpenAITranslatedAudioToCache(cacheKey, mp3Path string) {
	cacheDst := voiceTranslateCacheMP3Path(cacheKey)
	if in, err := os.Open(mp3Path); err == nil {
		if out, err2 := os.Create(cacheDst); err2 == nil {
			_, _ = io.Copy(out, in)
			_ = out.Close()
			setVoiceTranslateCache(cacheKey, cacheDst, openAIGPTTranslateCacheProvider())
		}
		_ = in.Close()
	}
}

func processOpenAIVoiceTranslateTask(task voiceTranslateTask, sendCtx sendContext, progress *mediaProgressHandle, mediaInfo replyMediaInfo, sourcePath, workDir, srcLang, resLang, cacheKey string) {
	if task.Action == voiceTranslateActionText {
		if cached, ok := getFreshFile(voiceTextCachePath(cacheKey)); ok {
			if progress != nil {
				progress.SetFrame(8)
				progress.SetStage("Отправка результата")
			}
			if err := sendDocumentFromFileNamed(sendCtx, cached, voiceOutName(cacheKey, mediaInfo, ".txt"), ""); err != nil {
				reply(sendCtx, "Не удалось отправить текстовый файл GPT-перевода.", false)
			}
			return
		}
	}
	if task.Action == voiceTranslateActionSubs {
		if cached, ok := getFreshFile(voiceSubtitlesCachePath(cacheKey)); ok {
			if progress != nil {
				progress.SetFrame(8)
				progress.SetStage("Отправка результата")
			}
			if err := sendDocumentFromFileNamed(sendCtx, cached, voiceOutName(cacheKey, mediaInfo, ".srt"), ""); err != nil {
				reply(sendCtx, "Не удалось отправить SRT файл GPT-перевода.", false)
			}
			return
		}
	}

	mp3Path := filepath.Join(workDir, "openai_translated.mp3")
	if task.Action != voiceTranslateActionText && task.Action != voiceTranslateActionSubs {
		if cachedMP3, ok := getVoiceTranslateCache(cacheKey); ok {
			mp3Path = cachedMP3
		} else {
			if progress != nil {
				progress.SetFrame(3)
				progress.SetStage("Распознавание OpenAI")
			}
			transcript, err := runOpenAITranscriptionLocal(sourcePath, workDir, srcLang)
			if err != nil {
				if debugTriggerLogEnabled {
					log.Printf("gpt translate transcription failed chat=%d replyTo=%d err=%v", task.ChatID, task.ReplyTo, err)
				}
				reply(sendCtx, gptTranslateUserErrorMessage(err), false)
				return
			}
			if progress != nil {
				progress.SetFrame(4)
				progress.SetStage("Перевод GPT по таймингам")
			}
			var timedText string
			mp3Path, timedText, err = buildOpenAITimedDubbingTrack(transcript, sourcePath, workDir, srcLang, resLang)
			if err != nil {
				if debugTriggerLogEnabled {
					log.Printf("gpt timed dubbing failed chat=%d replyTo=%d err=%v", task.ChatID, task.ReplyTo, err)
				}
				reply(sendCtx, gptTranslateUserErrorMessage(err), false)
				return
			}
			if tmpText, e := os.CreateTemp(workDir, "openai_translate_text_*.txt"); e == nil {
				_, _ = tmpText.WriteString(strings.TrimSpace(timedText) + "\n")
				_ = tmpText.Close()
				saveCacheFile(voiceTextCachePath(cacheKey), tmpText.Name())
			}
			copyOpenAITranslatedAudioToCache(cacheKey, mp3Path)
		}
	} else {
		if progress != nil {
			progress.SetFrame(3)
			progress.SetStage("Распознавание OpenAI")
		}
		transcript, err := runOpenAITranscriptionLocal(sourcePath, workDir, srcLang)
		if err != nil {
			if debugTriggerLogEnabled {
				log.Printf("gpt translate transcription failed chat=%d replyTo=%d err=%v", task.ChatID, task.ReplyTo, err)
			}
			reply(sendCtx, gptTranslateUserErrorMessage(err), false)
			return
		}
		if progress != nil {
			progress.SetFrame(4)
			progress.SetStage("Перевод GPT")
		}
		if task.Action == voiceTranslateActionText {
			translated, err := translateTranscriptTextWithOpenAI(transcript.Text, srcLang, resLang, false)
			if err != nil {
				if debugTriggerLogEnabled {
					log.Printf("gpt translate text failed chat=%d replyTo=%d err=%v", task.ChatID, task.ReplyTo, err)
				}
				reply(sendCtx, gptTranslateUserErrorMessage(err), false)
				return
			}
			tmp, e := os.CreateTemp(workDir, "openai_translate_text_*.txt")
			if e != nil {
				reply(sendCtx, "Не удалось подготовить текстовый файл GPT-перевода.", false)
				return
			}
			_, _ = tmp.WriteString(strings.TrimSpace(translated) + "\n")
			_ = tmp.Close()
			saveCacheFile(voiceTextCachePath(cacheKey), tmp.Name())
			if progress != nil {
				progress.SetFrame(8)
				progress.SetStage("Отправка результата")
			}
			if err := sendDocumentFromFileNamed(sendCtx, tmp.Name(), voiceOutName(cacheKey, mediaInfo, ".txt"), ""); err != nil {
				reply(sendCtx, "Не удалось отправить текстовый файл GPT-перевода.", false)
			}
			return
		}
		translations, err := translateTranscriptSegmentsWithOpenAI(transcript.Segments, srcLang, resLang)
		if err != nil {
			if debugTriggerLogEnabled {
				log.Printf("gpt translate subtitles failed chat=%d replyTo=%d err=%v", task.ChatID, task.ReplyTo, err)
			}
			reply(sendCtx, gptTranslateUserErrorMessage(err), false)
			return
		}
		srtPath := filepath.Join(workDir, "openai_translate.srt")
		if err := writeOpenAISRT(srtPath, transcript.Segments, translations); err != nil {
			reply(sendCtx, "Не удалось собрать SRT файл GPT-перевода.", false)
			return
		}
		saveCacheFile(voiceSubtitlesCachePath(cacheKey), srtPath)
		if progress != nil {
			progress.SetFrame(8)
			progress.SetStage("Отправка результата")
		}
		if err := sendDocumentFromFileNamed(sendCtx, srtPath, voiceOutName(cacheKey, mediaInfo, ".srt"), ""); err != nil {
			reply(sendCtx, "Не удалось отправить SRT файл GPT-перевода.", false)
		}
		return
	}

	if debugTriggerLogEnabled {
		log.Printf("gpt translate success chat=%d replyTo=%d", task.ChatID, task.ReplyTo)
	}
	if task.Action == voiceTranslateActionAudio {
		if progress != nil {
			progress.SetFrame(8)
			progress.SetStage("Отправка результата")
		}
		if err := sendAudioFromFileNamed(sendCtx, mp3Path, voiceOutName(cacheKey, mediaInfo, ".mp3"), "", ""); err != nil {
			reply(sendCtx, "Не удалось отправить GPT-озвучку.", false)
		}
		return
	}
	if progress != nil {
		progress.SetFrame(7)
		if task.Action == voiceTranslateActionVideo {
			progress.SetStage("Микширование видео")
		} else {
			progress.SetStage("Микширование аудио")
		}
	}
	makeVideoMix := task.Action == voiceTranslateActionVideo && mediaInfo.HasVideo
	mixedPath, err := mixTranslatedAudioWithSource(sourcePath, mp3Path, makeVideoMix)
	if err != nil {
		if debugTriggerLogEnabled {
			log.Printf("gpt translate mix failed chat=%d replyTo=%d err=%v source=%s translated=%s", task.ChatID, task.ReplyTo, err, sourcePath, mp3Path)
		}
		reply(sendCtx, "Не удалось собрать финальный файл с GPT-переводом.", false)
		return
	}
	defer os.Remove(mixedPath)
	if progress != nil {
		progress.SetFrame(8)
		progress.SetStage("Отправка результата")
	}
	if makeVideoMix {
		if err := sendVideoFromFileNamed(sendCtx, mixedPath, voiceOutName(cacheKey, mediaInfo, ".mp4"), voiceTranslateMixCaption); err != nil {
			reply(sendCtx, "Не удалось отправить GPT-видеомикс.", false)
		}
		return
	}
	if err := sendAudioFromFileNamedCaption(sendCtx, mixedPath, voiceOutName(cacheKey, mediaInfo, ".mp3"), "", "", voiceTranslateMixCaption); err != nil {
		reply(sendCtx, "Не удалось отправить GPT-аудиомикс.", false)
	}
}

func gptTranslateUserErrorMessage(err error) string {
	msg := "Не удалось выполнить GPT-перевод."
	if err == nil {
		return msg
	}
	errText := strings.ToLower(err.Error())
	switch {
	case strings.Contains(errText, "openai_api_key"):
		return "OpenAI API key не настроен для GPT-перевода."
	case strings.Contains(errText, "empty text") || strings.Contains(errText, "no subtitle segments"):
		return "OpenAI не нашёл распознаваемую речь в файле."
	case strings.Contains(errText, "too large") || strings.Contains(errText, "maximum context"):
		return "Файл или расшифровка слишком большие для GPT-перевода."
	case strings.Contains(errText, "timeout") || strings.Contains(errText, "deadline exceeded"):
		return "GPT-перевод занял слишком много времени. Можно попробовать файл короче."
	case strings.Contains(errText, "openai transcription status"):
		return "OpenAI не смог распознать речь в этом файле."
	case strings.Contains(errText, "openai chat status"):
		return "OpenAI не смог перевести расшифровку."
	case strings.Contains(errText, "openai tts status"):
		return "OpenAI не смог озвучить перевод."
	default:
		return msg
	}
}
