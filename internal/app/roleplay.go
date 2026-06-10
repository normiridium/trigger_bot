package app

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"html"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	roleplayCallbackPrefix = "rp"
	roleplayPageSize       = 10
)

var roleplayDeclineAction = roleplayAction{
	Emoji:   "🙅‍♂️",
	EmojiID: "6325579261763651444",
	Command: "отказаться",
	Result:  genderVariants{Male: "не хочет", Female: "не хочет", Neuter: "не хочет", Plural: "не хотят", Unknown: "не хочет"},
}

type roleplayAction struct {
	Emoji       string
	EmojiID     string
	Command     string
	Result      genderVariants
	NeedsTarget bool
}

type roleplaySession struct {
	ID          string
	ChatID      int64
	ActorID     int64
	TargetID    int64
	ActorLink   string
	ActorTag    string
	TargetLink  string
	Inline      bool
	ActionIndex int
	SourceMsgID int
	CreatedAt   time.Time
}

type roleplaySessionManager struct {
	mu       sync.Mutex
	sessions map[string]roleplaySession
	ttl      time.Duration
}

var defaultRoleplaySessions = newRoleplaySessionManager(20 * time.Minute)

func newRoleplaySessionManager(ttl time.Duration) *roleplaySessionManager {
	return &roleplaySessionManager{
		sessions: make(map[string]roleplaySession),
		ttl:      ttl,
	}
}

func (m *roleplaySessionManager) create(st roleplaySession) roleplaySession {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupLocked(time.Now())
	st.ID = randomRoleplayID()
	st.CreatedAt = time.Now()
	m.sessions[st.ID] = st
	return st
}

func (m *roleplaySessionManager) get(id string) (roleplaySession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupLocked(time.Now())
	st, ok := m.sessions[id]
	return st, ok
}

func (m *roleplaySessionManager) update(st roleplaySession) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[st.ID] = st
}

func (m *roleplaySessionManager) delete(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, id)
}

func (m *roleplaySessionManager) cleanupLocked(now time.Time) {
	for id, st := range m.sessions {
		if now.Sub(st.CreatedAt) > m.ttl {
			delete(m.sessions, id)
		}
	}
}

func randomRoleplayID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

