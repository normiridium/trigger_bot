package app

import (
	"strings"
	"testing"
)

func TestBuildArticleMarkdown(t *testing.T) {
	got := buildArticleMarkdown("Заголовок", "Первый абзац.\n\nВторой абзац.\r\n\nТретий.", "https://example.com/a")
	want := "**Заголовок**\n\nПервый абзац.\n\nВторой абзац.\n\nТретий.\n\nИсточник: https://example.com/a"
	if got != want {
		t.Fatalf("markdown не совпал\n got: %q\nwant: %q", got, want)
	}
}

func TestBuildArticleMarkdownWithoutTitle(t *testing.T) {
	got := buildArticleMarkdown("   ", "Только текст.", "")
	if got != "Только текст." {
		t.Fatalf("без заголовка и источника ожидался только текст, получено %q", got)
	}
	if strings.Contains(got, "**") || strings.Contains(got, "Источник") {
		t.Fatalf("лишние части в тексте: %q", got)
	}
}

func TestPublishArticleFallbackDisabled(t *testing.T) {
	t.Setenv("ARTICLE_FALLBACK_ENABLED", "false")
	if got := publishArticleFallback(sendContext{}, "https://example.com/article"); got != articleFallbackNotArticle {
		t.Fatalf("при выключенном флаге ожидался NotArticle, получено %v", got)
	}
}

func TestPublishArticleFallbackNeedsBot(t *testing.T) {
	t.Setenv("ARTICLE_FALLBACK_ENABLED", "true")
	if got := publishArticleFallback(sendContext{}, "https://example.com/article"); got != articleFallbackNotArticle {
		t.Fatalf("без бота публиковать нельзя, получено %v", got)
	}
}
