package mediadl

import "testing"

func TestNormalizeRedditURLs(t *testing.T) {
	cases := []struct {
		raw string
		ok  bool
	}{
		{"https://www.reddit.com/r/XFiles/comments/1lp1gpp/david/?tl=ru", true},
		{"https://old.reddit.com/r/XFiles/comments/1lp1gpp/", true},
		{"https://redd.it/1lp1gpp", true},
		{"https://i.redd.it/ckif37erj9af1.jpeg", true},
		{"https://v.redd.it/a08z8ef2nyzh1", true},
		{"https://www.reddit.com/gallery/1lp1gpp", true},
		{"https://example.com/watch?v=1", false},
	}
	for _, tc := range cases {
		_, service, ok := NormalizeSupportedURL(tc.raw)
		if ok != tc.ok {
			t.Fatalf("%s: распознано=%v, ожидали %v", tc.raw, ok, tc.ok)
		}
		if ok && service != ServiceReddit {
			t.Fatalf("%s: сервис %q вместо reddit", tc.raw, service)
		}
	}
}

func TestRedditChoiceKeyboardOffersPhotoAndVideo(t *testing.T) {
	req := ChoiceRequest{URL: "https://www.reddit.com/r/XFiles/comments/1lp1gpp/david/"}
	keyboard := BuildChoiceKeyboard(nil, req)
	var texts []string
	for _, row := range keyboard.InlineKeyboard {
		for _, button := range row {
			texts = append(texts, button.Text)
		}
	}
	hasPhoto, hasVideo, hasCancel := false, false, false
	for _, text := range texts {
		switch text {
		case "Скачать картинку":
			hasPhoto = true
		case "Скачать видео":
			hasVideo = true
		case "Отменить":
			hasCancel = true
		}
	}
	if !hasPhoto || !hasVideo || !hasCancel {
		t.Fatalf("меню reddit неполное: %v", texts)
	}
}
