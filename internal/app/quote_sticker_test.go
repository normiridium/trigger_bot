package app

import (
	"encoding/json"
	"strconv"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestParseQuoteStickerCountArg(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 1},
		{"4", 4},
		{"0", 1},
		{"-2", 1},
		{"abc", 1},
		{"100", 20},
	}
	for _, tc := range cases {
		if got := parseQuoteStickerCountArg(tc.in); got != tc.want {
			t.Fatalf("parseQuoteStickerCountArg(%q)=%d want=%d", tc.in, got, tc.want)
		}
	}
}

func TestBuildQuoteStickerPickerKeyboard_SaveRequiresEmoji(t *testing.T) {
	st := quoteStickerSession{ID: "s1", Page: 0, Emoji: ""}
	kb := buildQuoteStickerPickerKeyboard(st)
	if len(kb.InlineKeyboard) == 0 {
		t.Fatalf("empty keyboard")
	}
	last := kb.InlineKeyboard[len(kb.InlineKeyboard)-1]
	if len(last) != 1 {
		t.Fatalf("expected only cancel button without emoji, got %d", len(last))
	}
	if last[0].Text != "Отмена" {
		t.Fatalf("unexpected last button: %q", last[0].Text)
	}

	st.Emoji = "🙂"
	kb = buildQuoteStickerPickerKeyboard(st)
	last = kb.InlineKeyboard[len(kb.InlineKeyboard)-1]
	if len(last) != 2 {
		t.Fatalf("expected save+cancel buttons with emoji, got %d", len(last))
	}
	if last[0].Text != "Сохранить" || last[1].Text != "Отмена" {
		t.Fatalf("unexpected buttons: %q / %q", last[0].Text, last[1].Text)
	}
}

func TestBuildQuoteStickerPickerKeyboard_HasExpectedPages(t *testing.T) {
	st := quoteStickerSession{ID: "s1", Page: 0, Emoji: ""}
	kb := buildQuoteStickerPickerKeyboard(st)
	if len(kb.InlineKeyboard) < 2 {
		t.Fatalf("unexpected keyboard size")
	}
	nav := kb.InlineKeyboard[len(kb.InlineKeyboard)-2]
	if len(nav) != 3 {
		t.Fatalf("unexpected nav row size: %d", len(nav))
	}
	totalPages := (len(effectiveQuoteStickerEmojiOptions()) + quoteStickerEmojiPerPage - 1) / quoteStickerEmojiPerPage
	want := "1/" + strconv.Itoa(totalPages)
	if nav[1].Text != want {
		t.Fatalf("unexpected page marker: %q", nav[1].Text)
	}
}

func TestBuildQuoteAPIMediaField_GIFFrameFallbackPrefersURL(t *testing.T) {
	media := &quoteMediaPayload{
		FileID: "CgACAgQAAyEFAASK3Py4AAEDTCtp8TIynLalc1h2mlkWqnkKkb1QOAAC_AIAAmkIDVPQOH2oWxvBpDsE",
		URL:    "https://api.telegram.org/file/bot123:token/documents/file_1.png",
	}
	got := buildQuoteAPIMediaField(media)
	obj, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("expected media object, got %T", got)
	}
	urlRaw, ok := obj["url"]
	if !ok {
		t.Fatalf("expected url key in media object")
	}
	url, ok := urlRaw.(string)
	if !ok || url == "" {
		t.Fatalf("expected non-empty url string, got %#v", urlRaw)
	}
	if _, exists := obj["file_id"]; exists {
		t.Fatalf("did not expect file_id when url is present")
	}

	blob, err := json.Marshal(struct {
		Media any `json:"media,omitempty"`
	}{Media: got})
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(blob, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	mediaDecoded, ok := decoded["media"].(map[string]any)
	if !ok {
		t.Fatalf("expected encoded media object, got %#v", decoded["media"])
	}
	if _, ok := mediaDecoded["url"]; !ok {
		t.Fatalf("encoded media has no url: %#v", mediaDecoded)
	}
	if _, ok := mediaDecoded["file_id"]; ok {
		t.Fatalf("encoded media unexpectedly has file_id: %#v", mediaDecoded)
	}
}

func TestBuildQuoteMediaPayload_StickerFileIDFallback(t *testing.T) {
	msg := &tgbotapi.Message{
		Sticker: &tgbotapi.Sticker{
			FileID: "sticker-file-id",
			Width:  512,
			Height: 512,
		},
	}
	media := buildQuoteMediaPayload(nil, nil, msg)
	if media == nil {
		t.Fatalf("expected non-nil media payload for sticker")
	}
	if media.FileID != "sticker-file-id" {
		t.Fatalf("unexpected media file_id: %q", media.FileID)
	}
}