var roleplayActions = []roleplayAction{
	{Emoji: "🫶", EmojiID: "5289978022957440232", Command: "обнять", Result: genderVariants{Male: "обнял", Female: "обняла", Neuter: "обняло", Plural: "обняли", Unknown: "обнял(а)"}, NeedsTarget: true},
	{Emoji: "💋", EmojiID: "5474525960143385880", Command: "поцеловать", Result: genderVariants{Male: "поцеловал", Female: "поцеловала", Neuter: "поцеловало", Plural: "поцеловали", Unknown: "поцеловал(а)"}, NeedsTarget: true},
	{Emoji: "☺️", EmojiID: "5373149502003747704", Command: "погладить", Result: genderVariants{Male: "погладил", Female: "погладила", Neuter: "погладило", Plural: "погладили", Unknown: "погладил(а)"}, NeedsTarget: true},
	{Emoji: "💋", EmojiID: "5244788236789755429", Command: "чмок", Result: genderVariants{Male: "нежно чмокнул", Female: "нежно чмокнула", Neuter: "нежно чмокнуло", Plural: "нежно чмокнули", Unknown: "нежно чмокнул(а)"}, NeedsTarget: true},
	{Emoji: "💋", EmojiID: "5253596987080588696", Command: "засосать", Result: genderVariants{Male: "сладко засосал", Female: "сладко засосала", Neuter: "сладко засосало", Plural: "сладко засосали", Unknown: "сладко засосал(а)"}, NeedsTarget: true},
	{Emoji: "💕", EmojiID: "5420146103603457887", Command: "прижать", Result: genderVariants{Male: "прижал к себе", Female: "прижала к себе", Neuter: "прижало к себе", Plural: "прижали к себе", Unknown: "прижал(а) к себе"}, NeedsTarget: true},
	{Emoji: "👅", EmojiID: "5310080741940410646", Command: "отлизать", Result: genderVariants{Male: "отлизал у", Female: "отлизала у", Neuter: "отлизало у", Plural: "отлизали у", Unknown: "отлизал(а) у"}, NeedsTarget: true},
	{Emoji: "🍌", EmojiID: "5436225043820456569", Command: "отсосать", Result: genderVariants{Male: "отсосал у", Female: "отсосала у", Neuter: "отсосало у", Plural: "отсосали у", Unknown: "отсосал(а) у"}, NeedsTarget: true},
	{Emoji: "🥵", EmojiID: "5238003558556650063", Command: "выебать", Result: genderVariants{Male: "жёстко выебал", Female: "жёстко выебала", Neuter: "жёстко выебало", Plural: "жёстко выебали", Unknown: "жёстко выебал(а)"}, NeedsTarget: true},
	{Emoji: "🦷", EmojiID: "5454226097866549856", Command: "кусь", Result: genderVariants{Male: "кусьнул", Female: "кусьнула", Neuter: "кусьнуло", Plural: "кусьнули", Unknown: "кусьнул(а)"}, NeedsTarget: true},
	{Emoji: "👩‍❤️‍👨", EmojiID: "5235785298142572002", Command: "лечь", Result: genderVariants{Male: "лёг рядом с", Female: "легла рядом с", Neuter: "легло рядом с", Plural: "легли рядом с", Unknown: "лег(ла) рядом с"}, NeedsTarget: true},
	{Emoji: "😈", EmojiID: "5235710501287111302", Command: "укусить", Result: genderVariants{Male: "укусил", Female: "укусила", Neuter: "укусило", Plural: "укусили", Unknown: "укусил(а)"}, NeedsTarget: true},
	{Emoji: "🍑", EmojiID: "5348300427546859519", Command: "шлёпнуть", Result: genderVariants{Male: "шлёпнул по попке", Female: "шлёпнула по попке", Neuter: "шлёпнуло по попке", Plural: "шлёпнули по попке", Unknown: "шлёпнул(а) по попке"}, NeedsTarget: true},
	{Emoji: "🥵", EmojiID: "5325650552439192610", Command: "трахнуть", Result: genderVariants{Male: "страстно трахнул", Female: "страстно трахнула", Neuter: "страстно трахнуло", Plural: "страстно трахнули", Unknown: "страстно трахнул(а)"}, NeedsTarget: true},
	{Emoji: "👙", EmojiID: "6325835362073577390", Command: "отдаться", Result: genderVariants{Male: "отдался", Female: "отдалась", Neuter: "отдалось", Plural: "отдались", Unknown: "отдался(-ась)"}, NeedsTarget: true},
	{Emoji: "🐾", EmojiID: "5301071996497447635", Command: "потрогать", Result: genderVariants{Male: "потрогал", Female: "потрогала", Neuter: "потрогало", Plural: "потрогали", Unknown: "потрогал(а)"}, NeedsTarget: true},
	{Emoji: "🫳", EmojiID: "5269400478160003300", Command: "похвалить", Result: genderVariants{Male: "похвалил умничку", Female: "похвалила умничку", Neuter: "похвалило умничку", Plural: "похвалили умничку", Unknown: "похвалил(а) умничку"}, NeedsTarget: true},
	{Emoji: "🍕", EmojiID: "5341722169736962913", Command: "покормить", Result: genderVariants{Male: "покормил", Female: "покормила", Neuter: "покормило", Plural: "покормили", Unknown: "покормил(а)"}, NeedsTarget: true},
	{Emoji: "🤩", EmojiID: "5348261695531794267", Command: "лизь", Result: genderVariants{Male: "лизнул", Female: "лизнула", Neuter: "лизнуло", Plural: "лизнули", Unknown: "лизнул(а)"}, NeedsTarget: true},
	{Emoji: "😮‍💨", EmojiID: "5301100665404148696", Command: "секс", Result: genderVariants{Male: "занялся сексом с", Female: "занялась сексом с", Neuter: "занялось сексом с", Plural: "занялись сексом с", Unknown: "занялся(-ась) сексом с"}, NeedsTarget: true},
	{Emoji: "✂", EmojiID: "5318804172705910750", Command: "кастрировать", Result: genderVariants{Male: "кастрировал", Female: "кастрировала", Neuter: "кастрировало", Plural: "кастрировали", Unknown: "кастрировал(а)"}, NeedsTarget: true},
	{Emoji: "🤩", EmojiID: "5346172142402684175", Command: "куснуть", Result: genderVariants{Male: "кусьнул с любовью", Female: "кусьнула с любовью", Neuter: "кусьнуло с любовью", Plural: "кусьнули с любовью", Unknown: "кусьнул(а) с любовью"}, NeedsTarget: true},
	{Emoji: "🍒", EmojiID: "5307733606737658564", Command: "раздеть", Result: genderVariants{Male: "страстно сорвал одежду с", Female: "страстно сорвала одежду с", Neuter: "страстно сорвало одежду с", Plural: "страстно сорвали одежду с", Unknown: "страстно сорвал(а) одежду с"}, NeedsTarget: true},
	{Emoji: "🍻", EmojiID: "5462873262897764840", Command: "выпить", Result: genderVariants{Male: "выпил с", Female: "выпила с", Neuter: "выпило с", Plural: "выпили с", Unknown: "выпил(а) с"}, NeedsTarget: true},
	{Emoji: "🙆‍♂️", EmojiID: "5258360608323021231", Command: "связать", Result: genderVariants{Male: "связал и начал доминировать над", Female: "связала и начала доминировать над", Neuter: "связало и начало доминировать над", Plural: "связали и начали доминировать над", Unknown: "связал(а) и начал(а) доминировать над"}, NeedsTarget: true},
	{Emoji: "💍", EmojiID: "5332672330046905245", Command: "пожениться", Result: genderVariants{Male: "объявил свадьбу с", Female: "объявила свадьбу с", Neuter: "объявило свадьбу с", Plural: "объявили свадьбу с", Unknown: "объявил(а) свадьбу с"}, NeedsTarget: true},
	{Emoji: "🍑", EmojiID: "5456462779395351441", Command: "наказать", Result: genderVariants{Male: "наказал", Female: "наказала", Neuter: "наказало", Plural: "наказали", Unknown: "наказал(а)"}, NeedsTarget: true},
	{Emoji: "🥰", EmojiID: "5217784085182687461", Command: "лизнуть", Result: genderVariants{Male: "лизнул", Female: "лизнула", Neuter: "лизнуло", Plural: "лизнули", Unknown: "лизнул(а)"}, NeedsTarget: true},
	{Emoji: "😔", EmojiID: "5346139938737910151", Command: "извиниться", Result: genderVariants{Male: "извинился перед", Female: "извинилась перед", Neuter: "извинилось перед", Plural: "извинились перед", Unknown: "извинился(ась) перед"}, NeedsTarget: true},
	{Emoji: "🙈", EmojiID: "5282784773075388685", Command: "доминировать", Result: genderVariants{Male: "доминирует и властвует над", Female: "доминирует и властвует над", Neuter: "доминирует и властвует над", Plural: "доминируют и властвуют над", Unknown: "доминирует и властвует над"}, NeedsTarget: true},
	{Emoji: "👊", EmojiID: "5463172257046076523", Command: "ударить", Result: genderVariants{Male: "ударил", Female: "ударила", Neuter: "ударило", Plural: "ударили", Unknown: "ударил(а)"}, NeedsTarget: true},
	{Emoji: "🤫", EmojiID: "5345809986465309859", Command: "раздеться", Result: genderVariants{Male: "разделся перед", Female: "разделась перед", Neuter: "разделось перед", Plural: "разделись перед", Unknown: "разделся(ась) перед"}, NeedsTarget: true},
	{Emoji: "😡", EmojiID: "5323804562610472199", Command: "пнуть", Result: genderVariants{Male: "дал смачного поджопника", Female: "дала смачного поджопника", Neuter: "дало смачного поджопника", Plural: "дали смачного поджопника", Unknown: "дал(а) смачного поджопника"}, NeedsTarget: true},
	{Emoji: "💍", EmojiID: "5370557420521154191", Command: "сделать предложение", Result: genderVariants{Male: "сделал предложение", Female: "сделала предложение", Neuter: "сделало предложение", Plural: "сделали предложение", Unknown: "сделал(а) предложение"}, NeedsTarget: true},
	{Emoji: "✊", EmojiID: "5348072201574691413", Command: "уебать", Result: genderVariants{Male: "жёстко уебал", Female: "жёстко уебала", Neuter: "жёстко уебало", Plural: "жёстко уебали", Unknown: "жёстко уебал(а)"}, NeedsTarget: true},
	{Emoji: "😈", EmojiID: "5460951857738293094", Command: "пощекотать", Result: genderVariants{Male: "защекотал до потери сознания", Female: "защекотала до потери сознания", Neuter: "защекотало до потери сознания", Plural: "защекотали до потери сознания", Unknown: "защекотал(а) до потери сознания"}, NeedsTarget: true},
	{Emoji: "🤏", EmojiID: "5447315718825582234", Command: "ущипнуть", Result: genderVariants{Male: "ущипнул", Female: "ущипнула", Neuter: "ущипнуло", Plural: "ущипнули", Unknown: "ущипнул(а)"}, NeedsTarget: true},
	{Emoji: "🥵", EmojiID: "5355017146512458653", Command: "придушить", Result: genderVariants{Male: "в порыве страсти придушил", Female: "в порыве страсти придушила", Neuter: "в порыве страсти придушило", Plural: "в порыве страсти придушили", Unknown: "в порыве страсти придушил(а)"}, NeedsTarget: true},
	{Emoji: "🔪", EmojiID: "5226472065288133637", Command: "убить", Result: genderVariants{Male: "безжалостно убил", Female: "безжалостно убила", Neuter: "безжалостно убило", Plural: "безжалостно убили", Unknown: "безжалостно убил(а)"}, NeedsTarget: true},
	{Emoji: "🔞", EmojiID: "5231422483248200859", Command: "заткнуть", Result: genderVariants{Male: "заткнул рот кляпом", Female: "заткнула рот кляпом", Neuter: "заткнуло рот кляпом", Plural: "заткнули рот кляпом", Unknown: "заткнул(а) рот кляпом"}, NeedsTarget: true},
	{Emoji: "🤝", EmojiID: "5269402556924180806", Command: "пожать руку", Result: genderVariants{Male: "крепко пожал руку", Female: "крепко пожала руку", Neuter: "крепко пожало руку", Plural: "крепко пожали руку", Unknown: "крепко пожал(а) руку"}, NeedsTarget: true},
	{Emoji: "👃", EmojiID: "5233191520147878517", Command: "понюхать", Result: genderVariants{Male: "понюхал", Female: "понюхала", Neuter: "понюхало", Plural: "понюхали", Unknown: "понюхал(а)"}, NeedsTarget: true},
	{Emoji: "🏏", EmojiID: "5260574612424499741", Command: "выпороть", Result: genderVariants{Male: "выпорол", Female: "выпорола", Neuter: "выпороло", Plural: "выпороли", Unknown: "выпорол(а)"}, NeedsTarget: true},
	{Emoji: "🖕", EmojiID: "5262694792965412496", Command: "послать нахуй", Result: genderVariants{Male: "с любовью шлёт нахуй", Female: "с любовью шлёт нахуй", Neuter: "с любовью шлёт нахуй", Plural: "с любовью шлют нахуй", Unknown: "с любовью шлёт нахуй"}, NeedsTarget: true},
	{Emoji: "😉", EmojiID: "5370641795153665848", Command: "подмигнуть", Result: genderVariants{Male: "игриво подмигнул", Female: "игриво подмигнула", Neuter: "игриво подмигнуло", Plural: "игриво подмигнули", Unknown: "игриво подмигнул(а)"}, NeedsTarget: true},
	{Emoji: "⛓️", EmojiID: "5422604048962453627", Command: "арестовать", Result: genderVariants{Male: "сковал в наручники", Female: "сковала в наручники", Neuter: "сковало в наручники", Plural: "сковали в наручники", Unknown: "сковал(а) в наручники"}, NeedsTarget: true},
	{Emoji: "💰", EmojiID: "5224257782013769471", Command: "продать", Result: genderVariants{Male: "продал", Female: "продала", Neuter: "продало", Plural: "продали", Unknown: "продал(а)"}, NeedsTarget: true},
	{Emoji: "🤝", EmojiID: "5235471263018788689", Command: "дать пять", Result: genderVariants{Male: "дал пять", Female: "дала пять", Neuter: "дало пять", Plural: "дали пять", Unknown: "дал(а) пять"}, NeedsTarget: true},
	{Emoji: "👅", EmojiID: "5390960151559678908", Command: "лизнуть", Result: genderVariants{Male: "лизнул", Female: "лизнула", Neuter: "лизнуло", Plural: "лизнули", Unknown: "лизнул(а)"}, NeedsTarget: true},
	{Emoji: "🤩", EmojiID: "5348174799753460477", Command: "стукнуть", Result: genderVariants{Male: "стукнул по башке", Female: "стукнула по башке", Neuter: "стукнуло по башке", Plural: "стукнули по башке", Unknown: "стукнул(а) по башке"}, NeedsTarget: true},
	{Emoji: "😡", EmojiID: "5375098090011238490", Command: "расстрелять", Result: genderVariants{Male: "расстрелял", Female: "расстреляла", Neuter: "расстреляло", Plural: "расстреляли", Unknown: "расстрелял(а)"}, NeedsTarget: true},
	{Emoji: "😵", EmojiID: "5352964336828565993", Command: "задушить", Result: genderVariants{Male: "задушил", Female: "задушила", Neuter: "задушило", Plural: "задушили", Unknown: "задушил(а)"}, NeedsTarget: true},
	{Emoji: "🩷", EmojiID: "5422444740035506029", Command: "унизить", Result: genderVariants{Male: "унизил", Female: "унизила", Neuter: "унизило", Plural: "унизили", Unknown: "унизил(а)"}, NeedsTarget: true},
	{Emoji: "😤", EmojiID: "6325618998801074473", Command: "наорать", Result: genderVariants{Male: "наорал", Female: "наорала", Neuter: "наорало", Plural: "наорали", Unknown: "наорал(а)"}, NeedsTarget: true},
	{Emoji: "👻", EmojiID: "5206523956537865948", Command: "испугать", Result: genderVariants{Male: "испугал", Female: "испугала", Neuter: "испугало", Plural: "испугали", Unknown: "испугал(а)"}, NeedsTarget: true},
	{Emoji: "🔥", EmojiID: "5463393636840382597", Command: "огонёк", Result: genderVariants{Male: "поджёг огонёк для", Female: "подожгла огонёк для", Neuter: "подожгло огонёк для", Plural: "подожгли огонёк для", Unknown: "поджёг/подожгла огонёк для"}, NeedsTarget: true},
	{Emoji: "🤯", EmojiID: "5253960689206181297", Command: "повесить", Result: genderVariants{Male: "повесил", Female: "повесила", Neuter: "повесило", Plural: "повесили", Unknown: "повесил(а)"}, NeedsTarget: true},
	{Emoji: "🔫", EmojiID: "6325570379771283209", Command: "застрелить", Result: genderVariants{Male: "застрелил", Female: "застрелила", Neuter: "застрелило", Plural: "застрелили", Unknown: "застрелил(а)"}, NeedsTarget: true},
	{Emoji: "😂", EmojiID: "5289707070650603120", Command: "рассмешить", Result: genderVariants{Male: "рассмешил", Female: "рассмешила", Neuter: "рассмешило", Plural: "рассмешили", Unknown: "рассмешил(а)"}, NeedsTarget: true},
	{Emoji: "😈", EmojiID: "5262879150141626454", Command: "сжечь", Result: genderVariants{Male: "сжёг", Female: "сожгла", Neuter: "сожгло", Plural: "сожгли", Unknown: "сжёг/сожгла"}, NeedsTarget: true},
	{Emoji: "💣", EmojiID: "5226813248900187912", Command: "взорвать", Result: genderVariants{Male: "взорвал", Female: "взорвала", Neuter: "взорвало", Plural: "взорвали", Unknown: "взорвал(а)"}, NeedsTarget: true},
	{Emoji: "😑", EmojiID: "5235677812291019916", Command: "заставить", Result: genderVariants{Male: "заставил", Female: "заставила", Neuter: "заставило", Plural: "заставили", Unknown: "заставил(а)"}, NeedsTarget: true},
	{Emoji: "🧑‍🚒", EmojiID: "5389041795826989794", Command: "отрубить", Result: genderVariants{Male: "отрубил кое-что у", Female: "отрубила кое-что у", Neuter: "отрубило кое-что у", Plural: "отрубили кое-что у", Unknown: "отрубил(а) кое-что у"}, NeedsTarget: true},
	{Emoji: "👏", EmojiID: "6325709519531804298", Command: "аплодировать", Result: genderVariants{Male: "громко поаплодировал", Female: "громко поаплодировала", Neuter: "громко поаплодировало", Plural: "громко поаплодировали", Unknown: "громко поаплодировал(а)"}, NeedsTarget: true},
	{Emoji: "🪄", EmojiID: "5247250550130486334", Command: "пожелать", Result: genderVariants{Male: "пожелал всего хорошего", Female: "пожелала всего хорошего", Neuter: "пожелало всего хорошего", Plural: "пожелали всего хорошего", Unknown: "пожелал(а) всего хорошего"}, NeedsTarget: true},
	{Emoji: "🥹", EmojiID: "5219692867433275476", Command: "кивнуть", Result: genderVariants{Male: "одобрительно кивнул", Female: "одобрительно кивнула", Neuter: "одобрительно кивнуло", Plural: "одобрительно кивнули", Unknown: "одобрительно кивнул(а)"}, NeedsTarget: true},
	{Emoji: "🥳", EmojiID: "5269480798343406297", Command: "поздравить", Result: genderVariants{Male: "поздравил", Female: "поздравила", Neuter: "поздравило", Plural: "поздравили", Unknown: "поздравил(а)"}, NeedsTarget: true},
	{Emoji: "💥", EmojiID: "5269331183157649318", Command: "уничтожить", Result: genderVariants{Male: "уничтожил", Female: "уничтожила", Neuter: "уничтожило", Plural: "уничтожили", Unknown: "уничтожил(а)"}, NeedsTarget: true},
	{Emoji: "🤢", EmojiID: "5350708280702281097", Command: "отравить", Result: genderVariants{Male: "отравил", Female: "отравила", Neuter: "отравило", Plural: "отравили", Unknown: "отравил(а)"}, NeedsTarget: true},
}

