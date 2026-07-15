package app

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/draw"
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

func TestRequestQuoteStickerPNGUsesLocalRendererWhenEndpointIsEmpty(t *testing.T) {
	t.Setenv("QUOTE_API_URI", "")
	got, err := requestQuoteStickerPNG(nil, []quoteHistoryItem{
		{
			Msg: &tgbotapi.Message{
				From: &tgbotapi.User{ID: 123, FirstName: "Test"},
				Text: "hello",
			},
		},
	})
	if err != nil {
		t.Fatalf("local quote render failed: %v", err)
	}
	img, format, err := image.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("decode local quote png: %v", err)
	}
	if format != "png" {
		t.Fatalf("unexpected format: %q", format)
	}
	if img.Bounds().Dx() != 512 || img.Bounds().Dy() != 512 {
		t.Fatalf("unexpected sticker size: %v", img.Bounds())
	}
}

func TestRenderLocalQuoteStickerPNGDrawsAvatar(t *testing.T) {
	avatar := image.NewRGBA(image.Rect(0, 0, 40, 40))
	draw.Draw(avatar, avatar.Bounds(), image.NewUniform(color.RGBA{R: 230, G: 40, B: 60, A: 255}), image.Point{}, draw.Src)
	got, err := renderLocalQuoteStickerPNG([]quoteAPIMessage{
		{
			From: quoteAPIFrom{
				Name:        "Avatar User",
				AvatarImage: avatar,
			},
			Text:   "hello",
			Avatar: true,
		},
	})
	if err != nil {
		t.Fatalf("local quote render failed: %v", err)
	}
	img, format, err := image.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("decode local quote png: %v", err)
	}
	if format != "png" {
		t.Fatalf("unexpected format: %q", format)
	}
	r, g, b, a := img.At(42, 57).RGBA()
	if a == 0 || r <= g || r <= b {
		t.Fatalf("avatar sample does not look red: rgba=(%d,%d,%d,%d)", r, g, b, a)
	}
}

func TestRenderLocalQuoteStickerPNGDrawsMediaPreview(t *testing.T) {
	preview := image.NewRGBA(image.Rect(0, 0, 80, 60))
	draw.Draw(preview, preview.Bounds(), image.NewUniform(color.RGBA{R: 20, G: 190, B: 80, A: 255}), image.Point{}, draw.Src)
	got, err := renderLocalQuoteStickerPNG([]quoteAPIMessage{
		{
			From: quoteAPIFrom{Name: "Media User"},
			Local: &quoteLocalMedia{
				Kind:    "photo",
				Preview: preview,
			},
			Reacts: []reactionDisplay{{Emoji: "👍", Count: 3}},
		},
	})
	if err != nil {
		t.Fatalf("local quote render failed: %v", err)
	}
	img, _, err := image.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("decode local quote png: %v", err)
	}
	r, g, b, a := img.At(100, 100).RGBA()
	if a == 0 || g <= r || g <= b {
		t.Fatalf("media sample does not look green: rgba=(%d,%d,%d,%d)", r, g, b, a)
	}
}

func TestRenderLocalQuoteStickerPNGDrawsCustomEmojiEntity(t *testing.T) {
	emoji := image.NewRGBA(image.Rect(0, 0, 32, 32))
	draw.Draw(emoji, emoji.Bounds(), image.NewUniform(color.RGBA{R: 20, G: 230, B: 40, A: 255}), image.Point{}, draw.Src)
	got, err := renderLocalQuoteStickerPNG([]quoteAPIMessage{
		{
			From: quoteAPIFrom{Name: "Emoji User"},
			Text: "A 🎉 B",
			Entities: []quoteAPIEntity{{
				Type:          "custom_emoji",
				Offset:        2,
				Length:        2,
				CustomEmojiID: "123",
				Fallback:      "🎉",
				Image:         emoji,
			}},
		},
	})
	if err != nil {
		t.Fatalf("local quote render failed: %v", err)
	}
	img, _, err := image.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("decode local quote png: %v", err)
	}
	greenPixels := 0
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			if a > 0x8000 && g > 0x9000 && r < 0x4000 && b < 0x5000 {
				greenPixels++
			}
		}
	}
	if greenPixels < 80 {
		t.Fatalf("custom emoji pixels were not rendered, green_pixels=%d", greenPixels)
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
