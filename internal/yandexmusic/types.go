package yandexmusic

import (
	"encoding/json"
	"strconv"
	"strings"
)

type YMInt64 int64

func (v *YMInt64) UnmarshalJSON(data []byte) error {
	var n int64
	if err := json.Unmarshal(data, &n); err == nil {
		*v = YMInt64(n)
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	if i := strings.IndexByte(s, ':'); i >= 0 {
		s = s[:i]
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return err
	}
	*v = YMInt64(n)
	return nil
}

type ArtistBrief struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type AlbumBrief struct {
	ID int `json:"id"`
}

type Track struct {
	ID         YMInt64       `json:"id"`
	Title      string        `json:"title"`
	Available  bool          `json:"available"`
	DurationMs int64         `json:"durationMs"`
	Artists    []ArtistBrief `json:"artists"`
	Albums     []AlbumBrief  `json:"albums"`
}

type DownloadInfo struct {
	Quality string   `json:"quality"`
	Codec   string   `json:"codec"`
	URLs    []string `json:"urls"`
	Key     string   `json:"key"`
	Bitrate int      `json:"bitrate"`
}

type SearchTrack struct {
	ID          int64
	Artist      string
	Title       string
	URL         string
	DurationSec float64
}
