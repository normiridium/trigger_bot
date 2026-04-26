package app

import "strings"

var allowedReactionEmojis = []string{
	"❤", "👍", "👎", "🔥", "🥰", "👏", "😁", "🤔", "🤯", "😱", "🤬", "😢", "🎉", "🤩", "🤮", "💩",
	"🙏", "👌", "🕊", "🤡", "🥱", "🥴", "😍", "🐳", "❤‍🔥", "🌚", "🌭", "💯", "🤣", "⚡", "🍌", "🏆",
	"💔", "🤨", "😐", "🍓", "🍾", "💋", "🖕", "😈", "😴", "😭", "🤓", "👻", "👨‍💻", "👀", "🎃", "🙈",
	"😇", "😨", "🤝", "✍", "🤗", "🫡", "🎅", "🎄", "☃", "💅", "🤪", "🗿", "🆒", "💘", "🙉", "🦄",
	"😘", "💊", "🙊", "😎", "👾", "🤷‍♂", "🤷", "🤷‍♀", "😡",
}

var allowedReactionEmojiByKey = buildAllowedReactionEmojiByKey()

var reactionEmojiAlias = buildReactionEmojiAlias()

func buildAllowedReactionEmojiByKey() map[string]string {
	m := make(map[string]string, len(allowedReactionEmojis))
	for _, e := range allowedReactionEmojis {
		m[normalizeReactionEmojiKey(e)] = e
	}
	return m
}