func handleRoleplayCommand(bot *tgbotapi.BotAPI, msg *tgbotapi.Message, args string) bool {
	if msg == nil || msg.From == nil {
		return true
	}
	if msg.Chat == nil || (msg.Chat.Type != "group" && msg.Chat.Type != "supergroup") {
		reply(sendContext{Bot: bot, ChatID: msg.Chat.ID, ReplyTo: msg.MessageID}, "Roleplay работает в группах: ответьте командой на сообщение участника.", false)
		return true
	}
	targetID := int64(0)
	targetLink := "кого-то"
	replyToID := 0
	if msg.ReplyToMessage != nil && msg.ReplyToMessage.From != nil {
		targetID = msg.ReplyToMessage.From.ID
		targetLink = buildUserLink(msg.ReplyToMessage.From)
		replyToID = msg.ReplyToMessage.MessageID
	}
	if targetID != 0 && targetID == msg.From.ID {
		reply(sendContext{Bot: bot, ChatID: msg.Chat.ID, ReplyTo: msg.MessageID}, "Для self-care лучше печеньку и плед, а roleplay нужен с другим участником.", false)
		return true
	}
	if containsUnsafeRoleplayAction(args) {
		reply(sendContext{Bot: bot, ChatID: msg.Chat.ID, ReplyTo: msg.MessageID}, "Эту формулировку не добавляю. Выбери безопасный consensual-вариант из /roleplay.", false)
		return true
	}

	st := defaultRoleplaySessions.create(roleplaySession{
		ChatID:      msg.Chat.ID,
		ActorID:     msg.From.ID,
		TargetID:    targetID,
		ActorLink:   buildUserLink(msg.From),
		ActorTag:    getChatMemberTagRaw(bot.Token, msg.Chat.ID, msg.From.ID),
		TargetLink:  targetLink,
		ActionIndex: -1,
		SourceMsgID: msg.MessageID,
	})

	args = strings.TrimSpace(args)
	if args == "" {
		return sendRoleplayPicker(bot, st, 0, replyToID)
	}

	idx := findRoleplayAction(args)
	if idx < 0 {
		reply(sendContext{Bot: bot, ChatID: msg.Chat.ID, ReplyTo: msg.MessageID}, "Не нашла такое действие. Напиши /roleplay без аргументов, там будет список.", false)
		return true
	}
	st.ActionIndex = idx
	defaultRoleplaySessions.update(st)
	return sendRoleplayProposal(bot, st, replyToID, false)
}

