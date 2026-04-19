package app

import (
	"context"

	"trigger-admin-bot/internal/mediadl"
	"trigger-admin-bot/internal/model"
	"trigger-admin-bot/internal/spotifymusic"
	"trigger-admin-bot/internal/yandexmusic"
)

// TriggerStorePort describes trigger/template reads required by runtime handlers.
type TriggerStorePort interface {
	ListTriggers() ([]model.Trigger, error)
	ListTriggersCached() ([]model.Trigger, error)
	ListTemplates() ([]model.ResponseTemplate, error)
}

// ChatAdminStorePort describes admin cache persistence required by adminStatusCache.
type ChatAdminStorePort interface {
	GetChatAdminCache(chatID, userID int64) (isAdmin bool, updatedAt int64, ok bool, err error)
	GetChatAdminSync(chatID int64) (updatedAt int64, adminCount int, ok bool, err error)
	UpsertChatAdminSync(chatID int64, updatedAt int64, adminCount int) error
	UpsertChatAdminCache(chatID, userID int64, isAdmin bool, updatedAt int64) error
	ClearChatAdminCache(chatID int64) error
}

// SpotifyMusicPort describes spotify metadata operations used by application layer.
type SpotifyMusicPort interface {
	Enabled() bool
	SearchTracks(ctx context.Context, query string, limit int) ([]spotifymusic.Track, error)
	GetTrack(ctx context.Context, id string) (*spotifymusic.Track, error)
}

// SpotifyDownloadPort describes spotify audio download operation used by workers.
type SpotifyDownloadPort interface {
	DownloadByQuery(ctx context.Context, query string) (string, error)
}

// YandexMusicDownloadPort describes yandex music audio download operation used by workers.
type YandexMusicDownloadPort interface {
	DownloadByURL(ctx context.Context, rawURL string) (string, error)
	SearchTracks(ctx context.Context, query string, limit int) ([]yandexmusic.SearchTrack, error)
}

// MediaDownloadPort describes media download operations (audio/video/auto) used by workers.
type MediaDownloadPort interface {
	DownloadAudioFromURL(ctx context.Context, rawURL string) (mediadl.DownloadResult, error)
	DownloadVideoFromURL(ctx context.Context, rawURL string) (mediadl.DownloadResult, error)
	DownloadMediaAutoFromURL(ctx context.Context, rawURL string) (mediadl.DownloadResult, error)
	ConfiguredMaxSizeMB() int
	ConfiguredMaxHeight() int
}