func buildReactionEmojiAlias() map[string]string {
	m := make(map[string]string, 512)
	add := func(target string, emojis ...string) {
		for _, e := range emojis {
			key := normalizeReactionEmojiKey(e)
			if key == "" {
				continue
			}
			m[key] = target
		}
	}

	// Love / hearts.
	add("❤",
		"❤️", "♥", "💟", "💕", "💞", "💓", "💗", "💖", "💝", "🩷", "🧡", "💛", "💚", "💙", "🩵", "💜", "🤍", "🤎", "🖤",
		"❣", "💌", "🫶", "💑", "👩‍❤️‍👨", "👨‍❤️‍👨", "👩‍❤️‍👩",
	)
	add("💘", "💋", "😘", "😗", "😙", "😚", "😽", "😻")
	add("💋", "👄", "🫦")
	add("💔", "🩹", "😿", "💔")
	add("❤‍🔥", "❤️‍🔥", "🔥❤️", "❤️🔥")

	// Positive faces.
	add("😁",
		"😀", "😃", "😄", "😅", "😆", "🙂", "🙃", "☺", "😬", "😸", "😺", "😹", "😼",
	)
	add("🥰", "😊", "😌", "😋", "🤭", "🫠", "😇")
	add("😍", "🤩", "🥹", "🤤")
	add("😎", "😏", "🤠", "🕶")
	add("🤪", "😛", "😜", "😝", "😹", "🤭")
	add("😐", "😶", "😑", "🫥", "🫤")
	add("🤨", "😒", "🙄", "🧐", "🤨")
	add("🤔", "🤫", "🫡", "🧠", "🧩", "❓", "❔")
	add("🤯", "😵", "😵‍💫", "🫨", "🤯")
	add("😱", "😮", "😯", "😲", "😳", "🫢")
	add("😨", "😦", "😧", "😰", "😱", "😖")
	add("😢", "🥺", "😥", "😓", "😞", "😟", "🙁", "☹", "😣", "😔", "😿")
	add("😭", "😩", "😫", "😭")
	add("😴", "😪", "🥱", "🛌")
	add("😡", "😠", "😤", "😾")
	add("🤬", "🤐", "💢")
	add("🤮", "🤢", "🤧", "🤒", "🤕", "🫢")
	add("😈", "👿", "😈")
	add("🤓", "🧑‍🏫", "📚")
	add("🤡", "🤥", "🎭", "🤹")
	add("👻", "💀", "☠", "🧟", "🧛", "🕸", "🕷")
	add("🎃", "👹", "👺", "🦇")
	add("🙈", "🫣")
	add("🙉", "🙉")
	add("🙊", "🙊")

	// Gestures.
	add("👍", "👍", "🫡", "✅", "☑", "✔", "🆗", "👌", "🙆", "🙆‍♂", "🙆‍♀")
	add("👎", "👎", "❌", "⛔", "🚫", "🙅", "🙅‍♂", "🙅‍♀")
	add("👏", "👏", "🙌", "🫸", "🫷")
	add("🙏", "🙏", "🤲", "🛐")
	add("👌", "👌", "🤌", "🫰", "👌🏻", "👌🏼", "👌🏽", "👌🏾", "👌🏿")
	add("🤝", "🤝", "🤜", "🤛", "🤞", "🫱", "🫲")
	add("✍", "✍", "📝", "🖊", "🖋")
	add("🫡", "🫡")
	add("🖕", "🖕")
	add("💅", "💅")
	add("🤷", "🤷", "🤷‍♂", "🤷‍♀", "🤦", "🤦‍♂", "🤦‍♀")

	// Celebration / approval / hype.
	add("🔥", "🔥", "⚡", "✨", "💥", "🌟", "⭐", "🌠")
	add("🎉", "🎉", "🥳", "🎊", "🎈", "🪅", "🪩")
	add("🏆", "🏆", "🥇", "🥈", "🥉", "🎖", "🏅", "🐐")
	add("💯", "💯", "100", "🔟")
	add("🍾", "🍾", "🥂", "🍷", "🍸", "🍹", "🍺", "🍻")
	add("🍓", "🍒", "🍑", "🍉", "🍇", "🍎", "🍏", "🥝", "🍍", "🍊", "🍋", "🍓")
	add("🍌", "🍌")
	add("🌭", "🌭", "🍔", "🍕", "🍟", "🌮", "🌯", "🍣", "🍜", "🍱", "🍿", "🥨", "🥐")

	// Symbolic / neutral objects.
	add("🗿", "🗿", "🪨", "🧱")
	add("🆒", "🆒", "🆕", "🆙", "🆒")
	add("💩", "💩")
	add("🕊", "🕊", "☮", "🫂")
	add("🐳", "🐋", "🐳", "🐬", "🐟")
	add("👀", "👀", "🫣")
	add("👾", "👾", "🎮", "🕹")
	add("👨‍💻", "👨‍💻", "👩‍💻", "🧑‍💻", "💻", "🖥", "⌨", "🖱")
	add("☃", "☃", "⛄", "❄", "🌨")
	add("🎄", "🎄", "🎋")
	add("🎅", "🎅", "🤶", "🧑‍🎄")
	add("🦄", "🦄", "🦌", "🦋", "🐴", "🦓", "🦙")
	add("💊", "💊", "💉", "🩹", "🧪", "🩺")

	// If GPT emits one of the existing allowed reactions in variant forms.
	add("❤", "❤")
	add("⚡", "⚡")
	add("🤣", "😂", "🤣", "😆")
	add("😇", "😇")

	return m
}

func normalizeReactionEmojiKey(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == 0xFE0F || r == 0xFE0E:
			continue
		case r >= 0x1F3FB && r <= 0x1F3FF:
			continue
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

func convertToAllowedReactionEmoji(raw string) (string, bool, string) {
	key := normalizeReactionEmojiKey(raw)
	if key == "" {
		return "", false, "empty"
	}
	if direct, ok := allowedReactionEmojiByKey[key]; ok {
		return direct, true, "direct"
	}
	aliasRaw, ok := reactionEmojiAlias[key]
	if !ok {
		return "", false, "no_rule"
	}
	aliasKey := normalizeReactionEmojiKey(aliasRaw)
	if aliasKey == "" {
		return "", false, "bad_rule"
	}
	mapped, ok := allowedReactionEmojiByKey[aliasKey]
	if !ok {
		return "", false, "bad_rule"
	}
	return mapped, true, "alias"
}
