package model

type Trigger struct {
	ID            int64
	UID           string `json:"uid,omitempty"`
	Priority      int    `json:"priority"`
	RegexBenchUS  int64  `json:"regex_bench_us"`
	Title         string
	Enabled       bool
	TriggerMode   TriggerMode // all
	AdminMode     AdminMode   // anybody|admins
	MatchText     string
	MatchType     MatchType // full|partial|regex|starts|ends|idle|new_member
	CaseSensitive bool
	ActionType    ActionType         // send|send_file|send_sticker|delete|gpt_prompt|gpt_image|search_image|spotify_music_audio|music_audio|yandex_music_audio|media_link_audio|media_tiktok_download|media_x_download|user_limit_low_warning
	ResponseText  []ResponseTextItem `json:"response_text"`
	Reply         bool
	Preview       bool
	DeleteSource  bool
	PassThrough   bool
	Chance        int
	CreatedAt     int64
	UpdatedAt     int64
	RegexError    string
	CapturingText string `json:"-"`
}

type ResponseTextItem struct {
	Text string `json:"text" bson:"text"`
}

type ResponseTemplate struct {
	ID        int64  `json:"id"`
	Key       string `json:"key"`
	Title     string `json:"title"`
	Text      string `json:"text"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

type TriggerMode string

const (
	TriggerModeAll                      TriggerMode = "all"
	TriggerModeOnlyReplies              TriggerMode = "only_replies"
	TriggerModeOnlyRepliesToBot         TriggerMode = "only_replies_to_any_bot"
	TriggerModeOnlyRepliesToSelf        TriggerMode = "only_replies_to_combot"
	TriggerModeOnlyRepliesToSelfNoMedia TriggerMode = "only_replies_to_combot_no_media"
	TriggerModeNeverOnReplies           TriggerMode = "never_on_replies"
	TriggerModeCommandReply             TriggerMode = "command_reply"
)

var TriggerModeValues = []TriggerMode{
	TriggerModeAll,
	TriggerModeOnlyReplies,
	TriggerModeOnlyRepliesToBot,
	TriggerModeOnlyRepliesToSelf,
	TriggerModeOnlyRepliesToSelfNoMedia,
	TriggerModeNeverOnReplies,
	TriggerModeCommandReply,
}

func (m TriggerMode) String() string {
	switch m {
	case TriggerModeAll:
		return "Все сообщения"
	case TriggerModeOnlyReplies:
		return "Только реплаи"
	case TriggerModeOnlyRepliesToBot:
		return "Только реплаи на любого бота"
	case TriggerModeOnlyRepliesToSelf:
		return "Реплай к боту триггера"
	case TriggerModeOnlyRepliesToSelfNoMedia:
		return "Реплай к боту триггера (без медиа)"
	case TriggerModeNeverOnReplies:
		return "Не срабатывать на реплаи"
	case TriggerModeCommandReply:
		return "Режим команд (действие в ответ)"
	default:
		return string(m)
	}
}

type AdminMode string

const (
	AdminModeAnybody  AdminMode = "anybody"
	AdminModeAdmins   AdminMode = "admins"
	AdminModeNotAdmin AdminMode = "not_admins"
)

var AdminModeValues = []AdminMode{
	AdminModeAnybody,
	AdminModeAdmins,
	AdminModeNotAdmin,
}

func (m AdminMode) String() string {
	switch m {
	case AdminModeAnybody:
		return "Любой участник"
	case AdminModeAdmins:
		return "Только админы"
	case AdminModeNotAdmin:
		return "Кроме админов"
	default:
		return string(m)
	}
}

type MatchType string

const (
	MatchTypeFull      MatchType = "full"
	MatchTypePartial   MatchType = "partial"
	MatchTypeRegex     MatchType = "regex"
	MatchTypeStarts    MatchType = "starts"
	MatchTypeEnds      MatchType = "ends"
	MatchTypeIdle      MatchType = "idle"
	MatchTypeNewMember MatchType = "new_member"
)

var MatchTypeValues = []MatchType{
	MatchTypeIdle,
	MatchTypeFull,
	MatchTypePartial,
	MatchTypeRegex,
	MatchTypeStarts,
	MatchTypeEnds,
	MatchTypeNewMember,
}

func (m MatchType) String() string {
	switch m {
	case MatchTypeIdle:
		return "Простой вызова бота"
	case MatchTypeFull:
		return "Полное совпадение"
	case MatchTypePartial:
		return "Частичное совпадение"
	case MatchTypeRegex:
		return "Регулярное выражение"
	case MatchTypeStarts:
		return "Начинается с"
	case MatchTypeEnds:
		return "Заканчивается на"
	case MatchTypeNewMember:
		return "Новый участник"
	default:
		return string(m)
	}
}

type ActionType string

const (
	ActionTypeSend           ActionType = "send"
	ActionTypeSendFile       ActionType = "send_file"
	ActionTypeSendSticker    ActionType = "send_sticker"
	ActionTypeDelete         ActionType = "delete"
	ActionTypeDeletePortrait ActionType = "delete_user_portrait"
	ActionTypeGPTPrompt      ActionType = "gpt_prompt"
	ActionTypeGPTImage       ActionType = "gpt_image"
	ActionTypeSearchImage    ActionType = "search_image"
	ActionTypeSpotifyMusic   ActionType = "spotify_music_audio"
	ActionTypeMusic          ActionType = "music_audio"
	ActionTypeYandexMusic    ActionType = "yandex_music_audio"
	ActionTypeMediaAudio     ActionType = "media_link_audio"
	ActionTypeMediaTikTok    ActionType = "media_tiktok_download"
	ActionTypeMediaX         ActionType = "media_x_download"
	ActionTypeUserLimitLow   ActionType = "user_limit_low_warning"
)

var ActionTypeValues = []ActionType{
	ActionTypeSend,
	ActionTypeSendFile,
	ActionTypeSendSticker,
	ActionTypeDelete,
	ActionTypeDeletePortrait,
	ActionTypeGPTPrompt,
	ActionTypeGPTImage,
	ActionTypeSearchImage,
	ActionTypeSpotifyMusic,
	ActionTypeMusic,
	ActionTypeYandexMusic,
	ActionTypeMediaAudio,
	ActionTypeMediaTikTok,
	ActionTypeMediaX,
	ActionTypeUserLimitLow,
}

func (m ActionType) String() string {
	switch m {
	case ActionTypeSend:
		return "Отправить сообщение"
	case ActionTypeSendFile:
		return "Отправить файл"
	case ActionTypeSendSticker:
		return "Отправить стикер"
	case ActionTypeDelete:
		return "Удалить сообщение"
	case ActionTypeDeletePortrait:
		return "Удалить портрет участника"
	case ActionTypeGPTPrompt:
		return "Промпт в ChatGPT"
	case ActionTypeGPTImage:
		return "Сгенерировать картинку (ChatGPT)"
	case ActionTypeSearchImage:
		return "Найти картинку (по запросу)"
	case ActionTypeSpotifyMusic:
		return "Spotify музыка (аудио-вложение)"
	case ActionTypeMusic:
		return "Музыка (Spotify / Yandex)"
	case ActionTypeYandexMusic:
		return "Yandex Music (аудио по ссылке)"
	case ActionTypeMediaAudio:
		return "Скачать медиа по ссылке (аудио/видео)"
	case ActionTypeMediaTikTok:
		return "Скачать с TikTok"
	case ActionTypeMediaX:
		return "Скачать с X (Twitter)"
	case ActionTypeUserLimitLow:
		return "Лимит GPT почти исчерпан (остаток 1)"
	default:
		return string(m)
	}
}
