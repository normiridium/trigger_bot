package app

import (
	"context"
	"log"
	"strings"
	"time"
)

// articleFallbackResult — что стало с ссылкой, которую не удалось скачать как медиа.
type articleFallbackResult int

const (
	// articleFallbackNotArticle — это не статья (или текста не достали): пусть
	// вызывающий код решает, показывать ли ошибку.
	articleFallbackNotArticle articleFallbackResult = iota
	// articleFallbackPublished — текст статьи опубликован в чат.
	articleFallbackPublished
	// articleFallbackSkipped — статья есть, но публиковать нечего (слишком
	// длинная или отправка не удалась): молчим, в чат ничего не пишем.
	articleFallbackSkipped
)

// publishArticleFallback пробует отдать ссылку как статью: если со страницы
// вытащили достаточно текста и он влезает в лимит rich-сообщения (32768
// символов), публикуем как статью; если не влезает — не трогаем публикацию.
//
// Зачем: раньше любая не-медиа ссылка (например, пост на Reddit) заканчивалась
// сообщением «ошибка скачивания файла». Для читаемых страниц это бессмысленно.
func publishArticleFallback(sendCtx sendContext, rawURL string) articleFallbackResult {
	if !envBool("ARTICLE_FALLBACK_ENABLED", true) {
		return articleFallbackNotArticle
	}
	if sendCtx.Bot == nil || strings.TrimSpace(rawURL) == "" {
		return articleFallbackNotArticle
	}
	minChars := envInt("ARTICLE_FALLBACK_MIN_CHARS", 400)
	if minChars <= 0 {
		minChars = 400
	}
	timeoutSec := envInt("ARTICLE_FALLBACK_TIMEOUT_SEC", 25)
	if timeoutSec < 5 {
		timeoutSec = 5
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	title, text, _, err := fetchLinkContextParts(ctx, rawURL, telegramRichMessageTextLimit)
	if err != nil && debugTriggerLogEnabled {
		log.Printf("article fallback: страница не читается url=%q err=%v", clipText(rawURL, 180), err)
	}
	text = strings.TrimSpace(text)
	if len([]rune(text)) < minChars {
		if debugTriggerLogEnabled {
			log.Printf("article fallback: текста не хватило url=%q runes=%d min=%d", clipText(rawURL, 180), len([]rune(text)), minChars)
		}
		return articleFallbackNotArticle
	}

	markdown := buildArticleMarkdown(title, text, rawURL)
	if len([]rune(markdown)) > telegramRichMessageTextLimit {
		log.Printf("article fallback: статья не влезает в лимит url=%q runes=%d limit=%d — публикацию не трогаем",
			clipText(rawURL, 180), len([]rune(markdown)), telegramRichMessageTextLimit)
		return articleFallbackSkipped
	}

	if !sendRichMarkdownArticle(sendCtx, markdown, false) {
		log.Printf("article fallback: отправка статьи не удалась url=%q runes=%d", clipText(rawURL, 180), len([]rune(markdown)))
		return articleFallbackSkipped
	}
	log.Printf("article fallback: опубликована статья вместо файла url=%q runes=%d", clipText(rawURL, 180), len([]rune(markdown)))
	return articleFallbackPublished
}

// buildArticleMarkdown собирает текст статьи: заголовок жирным, абзацы через
// пустую строку, источник — последней строкой.
func buildArticleMarkdown(title, text, rawURL string) string {
	var b strings.Builder
	if heading := strings.TrimSpace(title); heading != "" {
		b.WriteString("**")
		b.WriteString(heading)
		b.WriteString("**\n\n")
	}
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	for _, paragraph := range strings.Split(normalized, "\n") {
		line := strings.TrimSpace(paragraph)
		if line == "" {
			continue
		}
		b.WriteString(line)
		b.WriteString("\n\n")
	}
	if source := strings.TrimSpace(rawURL); source != "" {
		b.WriteString("Источник: ")
		b.WriteString(source)
	}
	return strings.TrimSpace(b.String())
}