func sendRoleplayInlineHint(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	if bot == nil || msg == nil || msg.Chat == nil {
		return
	}
	text := fmt.Sprintf("Открой inline-меню: начни вводить @%s и выбери действие.\nЕсли отвечаешь на сообщение участника — Telegram отправит выбранный roleplay ответом.", bot.Self.UserName)
	m := tgbotapi.NewMessage(msg.Chat.ID, text)
	m.ReplyToMessageID = msg.MessageID
	m.AllowSendingWithoutReply = true
	sw := ""
	m.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(tgbotapi.InlineKeyboardButton{
		Text:                         "Открыть inline-меню",
		SwitchInlineQueryCurrentChat: &sw,
	}))
	if _, err := bot.Send(m); err != nil {
		log.Printf("roleplay inline hint send failed chat=%d: %v", msg.Chat.ID, err)
		reportChatFailure(bot, msg.Chat.ID, "ошибка roleplay", err)
		return
	}
	deleteRoleplayCommandMessage(bot, msg.Chat.ID, msg.MessageID)
}

func handleRoleplayInlineQuery(bot *tgbotapi.BotAPI, q *tgbotapi.InlineQuery) bool {
	if bot == nil || q == nil || strings.TrimSpace(q.ID) == "" || q.From == nil {
		return false
	}
	results := roleplayInlineResults(q, 50)
	conf := tgbotapi.InlineConfig{
		InlineQueryID: q.ID,
		Results:       results,
		CacheTime:     0,
		IsPersonal:    true,
	}
	if _, err := bot.Request(conf); err != nil {
		log.Printf("roleplay inline answer failed user=%d query=%q: %v", q.From.ID, q.Query, err)
	}
	return true
}

func handleRoleplayInlineSentMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) bool {
	if bot == nil || msg == nil || msg.Chat == nil || msg.From == nil {
		return false
	}
	id := roleplaySessionIDFromMarkup(msg.ReplyMarkup)
	if id == "" {
		return false
	}
	st, ok := defaultRoleplaySessions.get(id)
	if !ok || !st.Inline {
		return false
	}
	if st.ActorID != 0 && st.ActorID != msg.From.ID {
		return false
	}

	st.ChatID = msg.Chat.ID
	st.ActorID = msg.From.ID
	st.ActorLink = buildUserLink(msg.From)
	st.ActorTag = getChatMemberTagRaw(bot.Token, msg.Chat.ID, msg.From.ID)
	if strings.Contains(firstNonEmptyUserText(msg), "→ кого-то") && msg.ReplyToMessage != nil && msg.ReplyToMessage.From != nil && msg.ReplyToMessage.From.ID != msg.From.ID {
		st.TargetID = msg.ReplyToMessage.From.ID
		st.TargetLink = buildUserLink(msg.ReplyToMessage.From)
	}
	defaultRoleplaySessions.update(st)

	if st.TargetID == 0 || roleplayPlainText(st.TargetLink) == "кого-то" {
		return true
	}
	text, entities := roleplayInlineProposalContent(st)
	edit := tgbotapi.EditMessageTextConfig{
		BaseEdit: tgbotapi.BaseEdit{
			ChatID:      msg.Chat.ID,
			MessageID:   msg.MessageID,
			ReplyMarkup: msg.ReplyMarkup,
		},
		Text:                  text,
		Entities:              entities,
		DisableWebPagePreview: true,
	}
	if _, err := bot.Request(edit); err != nil {
		log.Printf("roleplay inline target edit failed chat=%d msg=%d session=%s: %v", msg.Chat.ID, msg.MessageID, id, err)
		return true
	}
	return true
}

func roleplaySessionIDFromMarkup(markup *tgbotapi.InlineKeyboardMarkup) string {
	if markup == nil {
		return ""
	}
	for _, row := range markup.InlineKeyboard {
		for _, btn := range row {
			if btn.CallbackData == nil {
				continue
			}
			parts := strings.Split(*btn.CallbackData, "|")
			if len(parts) >= 3 && parts[0] == roleplayCallbackPrefix && (parts[1] == "accept" || parts[1] == "decline") {
				return strings.TrimSpace(parts[2])
			}
		}
	}
	return ""
}

func handleRoleplayCallback(bot *tgbotapi.BotAPI, cb *tgbotapi.CallbackQuery) bool {
	if cb == nil || !strings.HasPrefix(cb.Data, roleplayCallbackPrefix+"|") {
		return false
	}
	parts := strings.Split(cb.Data, "|")
	if len(parts) < 3 {
		answerRoleplayCallback(bot, cb, "Кнопка сломалась")
		return true
	}
	action := parts[1]
	id := parts[2]
	st, ok := defaultRoleplaySessions.get(id)
	if !ok {
		answerRoleplayCallback(bot, cb, "Заявка устарела")
		return true
	}
	isInlineMessage := st.Inline && cb.InlineMessageID != ""
	if !isInlineMessage && (cb.Message == nil || cb.Message.Chat == nil || cb.Message.Chat.ID != st.ChatID) {
		answerRoleplayCallback(bot, cb, "Чат не совпадает")
		return true
	}

	switch action {
	case "page":
		if cb.From == nil || cb.From.ID != st.ActorID {
			answerRoleplayCallback(bot, cb, "Это меню не для вас")
			return true
		}
		page := 0
		if len(parts) >= 4 {
			_, _ = fmt.Sscanf(parts[3], "%d", &page)
		}
		answerRoleplayCallback(bot, cb, "")
		editRoleplayPicker(bot, st, cb.Message.MessageID, page)
	case "pick":
		if cb.From == nil || cb.From.ID != st.ActorID {
			answerRoleplayCallback(bot, cb, "Это меню не для вас")
			return true
		}
		idx := -1
		if len(parts) >= 4 {
			_, _ = fmt.Sscanf(parts[3], "%d", &idx)
		}
		if idx < 0 || idx >= len(roleplayActions) {
			answerRoleplayCallback(bot, cb, "Некорректный выбор")
			return true
		}
		st.ActionIndex = idx
		defaultRoleplaySessions.update(st)
		answerRoleplayCallback(bot, cb, "Выбрано: "+roleplayActions[idx].Command)
		editRoleplayProposal(bot, st, cb.Message.MessageID)
	case "accept":
		next, ok, reason := roleplayResolveResponder(st, cb.From, "accept")
		if !ok {
			answerRoleplayCallback(bot, cb, reason)
			return true
		}
		st = next
		if st.ActionIndex < 0 || st.ActionIndex >= len(roleplayActions) {
			answerRoleplayCallback(bot, cb, "Действие не выбрано")
			return true
		}
		answerRoleplayCallback(bot, cb, "Принято")
		defaultRoleplaySessions.delete(st.ID)
		if isInlineMessage {
			editRoleplayInlineFinal(bot, st, cb.InlineMessageID, false)
		} else {
			editRoleplayFinal(bot, st, cb.Message.MessageID, false)
		}
	case "decline":
		next, ok, reason := roleplayResolveResponder(st, cb.From, "decline")
		if !ok {
			answerRoleplayCallback(bot, cb, reason)
			return true
		}
		st = next
		answerRoleplayCallback(bot, cb, "Отказано")
		defaultRoleplaySessions.delete(st.ID)
		if isInlineMessage {
			editRoleplayInlineFinal(bot, st, cb.InlineMessageID, true)
		} else {
			editRoleplayFinal(bot, st, cb.Message.MessageID, true)
		}
	default:
		answerRoleplayCallback(bot, cb, "Неизвестное действие")
	}
	return true
}

