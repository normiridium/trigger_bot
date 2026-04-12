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
	ActionType    ActionType         // send|delete|gpt_prompt|gpt_image|search_image|vk_music_audio
	ResponseText  []ResponseTextItem `json:"response_text"`
	Reply         bool
	Preview       bool
	DeleteSource  bool
	Chance        int
	CreatedAt     int64
	UpdatedAt     int64
	RegexError    string
	CapturingText string `json:"-"`
}

type ResponseTextItem struct {
	Text string `json:"text" bson:"text"`
}

type TriggerMode string

const (
	TriggerModeAll               TriggerMode = "all"
	TriggerModeOnlyReplies       TriggerMode = "only_replies"
	TriggerModeOnlyRepliesToBot  TriggerMode = "only_replies_to_any_bot"
	TriggerModeOnlyRepliesToSelf TriggerMode = "only_replies_to_combot"
	TriggerModeNeverOnReplies    TriggerMode = "never_on_replies"
	TriggerModeCommandReply      TriggerMode = "command_reply"
)

type AdminMode string

const (
	AdminModeAnybody  AdminMode = "anybody"
	AdminModeAdmins   AdminMode = "admins"
	AdminModeNotAdmin AdminMode = "not_admins"
)

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

type ActionType string

const (
	ActionTypeSend        ActionType = "send"
	ActionTypeDelete      ActionType = "delete"
	ActionTypeGPTPrompt   ActionType = "gpt_prompt"
	ActionTypeGPTImage    ActionType = "gpt_image"
	ActionTypeSearchImage ActionType = "search_image"
	ActionTypeVKMusic     ActionType = "vk_music_audio"
)
