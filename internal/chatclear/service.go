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

// Service executes chat clear operations.
type Service interface {
	Clear(ctx context.Context, req Request) error
	StartAuth(ctx context.Context, req AuthStartRequest) (AuthStartResult, error)
	CompleteAuth(ctx context.Context, req AuthCompleteRequest) (AuthCompleteResult, error)
	Available(ctx context.Context) bool
}
