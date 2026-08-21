package app

import (
	"encoding/json"
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestRoleplayConfirmKeyboardUsesModernButtonFields(t *testing.T) {
	kb := roleplayConfirmKeyboard("abc")
	if len(kb.InlineKeyboard) != 1 || len(kb.InlineKeyboard[0]) != 2 {
		t.Fatalf("unexpected keyboard shape: %#v", kb.InlineKeyboard)
	}
	accept := kb.InlineKeyboard[0][0]
	if accept.IconCustomEmojiID == "" {
		t.Fatal("accept button must include custom emoji id")
	}
	if accept.Style != tgbotapi.ButtonStyleSuccess {
		t.Fatalf("unexpected accept style: %q", accept.Style)
	}
	decline := kb.InlineKeyboard[0][1]
	if decline.Style != tgbotapi.ButtonStyleDanger {
		t.Fatalf("unexpected decline style: %q", decline.Style)
	}
}

func TestRoleplayPickerDoesNotExposeUnsafeAction(t *testing.T) {
	for _, action := range roleplayActions {
		cmd := strings.ToLower(action.Command)
		if strings.Contains(cmd, "изнасил") || strings.Contains(cmd, "насил") {
			t.Fatalf("unsafe action exposed: %q", action.Command)
		}
	}
	if !containsUnsafeRoleplayAction("изнасиловать") {
		t.Fatal("unsafe action filter did not match rape wording")
	}
}

func TestRoleplayInlineResultsExposeMenu(t *testing.T) {
	t.Setenv("ROLEPLAY_INLINE_THUMB_BASE_URL", "https://bot.example.test")
	q := &tgbotapi.InlineQuery{
		ID:    "inline-1",
		Query: "",
		From:  &tgbotapi.User{ID: 123, FirstName: "Оленька"},
	}
	results := roleplayInlineResults(q, 5)
	if len(results) != 5 {
		t.Fatalf("unexpected result count: %d", len(results))
	}
	article, ok := results[0].(tgbotapi.InlineQueryResultArticle)
	if !ok {
		t.Fatalf("unexpected result type: %T", results[0])
	}
	if article.ReplyMarkup == nil {
		t.Fatal("inline result must include accept/decline keyboard")
	}
	if article.ThumbURL == "" {
		t.Fatal("inline result must include thumbnail URL")
	}
	if !strings.Contains(article.Title, roleplayActions[0].Command) {
		t.Fatalf("title does not contain action command: %q", article.Title)
	}
	data, err := json.Marshal(article)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"custom_emoji_id":"`+roleplayActions[0].EmojiID+`"`) {
		t.Fatalf("inline result json does not include custom emoji id: %s", data)
	}
}

func TestRoleplayInlineTargetFromQuery(t *testing.T) {
	q := &tgbotapi.InlineQuery{
		ID:    "inline-2",
		Query: "погладить Фрешка",
		From:  &tgbotapi.User{ID: 123, FirstName: "Оленька"},
	}
	results := roleplayInlineResults(q, 10)
	if len(results) == 0 {
		t.Fatal("expected at least one inline result")
	}
	article, ok := results[0].(tgbotapi.InlineQueryResultArticle)
	if !ok {
		t.Fatalf("unexpected result type: %T", results[0])
	}
	content, ok := article.InputMessageContent.(tgbotapi.InputTextMessageContent)
	if !ok {
		t.Fatalf("unexpected content type: %T", article.InputMessageContent)
	}
	if !strings.Contains(content.Text, "Фрешка") {
		t.Fatalf("target was not inserted into inline message: %q", content.Text)
	}
}

func TestRoleplayApplyReplyTargetForInlineDefaultTarget(t *testing.T) {
	st := roleplaySession{
		ActorID:     10,
		ActorLink:   "Оленька",
		TargetLink:  "кого-то",
		Inline:      true,
		ActionIndex: 2,
	}
	msg := &tgbotapi.Message{
		Chat: &tgbotapi.Chat{ID: -1001, Type: "supergroup"},
		From: &tgbotapi.User{ID: 10, FirstName: "Оленька"},
		ReplyToMessage: &tgbotapi.Message{
			From: &tgbotapi.User{ID: 20, FirstName: "Максим Тимченко"},
		},
	}

	got := roleplayApplyReplyTarget(nil, st, msg)
	if got.TargetID != 20 {
		t.Fatalf("reply target was not applied: %#v", got)
	}
	if plain := roleplayPlainText(got.TargetLink); plain != "Максим Тимченко" {
		t.Fatalf("unexpected target link/plain text: %q from %q", plain, got.TargetLink)
	}
	if text, _ := roleplayInlineProposalContent(got); !strings.Contains(text, "погладить → Максим Тимченко") {
		t.Fatalf("proposal must use reply target, got %q", text)
	}
}

func TestRoleplayApplyReplyTargetDoesNotOverrideExplicitTarget(t *testing.T) {
	st := roleplaySession{
		ActorID:     10,
		ActorLink:   "Оленька",
		TargetID:    30,
		TargetLink:  "Фрешка",
		Inline:      true,
		ActionIndex: 2,
	}
	msg := &tgbotapi.Message{
		Chat: &tgbotapi.Chat{ID: -1001, Type: "supergroup"},
		From: &tgbotapi.User{ID: 10, FirstName: "Оленька"},
		ReplyToMessage: &tgbotapi.Message{
			From: &tgbotapi.User{ID: 20, FirstName: "Максим Тимченко"},
		},
	}

	got := roleplayApplyReplyTarget(nil, st, msg)
	if got.TargetID != 30 || roleplayPlainText(got.TargetLink) != "Фрешка" {
		t.Fatalf("explicit target must not be overridden: %#v", got)
	}
}

func TestRoleplayInlineThumbURL(t *testing.T) {
	t.Setenv("ROLEPLAY_INLINE_THUMB_BASE_URL", "https://bot.example.test/")
	got := roleplayInlineThumbURL(roleplayActions[0])
	want := "https://bot.example.test/trigger_bot/static/roleplay/" + roleplayActions[0].EmojiID + ".jpg"
	if got != want {
		t.Fatalf("unexpected thumb url: got %q want %q", got, want)
	}
}

func TestRoleplayInlineProposalUsesCustomEmojiEntity(t *testing.T) {
	st := roleplaySession{
		ActorLink:   "Оленька",
		TargetLink:  "Фрешка",
		ActionIndex: 0,
	}
	text, entities := roleplayInlineProposalContent(st)
	if !strings.HasPrefix(text, roleplayActions[0].Emoji) {
		t.Fatalf("message must start with fallback emoji for custom entity offset, got %q", text)
	}
	if !strings.HasPrefix(text, roleplayActions[0].Emoji+" | ") {
		t.Fatalf("message must include roleplay separator after emoji, got %q", text)
	}
	if strings.Contains(text, "\n") {
		t.Fatalf("proposal must be one line, got %q", text)
	}
	if !strings.Contains(text, "хочет: "+roleplayActions[0].Command+" → Фрешка") {
		t.Fatalf("proposal must contain compact target arrow, got %q", text)
	}
	if len(entities) != 1 {
		t.Fatalf("expected one custom emoji entity, got %#v", entities)
	}
	if entities[0].Type != "custom_emoji" || entities[0].CustomEmojiID != roleplayActions[0].EmojiID {
		t.Fatalf("unexpected entity: %#v", entities[0])
	}
	if entities[0].Offset != 0 || entities[0].Length != roleplayUTF16Len(roleplayActions[0].Emoji) {
		t.Fatalf("unexpected entity range: %#v", entities[0])
	}
}

func TestRoleplayProposalTextUsesCompactTargetArrow(t *testing.T) {
	st := roleplaySession{
		ActorLink:   "Оленька",
		TargetLink:  "Ci",
		ActionIndex: 0,
	}
	text := roleplayProposalText(st)
	if strings.Contains(text, "\n") {
		t.Fatalf("proposal must be one line, got %q", text)
	}
	if strings.Contains(text, "Оленька →") {
		t.Fatalf("proposal must not repeat actor before arrow, got %q", text)
	}
	if !strings.Contains(text, "хочет: <b>обнять</b> → Ci") {
		t.Fatalf("proposal must include compact arrow target, got %q", text)
	}
}

func TestRoleplayInlineFinalUsesCustomEmojiEntity(t *testing.T) {
	st := roleplaySession{
		ActorLink:   "Оленька",
		TargetLink:  "Фрешка",
		ActionIndex: 0,
	}
	text, entities := roleplayInlineFinalContent(st, false)
	if !strings.HasPrefix(text, roleplayActions[0].Emoji) {
		t.Fatalf("message must start with fallback emoji for custom entity offset, got %q", text)
	}
	if !strings.HasPrefix(text, roleplayActions[0].Emoji+" | ") {
		t.Fatalf("message must include roleplay separator after emoji, got %q", text)
	}
	if len(entities) != 1 {
		t.Fatalf("expected one custom emoji entity, got %#v", entities)
	}
	if entities[0].CustomEmojiID != roleplayActions[0].EmojiID {
		t.Fatalf("unexpected entity: %#v", entities[0])
	}
}

func TestRoleplayInlineDeclineUsesCustomEmojiAndRoleplayText(t *testing.T) {
	st := roleplaySession{
		ActorLink:   "Оленька",
		ActorTag:    "она",
		TargetLink:  "Фрешка",
		TargetTag:   "она",
		ActionIndex: 0,
	}
	text, entities := roleplayInlineFinalContent(st, true)
	if text != roleplayDeclineAction.Emoji+" | Фрешка не хочет, чтобы Оленька обняла её" {
		t.Fatalf("unexpected decline text: %q", text)
	}
	if len(entities) != 1 || entities[0].CustomEmojiID != roleplayDeclineAction.EmojiID {
		t.Fatalf("unexpected decline entity: %#v", entities)
	}
}

func TestRoleplayDeclineTextIncludesActionTarget(t *testing.T) {
	st := roleplaySession{
		ActorLink:   "Оленька",
		ActorTag:    "она",
		TargetLink:  "Женя",
		TargetTag:   "она",
		ActionIndex: 5,
	}
	text := roleplayFinalText(st, true)
	if !strings.Contains(text, "Женя не хочет, чтобы Оленька прижала её к себе") {
		t.Fatalf("decline text must include action target, got %q", text)
	}
}

func TestRoleplayDeclineResultUsesTargetPronounCases(t *testing.T) {
	lie := roleplayAction{Command: "лечь", DeclineResult: roleplayDeclineResults["лечь"]}
	if got := roleplayDeclineResult(lie, "он", "она"); got != "лёг рядом с ней" {
		t.Fatalf("unexpected instrumental decline result: %q", got)
	}
	handshake := roleplayAction{Command: "пожать руку", DeclineResult: roleplayDeclineResults["пожать руку"]}
	if got := roleplayDeclineResult(handshake, "они", "он"); got != "крепко пожали ему руку" {
		t.Fatalf("unexpected dative decline result: %q", got)
	}
	sniff := roleplayAction{Command: "понюхать", DeclineResult: roleplayDeclineResults["понюхать"]}
	if got := roleplayDeclineResult(sniff, "она", "они"); got != "понюхала их" {
		t.Fatalf("unexpected object decline result: %q", got)
	}
}

func TestRoleplayActionsHaveDeclineResults(t *testing.T) {
	for _, action := range roleplayActions {
		if strings.TrimSpace(resolveGenderVariant("", action.DeclineResult)) == "" {
			t.Fatalf("roleplay action %q has no decline result", action.Command)
		}
	}
}

func TestRoleplayResponderPolicyWithReplyTarget(t *testing.T) {
	st := roleplaySession{
		ActorID:     10,
		TargetID:    20,
		TargetLink:  "Ci",
		ActionIndex: 0,
	}
	if _, ok, reason := roleplayResolveResponder(nil, st, &tgbotapi.User{ID: 30, FirstName: "Ann"}, "accept"); ok || reason != "Принять может только адресат" {
		t.Fatalf("non-target accept must be rejected, ok=%v reason=%q", ok, reason)
	}
	if _, ok, reason := roleplayResolveResponder(nil, st, &tgbotapi.User{ID: 10, FirstName: "Оленька"}, "decline"); ok || reason != "Отказать может только адресат" {
		t.Fatalf("actor decline must be rejected when target exists, ok=%v reason=%q", ok, reason)
	}
	next, ok, reason := roleplayResolveResponder(nil, st, &tgbotapi.User{ID: 20, FirstName: "Ci"}, "decline")
	if !ok || reason != "" || next.TargetID != 20 {
		t.Fatalf("target decline must be allowed, next=%#v ok=%v reason=%q", next, ok, reason)
	}
}

func TestRoleplayResponderPolicyWithoutReplyTarget(t *testing.T) {
	st := roleplaySession{
		ActorID:     10,
		ActorLink:   "Оленька",
		TargetLink:  "кого-то",
		ActionIndex: 0,
	}
	if _, ok, reason := roleplayResolveResponder(nil, st, &tgbotapi.User{ID: 10, FirstName: "Оленька"}, "accept"); ok || reason != "Принять должен другой участник" {
		t.Fatalf("actor accept must be rejected without target, ok=%v reason=%q", ok, reason)
	}
	next, ok, reason := roleplayResolveResponder(nil, st, &tgbotapi.User{ID: 30, FirstName: "Ann"}, "accept")
	if !ok || reason != "" || next.TargetID != 30 || roleplayPlainText(next.TargetLink) != "Ann" {
		t.Fatalf("first non-actor accept must claim target, next=%#v ok=%v reason=%q", next, ok, reason)
	}
}

func TestRoleplayResponderPolicyAllowsActorToCancelPicker(t *testing.T) {
	st := roleplaySession{
		ActorID:     10,
		TargetID:    20,
		ActionIndex: -1,
	}
	if _, ok, reason := roleplayResolveResponder(nil, st, &tgbotapi.User{ID: 20, FirstName: "Ci"}, "decline"); ok || reason != "Это меню не для вас" {
		t.Fatalf("target must not cancel actor picker, ok=%v reason=%q", ok, reason)
	}
	if _, ok, reason := roleplayResolveResponder(nil, st, &tgbotapi.User{ID: 10, FirstName: "Оленька"}, "decline"); !ok || reason != "" {
		t.Fatalf("actor must cancel own picker, ok=%v reason=%q", ok, reason)
	}
}

func TestRoleplaySessionIDFromMarkup(t *testing.T) {
	kb := roleplayConfirmKeyboard("abc123")
	if got := roleplaySessionIDFromMarkup(&kb); got != "abc123" {
		t.Fatalf("unexpected session id: %q", got)
	}
}

func TestRoleplayActionResultUsesGenderTag(t *testing.T) {
	action := roleplayAction{Command: "погладить", Result: genderVariants{
		Male:    "погладил",
		Female:  "погладила",
		Neuter:  "погладило",
		Plural:  "погладили",
		Unknown: "погладил(а)",
	}}
	if got := roleplayActionResult(action, "он"); got != "погладил" {
		t.Fatalf("unexpected male result: %q", got)
	}
	if got := roleplayActionResult(action, "она"); got != "погладила" {
		t.Fatalf("unexpected female result: %q", got)
	}
	if got := roleplayActionResult(roleplayAction{Command: "секс", Result: genderVariants{
		Male:    "занялся сексом с",
		Female:  "занялась сексом с",
		Neuter:  "занялось сексом с",
		Plural:  "занялись сексом с",
		Unknown: "занялся(-ась) сексом с",
	}}, "она"); got != "занялась сексом с" {
		t.Fatalf("unexpected reflexive female result: %q", got)
	}
	if got := roleplayActionResult(action, "оно"); got != "погладило" {
		t.Fatalf("unexpected neuter result: %q", got)
	}
	if got := roleplayActionResult(action, "они"); got != "погладили" {
		t.Fatalf("unexpected plural result: %q", got)
	}
	if got := roleplayActionResult(action, ""); got != "погладил(а)" {
		t.Fatalf("unexpected unknown result: %q", got)
	}
}
