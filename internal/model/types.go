package model

type Trigger struct {
	ID            int64
	UID           string `json:"uid,omitempty"`
	Priority      int    `json:"priority"`
	RegexBenchUS  int64  `json:"regex_bench_us"`
	Title         string
	Enabled       bool
	TriggerMode   string // all
	AdminMode     string // anybody|admins
	MatchText     string
	MatchType     string // full|partial|regex|starts|ends|idle|new_member
	CaseSensitive bool
	ActionType    string // send|delete|gpt_prompt|gpt_image|search_image|vk_music_audio
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
