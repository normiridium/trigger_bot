package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type openAICostSummary struct {
	Total          float64
	Today          float64
	Last7Days      float64
	AveragePerDay  float64
	ProjectedMonth float64
	PeakDay        time.Time
	PeakDayCost    float64
	Currency       string
	StartTime      time.Time
	EndTime        time.Time
	Buckets        int
}

func canUseSensitiveBotAdminCommand(bot *tgbotapi.BotAPI, adminCache *adminStatusCache, msg *tgbotapi.Message) bool {
	if msg == nil || msg.Chat == nil || msg.From == nil || msg.From.ID == 0 {
		return false
	}
	if isConfiguredBotOwner(msg.From.ID) {
		return true
	}
	if msg.Chat.IsPrivate() {
		return false
	}
	_ = syncAdminCacheForUser(bot, adminCache, msg.Chat.ID, msg.From.ID)
	return adminCache != nil && adminCache.IsChatAdmin(bot, msg.Chat.ID, msg.From.ID)
}

func isConfiguredBotOwner(userID int64) bool {
	if userID == 0 {
		return false
	}
	raw := strings.TrimSpace(os.Getenv("OWNER_ID"))
	if extra := strings.TrimSpace(os.Getenv("BOT_ADMIN_USER_IDS")); extra != "" {
		if raw != "" {
			raw += ","
		}
		raw += extra
	}
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case ',', ';', ' ', '\n', '\r', '\t':
			return true
		default:
			return false
		}
	}) {
		id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err == nil && id == userID {
			return true
		}
	}
	return false
}

func fetchOpenAICurrentMonthCosts(now time.Time) (openAICostSummary, error) {
	adminKey := strings.TrimSpace(os.Getenv("OPENAI_ADMIN_KEY"))
	if adminKey == "" {
		return openAICostSummary{}, errors.New("OPENAI_ADMIN_KEY is empty")
	}
	if now.IsZero() {
		now = time.Now()
	}
	end := now.UTC()
	start := time.Date(end.Year(), end.Month(), 1, 0, 0, 0, 0, time.UTC)
	if !start.Before(end) {
		start = end.Add(-24 * time.Hour)
	}

	params := url.Values{}
	params.Set("start_time", strconv.FormatInt(start.Unix(), 10))
	params.Set("end_time", strconv.FormatInt(end.Unix(), 10))
	params.Set("bucket_width", "1d")
	params.Set("limit", "31")

	req, err := http.NewRequest(http.MethodGet, "https://api.openai.com/v1/organization/costs?"+params.Encode(), nil)
	if err != nil {
		return openAICostSummary{}, err
	}
	req.Header.Set("Authorization", "Bearer "+adminKey)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return openAICostSummary{}, err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return openAICostSummary{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return openAICostSummary{}, fmt.Errorf("openai costs status=%d body=%s", resp.StatusCode, clipText(sanitizeSecretText(string(bodyBytes)), 600))
	}

	return parseOpenAICostPayload(bodyBytes, start, end)
}

func parseOpenAICostPayload(bodyBytes []byte, start, end time.Time) (openAICostSummary, error) {
	var payload struct {
		Data []struct {
			StartTime int64 `json:"start_time"`
			EndTime   int64 `json:"end_time"`
			Results   []struct {
				Amount struct {
					Value    json.RawMessage `json:"value"`
					Currency string          `json:"currency"`
				} `json:"amount"`
			} `json:"results"`
		} `json:"data"`
	}
	if err := json.Unmarshal(bodyBytes, &payload); err != nil {
		return openAICostSummary{}, err
	}
	start = start.UTC()
	end = end.UTC()
	todayStart := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, time.UTC)
	last7Start := end.AddDate(0, 0, -7)
	out := openAICostSummary{
		StartTime: start,
		EndTime:   end,
		Buckets:   len(payload.Data),
	}
	for _, bucket := range payload.Data {
		bucketStart := time.Unix(bucket.StartTime, 0).UTC()
		bucketTotal := 0.0
		for _, result := range bucket.Results {
			value, err := parseOpenAICostValue(result.Amount.Value)
			if err != nil {
				return openAICostSummary{}, err
			}
			bucketTotal += value
			if out.Currency == "" {
				out.Currency = strings.ToUpper(strings.TrimSpace(result.Amount.Currency))
			}
		}
		out.Total += bucketTotal
		if !bucketStart.Before(todayStart) {
			out.Today += bucketTotal
		}
		if !bucketStart.Before(last7Start) {
			out.Last7Days += bucketTotal
		}
		if bucketTotal > out.PeakDayCost {
			out.PeakDay = bucketStart
			out.PeakDayCost = bucketTotal
		}
	}
	if out.Currency == "" {
		out.Currency = "USD"
	}
	elapsedDays := end.Sub(start).Hours() / 24
	if elapsedDays > 0 {
		out.AveragePerDay = out.Total / elapsedDays
	}
	daysInMonth := time.Date(start.Year(), start.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if out.AveragePerDay > 0 && daysInMonth > 0 {
		out.ProjectedMonth = out.AveragePerDay * float64(daysInMonth)
	}
	return out, nil
}

func parseOpenAICostValue(raw json.RawMessage) (float64, error) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return 0, nil
	}
	var number float64
	if err := json.Unmarshal(raw, &number); err == nil {
		return number, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0, fmt.Errorf("unexpected openai cost value: %s", clipText(s, 80))
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, nil
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, fmt.Errorf("parse openai cost value %q: %w", clipText(text, 80), err)
	}
	return value, nil
}

func formatOpenAIBalanceMessage(costs openAICostSummary) string {
	month := costs.StartTime.Format("2006-01")
	currency := strings.ToUpper(strings.TrimSpace(costs.Currency))
	if currency == "" {
		currency = "USD"
	}
	lines := []string{
		"OpenAI API",
		fmt.Sprintf("Траты за %s: %.4f %s", month, costs.Total, currency),
		fmt.Sprintf("Сегодня: %.4f %s", costs.Today, currency),
		fmt.Sprintf("Последние 7 дней: %.4f %s", costs.Last7Days, currency),
		fmt.Sprintf("Среднее в день: %.4f %s", costs.AveragePerDay, currency),
		fmt.Sprintf("Прогноз месяца: %.4f %s", costs.ProjectedMonth, currency),
	}
	if !costs.PeakDay.IsZero() {
		lines = append(lines, fmt.Sprintf("Пиковый день: %s — %.4f %s", costs.PeakDay.Format("2006-01-02"), costs.PeakDayCost, currency))
	}
	lines = append(lines,
		fmt.Sprintf("Период: %s — %s UTC", costs.StartTime.Format("2006-01-02 15:04"), costs.EndTime.Format("2006-01-02 15:04")),
		"",
		"Остаток/кредиты OpenAI через Admin API не отдаёт, поэтому показываю текущие расходы.",
	)
	return strings.Join(lines, "\n")
}
