package app

import (
	"fmt"
	"html"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type messageFileID struct {
	Kind   string
	FileID string
	Extra  string
}

func extractMessageFileIDs(msg *tgbotapi.Message) []messageFileID {
	if msg == nil {
		return nil
	}
	out := make([]messageFileID, 0, 4)
	add := func(kind, fileID, extra string) {
		fileID = strings.TrimSpace(fileID)
		if fileID == "" {
			return
		}
		out = append(out, messageFileID{Kind: kind, FileID: fileID, Extra: strings.TrimSpace(extra)})
	}
	if msg.Voice != nil {
		add("voice", msg.Voice.FileID, fmt.Sprintf("%ds", msg.Voice.Duration))
	}
	if msg.Audio != nil {
		extra := strings.TrimSpace(strings.Join([]string{msg.Audio.Performer, msg.Audio.Title}, " - "))
		add("audio", msg.Audio.FileID, extra)
	}
	if msg.Document != nil {
		add("document", msg.Document.FileID, msg.Document.FileName)
	}
	if msg.Video != nil {
		add("video", msg.Video.FileID, fmt.Sprintf("%ds", msg.Video.Duration))
	}
	if msg.Animation != nil {
		add("animation", msg.Animation.FileID, msg.Animation.FileName)
	}
	if msg.VideoNote != nil {
		add("video_note", msg.VideoNote.FileID, fmt.Sprintf("%ds", msg.VideoNote.Duration))
	}
	if msg.Sticker != nil {
		add("sticker", msg.Sticker.FileID, msg.Sticker.SetName)
	}
	if len(msg.Photo) > 0 {
		best := msg.Photo[0]
		for _, p := range msg.Photo[1:] {
			if p.Width*p.Height > best.Width*best.Height {
				best = p
			}
		}
		add("photo", best.FileID, fmt.Sprintf("%dx%d", best.Width, best.Height))
	}
	return out
}

func extractPrivateAutoFileIDs(msg *tgbotapi.Message) []messageFileID {
	items := extractMessageFileIDs(msg)
	if len(items) == 0 {
		return nil
	}
	out := make([]messageFileID, 0, len(items))
	for _, it := range items {
		switch it.Kind {
		case "sticker", "animation":
			// Private chat already has specialized helpers for stickers/GIFs.
			continue
		default:
			out = append(out, it)
		}
	}
	return out
}

func buildFileIDReply(items []messageFileID) string {
	if len(items) == 0 {
		return ""
	}
	lines := []string{"file_id:"}
	for _, it := range items {
		label := it.Kind
		if it.Extra != "" {
			label += " · " + it.Extra
		}
		lines = append(lines,
			"<b>"+html.EscapeString(label)+"</b>",
			"<code>"+html.EscapeString(it.FileID)+"</code>",
		)
	}
	return strings.Join(lines, "\n")
}
