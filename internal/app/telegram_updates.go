package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf16"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type rawMessageEntity struct {
	Type          string `json:"type"`
	CustomEmojiID string `json:"custom_emoji_id"`
	Offset        int    `json:"offset"`
	Length        int    `json:"length"`
}

type rawMessageWithEmoji struct {
	Entities        []rawMessageEntity   `json:"entities"`
	CaptionEntities []rawMessageEntity   `json:"caption_entities"`
	Text            string               `json:"text"`
	Caption         string               `json:"caption"`
	ReplyToMessage  *rawMessageWithEmoji `json:"reply_to_message"`
}

type rawUpdateWithEmoji struct {
	Message      *rawMessageWithEmoji  `json:"message"`
	ChatMember   *rawChatMemberUpdated `json:"chat_member"`
	MyChatMember *rawChatMemberUpdated `json:"my_chat_member"`
}

type updateWithEmojiMeta struct {
	Update          tgbotapi.Update
	RawMessage      *rawMessageWithEmoji
	RawChatMember   *rawChatMemberUpdated
	RawMyChatMember *rawChatMemberUpdated
}

type rawChatMemberUpdated struct {
	Chat          *rawChat       `json:"chat"`
	From          *rawUser       `json:"from"`
	Date          int64          `json:"date"`
	OldChatMember *rawChatMember `json:"old_chat_member"`
	NewChatMember *rawChatMember `json:"new_chat_member"`
}

type rawChat struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
}

type rawUser struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	UserName  string `json:"username"`
}

type rawChatMember struct {
	User   *rawUser `json:"user"`
	Status string   `json:"status"`
}

type customEmojiHit struct {
	ID       string
	Fallback string
}

type stickerCodeHit struct {
	FileID string
	SetID  string
}

type animationCodeHit struct {
	FileID  string
	Caption string
}

func getUpdatesChanWithEmojiMeta(bot *tgbotapi.BotAPI, config tgbotapi.UpdateConfig) <-chan updateWithEmojiMeta {
	ch := make(chan updateWithEmojiMeta, 100)
	go func() {
		for {
			items, err := getUpdatesWithEmojiMeta(bot, config)
			if err != nil {
				log.Println(err)
				log.Println("Failed to get updates, retrying in 3 seconds...")
				time.Sleep(3 * time.Second)
				continue
			}
			for _, item := range items {
				if item.Update.UpdateID >= config.Offset {
					config.Offset = item.Update.UpdateID + 1
					ch <- item
				}
			}
		}
	}()
	return ch
}

func getUpdatesWithEmojiMeta(bot *tgbotapi.BotAPI, config tgbotapi.UpdateConfig) ([]updateWithEmojiMeta, error) {
	resp, err := bot.Request(config)
	if err != nil {
		return nil, err
	}
	if !resp.Ok {
		return nil, errors.New(resp.Description)
	}
	var rawItems []json.RawMessage
	if err := json.Unmarshal(resp.Result, &rawItems); err != nil {
		return nil, err
	}
	out := make([]updateWithEmojiMeta, 0, len(rawItems))
	for _, rawItem := range rawItems {
		var upd tgbotapi.Update
		if err := json.Unmarshal(rawItem, &upd); err != nil {
			return nil, err
		}
		var rawUpd rawUpdateWithEmoji
		if err := json.Unmarshal(rawItem, &rawUpd); err != nil {
			return nil, err
		}
		out = append(out, updateWithEmojiMeta{
			Update:          upd,
			RawMessage:      rawUpd.Message,
			RawChatMember:   rawUpd.ChatMember,
			RawMyChatMember: rawUpd.MyChatMember,
		})
	}
	return out, nil
}

func extractCustomEmojiFromRaw(rawMsg *rawMessageWithEmoji) ([]customEmojiHit, int) {
	if rawMsg == nil {
		return nil, 0
	}
	out := make([]customEmojiHit, 0, 4)
	seen := make(map[string]int)
	count := 0
	push := func(e rawMessageEntity, srcText string) {
		if e.Type != "custom_emoji" {
			return
		}
		count++
		id := strings.TrimSpace(e.CustomEmojiID)
		if id == "" {
			return
		}
		fallback := strings.TrimSpace(sliceUTF16ByEntity(srcText, e.Offset, e.Length))
		if idx, ok := seen[id]; ok {
			if out[idx].Fallback == "" && fallback != "" {
				out[idx].Fallback = fallback
			}
			return
		}
		seen[id] = len(out)
		out = append(out, customEmojiHit{
			ID:       id,
			Fallback: fallback,
		})
	}
	for _, e := range rawMsg.Entities {
		push(e, rawMsg.Text)
	}
	for _, e := range rawMsg.CaptionEntities {
		push(e, rawMsg.Caption)
	}
	return out, count
}

func sliceUTF16ByEntity(s string, offsetCU, lengthCU int) string {
	if strings.TrimSpace(s) == "" || offsetCU < 0 || lengthCU <= 0 {
		return ""
	}
	endCU := offsetCU + lengthCU
	if endCU <= offsetCU {
		return ""
	}
	var b strings.Builder
	cu := 0
	for _, r := range s {
		n := utf16.RuneLen(r)
		if n < 1 {
			n = 1
		}
		nextCU := cu + n
		if nextCU <= offsetCU {
			cu = nextCU
			continue
		}
		if cu >= endCU {
			break
		}
		b.WriteRune(r)
		cu = nextCU
	}
	return b.String()
}

func buildTGEmojiSnippet(id, fallback string) string {
	id = strings.TrimSpace(id)
	fallback = strings.TrimSpace(fallback)
	if fallback == "" {
		fallback = "🙂"
	}
	return fmt.Sprintf(`<tg-emoji emoji-id="%s">%s</tg-emoji>`, id, fallback)
}

func extractStickerCode(msg *tgbotapi.Message) (stickerCodeHit, bool) {
	if msg == nil || msg.Sticker == nil {
		return stickerCodeHit{}, false
	}
	fileID := strings.TrimSpace(msg.Sticker.FileID)
	if fileID == "" {
		return stickerCodeHit{}, false
	}
	return stickerCodeHit{
		FileID: fileID,
		SetID:  strings.TrimSpace(msg.Sticker.SetName),
	}, true
}

func buildStickerPairCode(hit stickerCodeHit) string {
	return strings.TrimSpace(hit.FileID) + ":" + strings.TrimSpace(hit.SetID)
}

func extractAnimationCode(msg *tgbotapi.Message) (animationCodeHit, bool) {
	if msg == nil || msg.Animation == nil {
		return animationCodeHit{}, false
	}
	fileID := strings.TrimSpace(msg.Animation.FileID)
	if fileID == "" {
		return animationCodeHit{}, false
	}
	return animationCodeHit{
		FileID:  fileID,
		Caption: strings.TrimSpace(msg.Caption),
	}, true
}

func buildAnimationReplyText(hit animationCodeHit) string {
	return strings.TrimSpace(hit.FileID) + "\n" + strings.TrimSpace(hit.Caption)
}
