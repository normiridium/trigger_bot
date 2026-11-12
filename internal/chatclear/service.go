package chatclear

import "context"

// Request is a clear-chat operation request sent to tg-ops-service.
type Request struct {
	ChatID   int64
	Username string
}

type HistoryRequest struct {
	ChatID       int64
	Username     string
	Limit        int
	SinceUnix    int64
	UntilUnix    int64
	FromID       int64
	FromUsername string
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

// DownloadMediaRequest описывает медиа сообщения, которое надо скачать.
type DownloadMediaRequest struct {
	ChatID    int64
	MessageID int
	MaxBytes  int64
}

// MediaFile — результат скачивания: локальный путь и метаданные.
type MediaFile struct {
	Path      string
	Size      int64
	MimeType  string
	FileName  string
	MediaType string
}

// Service executes chat clear operations.
type Service interface {
	Clear(ctx context.Context, req Request) error
	GetHistory(ctx context.Context, req HistoryRequest) (HistoryResult, error)
	// DownloadMedia тянет медиа через MTProto: Bot API отдаёт только до 20 МБ.
	DownloadMedia(ctx context.Context, req DownloadMediaRequest) (MediaFile, error)
	Available(ctx context.Context) bool

	// QR login (works when Telegram does not deliver login codes).
	StartQR(ctx context.Context, chatID int64) (QRState, error)
	QRState(ctx context.Context, chatID int64) (QRState, error)
	SubmitQRPassword(ctx context.Context, chatID int64, password string) (QRState, error)
	StopQR(ctx context.Context, chatID int64) error
}
