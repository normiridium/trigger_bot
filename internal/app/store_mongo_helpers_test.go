package app

import (
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

func TestResponseItemsFromRaw(t *testing.T) {
	t.Run("legacy string", func(t *testing.T) {
		items, migrate := responseItemsFromRaw(" hello ")
		if !migrate {
			t.Fatalf("expected migration for legacy string")
		}
		if len(items) != 1 || items[0].Text != "hello" {
			t.Fatalf("unexpected items: %#v", items)
		}
	})

	t.Run("array mixed", func(t *testing.T) {
		raw := []interface{}{
			" a ",
			bson.M{"text": "b"},
			map[string]interface{}{"text": " c "},
			bson.D{{Key: "text", Value: "d"}},
			123,
		}
		items, migrate := responseItemsFromRaw(raw)
		if !migrate {
			t.Fatalf("expected migration because of string item")
		}
		if len(items) != 4 {
			t.Fatalf("expected 4 items, got %d (%#v)", len(items), items)
		}
	})

	t.Run("bson array modern", func(t *testing.T) {
		raw := bson.A{bson.M{"text": "first"}, bson.D{{Key: "text", Value: "second"}}}
		items, migrate := responseItemsFromRaw(raw)
		if migrate {
			t.Fatalf("did not expect migration for modern doc items")
		}
		if len(items) != 2 {
			t.Fatalf("expected 2 items, got %d", len(items))
		}
	})
}

func TestNormalizeResponseItems(t *testing.T) {
	items := []ResponseTextItem{{Text: " one "}, {Text: ""}, {Text: "  "}, {Text: "two"}}
	got := normalizeResponseItems(items)
	if len(got) != 2 {
		t.Fatalf("expected 2 normalized items, got %d", len(got))
	}
	if got[0].Text != "one" || got[1].Text != "two" {
		t.Fatalf("unexpected normalized items: %#v", got)
	}
	if normalizeResponseItems(nil) != nil {
		t.Fatalf("expected nil for empty input")
	}
}

func TestIsMongoURI(t *testing.T) {
	if !isMongoURI("mongodb://localhost:27017") {
		t.Fatalf("expected mongodb:// URI to match")
	}
	if !isMongoURI("MONGODB+SRV://cluster/test") {
		t.Fatalf("expected mongodb+srv URI to match")
	}
	if isMongoURI("postgres://localhost/db") {
		t.Fatalf("did not expect non-mongo uri to match")
	}
}

func TestMongoDBNameFromURI(t *testing.T) {
	cases := []struct {
		uri  string
		want string
	}{
		{"mongodb://localhost:27017/mydb", "mydb"},
		{"mongodb://localhost:27017/mydb?retryWrites=true", "mydb"},
		{"mongodb://localhost:27017", "trigger_admin_bot"},
		{"not-a-uri", "not-a-uri"},
	}
	for _, tc := range cases {
		if got := mongoDBNameFromURI(tc.uri); got != tc.want {
			t.Fatalf("mongoDBNameFromURI(%q) = %q, want %q", tc.uri, got, tc.want)
		}
	}
}

func TestTriggerDocRoundTripFromRaw(t *testing.T) {
	raw := bson.M{
		"id":                    int64(42),
		"uid":                   " uid-42 ",
		"priority":              7,
		"title":                 "title",
		"enabled":               true,
		"trigger_mode":          "only_replies",
		"admin_mode":            "admins",
		"match_text":            "Hello",
		"match_type":            "partial",
		"action_type":           "media_link_audio",
		"response_text":         "  answer  ",
		"send_as_reply":         true,
		"preview_first_link":    true,
		"delete_source_message": true,
		"chance":                101,
	}

	tr, migrate, err := docToTriggerFromRaw(raw)
	if err != nil {
		t.Fatalf("docToTriggerFromRaw: %v", err)
	}
	if !migrate {
		t.Fatalf("expected migration for legacy response_text format")
	}
	if tr.UID != "uid-42" {
		t.Fatalf("expected trimmed UID, got %q", tr.UID)
	}
	if tr.TriggerMode != TriggerModeOnlyReplies {
		t.Fatalf("unexpected trigger mode: %q", tr.TriggerMode)
	}
	if tr.AdminMode != AdminModeAdmins {
		t.Fatalf("unexpected admin mode: %q", tr.AdminMode)
	}
	if tr.ActionType != ActionTypeMediaAudio {
		t.Fatalf("unexpected action type: %q", tr.ActionType)
	}
	if tr.Chance != 101 {
		t.Fatalf("expected sanitized chance 101, got %d", tr.Chance)
	}
	if len(tr.ResponseText) != 1 || tr.ResponseText[0].Text != "answer" {
		t.Fatalf("unexpected response text: %#v", tr.ResponseText)
	}

	doc := triggerToDocMap(tr)
	if _, ok := doc["response_text"]; !ok {
		t.Fatalf("expected response_text in doc map")
	}
	if got, ok := doc["match_type"].(MatchType); !ok || got != MatchTypePartial {
		t.Fatalf("unexpected match_type in doc map: %#v", doc["match_type"])
	}
}

func TestNormalizeTriggerMode_NoMediaMode(t *testing.T) {
	got := normalizeTriggerMode("only_replies_to_combot_no_media")
	if got != TriggerModeOnlyRepliesToSelfNoMedia {
		t.Fatalf("normalizeTriggerMode returned %q", got)
	}
}

func TestNormalizeActionType_TikTok(t *testing.T) {
	got := normalizeActionType("media_tiktok_download")
	if got != ActionTypeMediaTikTok {
		t.Fatalf("normalizeActionType returned %q", got)
	}
}

func TestNormalizeActionType_X(t *testing.T) {
	got := normalizeActionType("media_x_download")
	if got != ActionTypeMediaX {
		t.Fatalf("normalizeActionType returned %q", got)
	}
}

func TestNormalizeActionType_SendSticker(t *testing.T) {
	got := normalizeActionType("send_sticker")
	if got != ActionTypeSendSticker {
		t.Fatalf("normalizeActionType returned %q", got)
	}
}

func TestNormalizeActionType_SendFile(t *testing.T) {
	got := normalizeActionType("send_file")
	if got != ActionTypeSendFile {
		t.Fatalf("normalizeActionType returned %q", got)
	}
}

func TestNormalizeActionType_SendGIF(t *testing.T) {
	got := normalizeActionType("send_gif")
	if got != ActionTypeSendGIF {
		t.Fatalf("normalizeActionType returned %q", got)
	}
}

func TestNormalizeActionType_YandexMusic(t *testing.T) {
	got := normalizeActionType("yandex_music_audio")
	if got != ActionTypeYandexMusic {
		t.Fatalf("normalizeActionType returned %q", got)
	}
}

func TestNormalizeActionType_Music(t *testing.T) {
	got := normalizeActionType("music_audio")
	if got != ActionTypeMusic {
		t.Fatalf("normalizeActionType returned %q", got)
	}
}

func TestNormalizeActionType_UserLimitLowWarning(t *testing.T) {
	got := normalizeActionType("user_limit_low_warning")
	if got != ActionTypeUserLimitLow {
		t.Fatalf("normalizeActionType returned %q", got)
	}
}

func TestParseActionType_Unknown(t *testing.T) {
	if _, ok := parseActionType("typo_send_giff"); ok {
		t.Fatalf("expected unknown action type to be rejected")
	}
}

func TestParseActionType_Known(t *testing.T) {
	if got, ok := parseActionType("send"); !ok || got != ActionTypeSend {
		t.Fatalf("expected send to be accepted, got=%q ok=%v", got, ok)
	}
	if got, ok := parseActionType("send_gif"); !ok || got != ActionTypeSendGIF {
		t.Fatalf("expected send_gif to be accepted, got=%q ok=%v", got, ok)
	}
}

func TestTriggerCollectionValidatorContainsSendGIF(t *testing.T) {
	v := triggerCollectionValidatorDoc()
	root, ok := v["$jsonSchema"].(bson.M)
	if !ok {
		t.Fatalf("expected $jsonSchema object")
	}
	props, ok := root["properties"].(bson.M)
	if !ok {
		t.Fatalf("expected properties object")
	}
	actionTypeNode, ok := props["action_type"].(bson.M)
	if !ok {
		t.Fatalf("expected action_type node")
	}
	rawEnum, ok := actionTypeNode["enum"].([]string)
	if !ok {
		t.Fatalf("expected []string enum for action_type")
	}
	found := false
	for _, v := range rawEnum {
		if v == string(ActionTypeSendGIF) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("action_type enum must contain %q", ActionTypeSendGIF)
	}
}
