package chatclear

import "context"

// Request is a clear-chat operation request sent to tg-ops-service.
type Request struct {
	ChatID   int64
	Username string
}

type AuthStartRequest struct {
	ChatID int64
	Phone  string
}

type AuthStartResult struct {
	ChallengeID string
}

type AuthCompleteRequest struct {
	ChallengeID string
	Code        string
	Password    string
}

type AuthCompleteResult struct {
	ChatID     int64
	AccessHash int64
}

type HistoryRequest struct {
	ChatID    int64
	Username  string
	Limit     int
	SinceUnix int64
	UntilUnix int64
}

type HistoryMessage struct {
	ID             int    `json:"id"`
	Date           int64  `json:"date"`
	FromID         int64  `json:"from_id,omitempty"`
	FromUsername   string `json:"from_username,omitempty"`
	FromFirstName  string `json:"from_first_name,omitempty"`
	FromLastName   string `json:"from_last_name,omitempty"`
	FromTitle      string `json:"from_title,omitempty"`
	ReplyToMessage int    `json:"reply_to_message_id,omitempty"`
	Text           string `json:"text"`
}

type HistoryResult struct {
	Messages []HistoryMessage
}

// Service executes chat clear operations.
type Service interface {
	Clear(ctx context.Context, req Request) error
	StartAuth(ctx context.Context, req AuthStartRequest) (AuthStartResult, error)
	CompleteAuth(ctx context.Context, req AuthCompleteRequest) (AuthCompleteResult, error)
	GetHistory(ctx context.Context, req HistoryRequest) (HistoryResult, error)
	Available(ctx context.Context) bool
}