func roleplayResolveResponder(st roleplaySession, user *tgbotapi.User, action string) (roleplaySession, bool, string) {
	if user == nil {
		return st, false, "Не вижу пользователя"
	}
	userID := user.ID
	if action == "decline" && st.ActionIndex < 0 {
		if userID != st.ActorID {
			return st, false, "Это меню не для вас"
		}
		return st, true, ""
	}
	if st.TargetID != 0 {
		if userID != st.TargetID {
			if action == "decline" {
				return st, false, "Отказать может только адресат"
			}
			return st, false, "Принять может только адресат"
		}
		return st, true, ""
	}
	if userID == st.ActorID {
		if action == "decline" {
			return st, false, "Отказать должен другой участник"
		}
		return st, false, "Принять должен другой участник"
	}
	st.TargetID = userID
	st.TargetLink = buildUserLink(user)
	return st, true, ""
}

func sendRoleplayPicker(bot *tgbotapi.BotAPI, st roleplaySession, page, replyTo int) bool {
	m := tgbotapi.NewMessage(st.ChatID, roleplayPickerText(st))
	m.ParseMode = tgbotapi.ModeHTML
	m.DisableWebPagePreview = true
	m.ReplyToMessageID = replyTo
	m.AllowSendingWithoutReply = true
	m.ReplyMarkup = roleplayPickerKeyboard(st.ID, page)
	if _, err := bot.Send(m); err != nil {
		log.Printf("roleplay picker send failed chat=%d: %v", st.ChatID, err)
		reportChatFailure(bot, st.ChatID, "ошибка roleplay", err)
		return false
	}
	if st.SourceMsgID > 0 {
		deleteRoleplayCommandMessage(bot, st.ChatID, st.SourceMsgID)
	}
	return true
}

func editRoleplayPicker(bot *tgbotapi.BotAPI, st roleplaySession, msgID, page int) {
	edit := tgbotapi.NewEditMessageTextAndMarkup(st.ChatID, msgID, roleplayPickerText(st), roleplayPickerKeyboard(st.ID, page))
	edit.ParseMode = tgbotapi.ModeHTML
	if _, err := bot.Request(edit); err != nil {
		log.Printf("roleplay picker edit failed chat=%d msg=%d: %v", st.ChatID, msgID, err)
	}
}

func sendRoleplayProposal(bot *tgbotapi.BotAPI, st roleplaySession, replyTo int, editExisting bool) bool {
	_ = editExisting
	m := tgbotapi.NewMessage(st.ChatID, roleplayProposalText(st))
	m.ParseMode = tgbotapi.ModeHTML
	m.DisableWebPagePreview = true
	m.ReplyToMessageID = replyTo
	m.AllowSendingWithoutReply = true
	m.ReplyMarkup = roleplayConfirmKeyboard(st.ID)
	if _, err := bot.Send(m); err != nil {
		log.Printf("roleplay proposal send failed chat=%d: %v", st.ChatID, err)
		reportChatFailure(bot, st.ChatID, "ошибка roleplay", err)
		return false
	}
	if st.SourceMsgID > 0 {
		deleteRoleplayCommandMessage(bot, st.ChatID, st.SourceMsgID)
	}
	return true
}

func deleteRoleplayCommandMessage(bot *tgbotapi.BotAPI, chatID int64, msgID int) {
	if bot == nil || chatID == 0 || msgID == 0 {
		return
	}
	if _, err := bot.Request(tgbotapi.DeleteMessageConfig{ChatID: chatID, MessageID: msgID}); err != nil && debugTriggerLogEnabled {
		log.Printf("roleplay command delete failed chat=%d msg=%d: %v", chatID, msgID, err)
	}
}

func editRoleplayProposal(bot *tgbotapi.BotAPI, st roleplaySession, msgID int) {
	edit := tgbotapi.NewEditMessageTextAndMarkup(st.ChatID, msgID, roleplayProposalText(st), roleplayConfirmKeyboard(st.ID))
	edit.ParseMode = tgbotapi.ModeHTML
	if _, err := bot.Request(edit); err != nil {
		log.Printf("roleplay proposal edit failed chat=%d msg=%d: %v", st.ChatID, msgID, err)
	}
}

func editRoleplayFinal(bot *tgbotapi.BotAPI, st roleplaySession, msgID int, declined bool) {
	text := roleplayFinalText(st, declined)
	edit := tgbotapi.NewEditMessageText(st.ChatID, msgID, text)
	edit.ParseMode = tgbotapi.ModeHTML
	edit.DisableWebPagePreview = true
	if _, err := bot.Request(edit); err != nil {
		log.Printf("roleplay final edit failed chat=%d msg=%d: %v", st.ChatID, msgID, err)
	}
}

func editRoleplayInlineFinal(bot *tgbotapi.BotAPI, st roleplaySession, inlineMessageID string, declined bool) {
	text, entities := roleplayInlineFinalContent(st, declined)
	edit := tgbotapi.EditMessageTextConfig{
		BaseEdit: tgbotapi.BaseEdit{
			InlineMessageID: inlineMessageID,
		},
		Text:                  text,
		Entities:              entities,
		DisableWebPagePreview: true,
	}
	if _, err := bot.Request(edit); err != nil {
		log.Printf("roleplay inline final edit failed inline=%s: %v", inlineMessageID, err)
	}
}

func roleplayInlineResults(q *tgbotapi.InlineQuery, limit int) []interface{} {
	if q == nil || q.From == nil {
		return nil
	}
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	query := normalizeRoleplayInlineQuery(q.Query)
	matched := roleplayInlineMatches(query, limit)
	results := make([]interface{}, 0, len(matched))
	for _, idx := range matched {
		a := roleplayActions[idx]
		target := roleplayInlineTargetHTML(query, a)
		st := defaultRoleplaySessions.create(roleplaySession{
			ActorID:     q.From.ID,
			ActorLink:   buildUserLink(q.From),
			TargetLink:  target,
			Inline:      true,
			ActionIndex: idx,
		})

		messageText, entities := roleplayInlineProposalContent(st)
		article := roleplayInlineArticleResult("rp_"+st.ID, roleplayInlineTitle(a), messageText, entities, roleplayInlineDescription(q.From, a, target))
		if thumbURL := roleplayInlineThumbURL(a); thumbURL != "" {
			article.ThumbURL = thumbURL
			article.ThumbWidth = 96
			article.ThumbHeight = 96
		}
		kb := roleplayConfirmKeyboard(st.ID)
		article.ReplyMarkup = &kb
		results = append(results, article)
	}
	return results
}

func roleplayInlineArticleResult(id, title, messageText string, entities []tgbotapi.MessageEntity, description string) tgbotapi.InlineQueryResultArticle {
	return tgbotapi.InlineQueryResultArticle{
		Type:        "article",
		ID:          id,
		Title:       title,
		Description: description,
		InputMessageContent: tgbotapi.InputTextMessageContent{
			Text:                  messageText,
			Entities:              entities,
			DisableWebPagePreview: true,
		},
	}
}

func normalizeRoleplayInlineQuery(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "/"+cmdRoleplay)
	s = strings.TrimPrefix(s, cmdRoleplay)
	return strings.TrimSpace(s)
}

func roleplayInlineMatches(query string, limit int) []int {
	query = strings.ToLower(strings.TrimSpace(query))
	if limit <= 0 {
		limit = 50
	}
	seen := make(map[int]bool)
	result := make([]int, 0, limit)
	add := func(i int) bool {
		if i < 0 || i >= len(roleplayActions) || seen[i] {
			return len(result) >= limit
		}
		seen[i] = true
		result = append(result, i)
		return len(result) >= limit
	}
	if query == "" {
		for i := range roleplayActions {
			if add(i) {
				break
			}
		}
		return result
	}
	for i, a := range roleplayActions {
		cmd := strings.ToLower(a.Command)
		if strings.HasPrefix(cmd, query) || strings.HasPrefix(query, cmd) {
			if add(i) {
				return result
			}
		}
	}
	for i, a := range roleplayActions {
		cmd := strings.ToLower(a.Command)
		if strings.Contains(cmd, query) || strings.Contains(query, cmd) {
			if add(i) {
				return result
			}
		}
	}
	return result
}

func roleplayInlineTargetHTML(query string, a roleplayAction) string {
	query = strings.TrimSpace(query)
	if query == "" {
		return "кого-то"
	}
	lowerQuery := strings.ToLower(query)
	lowerCmd := strings.ToLower(a.Command)
	if lowerQuery == lowerCmd {
		return "кого-то"
	}
	if strings.HasPrefix(lowerQuery, lowerCmd+" ") {
		target := strings.TrimSpace(query[len(a.Command):])
		if target != "" {
			return html.EscapeString(target)
		}
	}
	return "кого-то"
}

func roleplayInlineTitle(a roleplayAction) string {
	return strings.TrimSpace(a.Command)
}

func roleplayInlineThumbURL(a roleplayAction) string {
	if strings.TrimSpace(a.EmojiID) == "" {
		return ""
	}
	base := strings.TrimSpace(envOr("ROLEPLAY_INLINE_THUMB_BASE_URL", envOr("VOICE_TRANSLATE_PUBLIC_BASE_URL", "")))
	if base == "" {
		return ""
	}
	base = strings.TrimRight(base, "/")
	return fmt.Sprintf("%s/trigger_bot/static/roleplay/%s.jpg", base, a.EmojiID)
}

func roleplayInlineDescription(user *tgbotapi.User, a roleplayAction, target string) string {
	name := "Кто-то"
	if user != nil {
		name = roleplayDisplayName(user)
	}
	return fmt.Sprintf("%s %s %s", name, roleplayActionResult(a, ""), html.UnescapeString(target))
}

func roleplayInlineProposalContent(st roleplaySession) (string, []tgbotapi.MessageEntity) {
	if st.ActionIndex < 0 || st.ActionIndex >= len(roleplayActions) {
		return roleplayPlainText(roleplayPickerText(st)), nil
	}
	a := roleplayActions[st.ActionIndex]
	actor := roleplayPlainText(st.ActorLink)
	target := roleplayPlainText(st.TargetLink)
	text := fmt.Sprintf("%s | %s хочет: %s → %s", a.Emoji, actor, a.Command, target)
	return text, roleplayCustomEmojiEntities(a)
}

func roleplayInlineFinalContent(st roleplaySession, declined bool) (string, []tgbotapi.MessageEntity) {
	if declined {
		if st.ActionIndex >= 0 && st.ActionIndex < len(roleplayActions) {
			a := roleplayActions[st.ActionIndex]
			text := fmt.Sprintf("%s | %s не хочет %s %s", roleplayDeclineAction.Emoji, roleplayPlainText(st.TargetLink), a.Command, roleplayPlainText(st.ActorLink))
			return text, roleplayCustomEmojiEntities(roleplayDeclineAction)
		}
		text := fmt.Sprintf("%s | %s не хочет roleplay", roleplayDeclineAction.Emoji, roleplayPlainText(st.TargetLink))
		return text, roleplayCustomEmojiEntities(roleplayDeclineAction)
	}
	if st.ActionIndex < 0 || st.ActionIndex >= len(roleplayActions) {
		return roleplayPlainText(roleplayPickerText(st)), nil
	}
	a := roleplayActions[st.ActionIndex]
	text := fmt.Sprintf("%s | %s %s %s", a.Emoji, roleplayPlainText(st.ActorLink), roleplayActionResult(a, st.ActorTag), roleplayPlainText(st.TargetLink))
	return text, roleplayCustomEmojiEntities(a)
}

func roleplayCustomEmojiEntities(a roleplayAction) []tgbotapi.MessageEntity {
	if strings.TrimSpace(a.EmojiID) != "" && strings.TrimSpace(a.Emoji) != "" {
		return []tgbotapi.MessageEntity{{
			Type:          "custom_emoji",
			Offset:        0,
			Length:        roleplayUTF16Len(a.Emoji),
			CustomEmojiID: a.EmojiID,
		}}
	}
	return nil
}

func roleplayActionResult(a roleplayAction, actorTag string) string {
	result := strings.TrimSpace(resolveGenderVariant(actorTag, a.Result))
	if result != "" {
		return result
	}
	return strings.TrimSpace(a.Command)
}

func roleplayPlainText(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for {
		start := strings.Index(s, "<")
		if start < 0 {
			break
		}
		end := strings.Index(s[start:], ">")
		if end < 0 {
			break
		}
		s = s[:start] + s[start+end+1:]
	}
	return html.UnescapeString(strings.TrimSpace(s))
}

func roleplayUTF16Len(s string) int {
	return len(utf16.Encode([]rune(s)))
}

func roleplayDisplayName(user *tgbotapi.User) string {
	if user == nil {
		return "Кто-то"
	}
	name := strings.TrimSpace(strings.TrimSpace(user.FirstName + " " + user.LastName))
	if name != "" {
		return name
	}
	if strings.TrimSpace(user.UserName) != "" {
		return "@" + strings.TrimSpace(user.UserName)
	}
	return "Кто-то"
}

func roleplayPickerText(st roleplaySession) string {
	return fmt.Sprintf("%s выбирает действие для %s", st.ActorLink, st.TargetLink)
}

func roleplayProposalText(st roleplaySession) string {
	if st.ActionIndex < 0 || st.ActionIndex >= len(roleplayActions) {
		return roleplayPickerText(st)
	}
	a := roleplayActions[st.ActionIndex]
	return fmt.Sprintf("%s | %s хочет: <b>%s</b> → %s", roleplayEmojiHTML(a), st.ActorLink, html.EscapeString(a.Command), st.TargetLink)
}

func roleplayFinalText(st roleplaySession, declined bool) string {
	if declined {
		if st.ActionIndex >= 0 && st.ActionIndex < len(roleplayActions) {
			a := roleplayActions[st.ActionIndex]
			return fmt.Sprintf("%s | %s не хочет %s %s", roleplayEmojiHTML(roleplayDeclineAction), st.TargetLink, html.EscapeString(a.Command), st.ActorLink)
		}
		return fmt.Sprintf("%s | %s не хочет roleplay", roleplayEmojiHTML(roleplayDeclineAction), st.TargetLink)
	}
	a := roleplayActions[st.ActionIndex]
	return fmt.Sprintf("%s | %s %s %s", roleplayEmojiHTML(a), st.ActorLink, html.EscapeString(roleplayActionResult(a, st.ActorTag)), st.TargetLink)
}

func roleplayPickerKeyboard(id string, page int) tgbotapi.InlineKeyboardMarkup {
	if page < 0 {
		page = 0
	}
	totalPages := (len(roleplayActions) + roleplayPageSize - 1) / roleplayPageSize
	if page >= totalPages {
		page = totalPages - 1
	}
	start := page * roleplayPageSize
	end := start + roleplayPageSize
	if end > len(roleplayActions) {
		end = len(roleplayActions)
	}
	rows := make([][]tgbotapi.InlineKeyboardButton, 0, roleplayPageSize+2)
	for i := start; i < end; i++ {
		a := roleplayActions[i]
		btn := tgbotapi.NewInlineKeyboardButtonDataIcon(a.Command, fmt.Sprintf("rp|pick|%s|%d", id, i), a.EmojiID)
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(btn))
	}
	if totalPages > 1 {
		prev := page - 1
		if prev < 0 {
			prev = totalPages - 1
		}
		next := page + 1
		if next >= totalPages {
			next = 0
		}
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("◀️", fmt.Sprintf("rp|page|%s|%d", id, prev)),
			tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("%d/%d", page+1, totalPages), fmt.Sprintf("rp|page|%s|%d", id, page)),
			tgbotapi.NewInlineKeyboardButtonData("▶️", fmt.Sprintf("rp|page|%s|%d", id, next)),
		))
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonDataStyled("Отмена", "rp|decline|"+id, tgbotapi.ButtonStyleDanger),
	))
	return tgbotapi.NewInlineKeyboardMarkup(rows...)
}

func roleplayConfirmKeyboard(id string) tgbotapi.InlineKeyboardMarkup {
	accept := tgbotapi.NewInlineKeyboardButtonDataIconStyled("Принять", "rp|accept|"+id, "5289978022957440232", tgbotapi.ButtonStyleSuccess)
	decline := tgbotapi.NewInlineKeyboardButtonDataStyled("Отказаться", "rp|decline|"+id, tgbotapi.ButtonStyleDanger)
	return tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(accept, decline))
}

func roleplayEmojiHTML(a roleplayAction) string {
	if strings.TrimSpace(a.EmojiID) == "" {
		return html.EscapeString(a.Emoji)
	}
	return fmt.Sprintf("<tg-emoji emoji-id=\"%s\">%s</tg-emoji>", html.EscapeString(a.EmojiID), html.EscapeString(a.Emoji))
}

func findRoleplayAction(raw string) int {
	needle := strings.ToLower(strings.TrimSpace(raw))
	for i, a := range roleplayActions {
		if strings.EqualFold(a.Command, needle) {
			return i
		}
	}
	for i, a := range roleplayActions {
		if strings.HasPrefix(strings.ToLower(a.Command), needle) || strings.HasPrefix(needle, strings.ToLower(a.Command)) {
			return i
		}
	}
	return -1
}

func containsUnsafeRoleplayAction(raw string) bool {
	s := strings.ToLower(strings.TrimSpace(raw))
	return strings.Contains(s, "изнасил") || strings.Contains(s, "насил")
}

func answerRoleplayCallback(bot *tgbotapi.BotAPI, cb *tgbotapi.CallbackQuery, text string) {
	_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, text))
}
