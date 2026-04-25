package yandexmusic

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	baseURL        = "https://api.music.yandex.net"
	defaultSignKey = "p93jhgh689SBReK6ghtw62"
)

type Client struct {
	httpClient *http.Client
	token      string
	tries      int
	retryDelay time.Duration
}

func NewClient(token string, timeoutSec, tries, retryDelaySec int) *Client {
	if timeoutSec <= 0 {
		timeoutSec = 20
	}
	if tries <= 0 {
		tries = 6
	}
	if retryDelaySec <= 0 {
		retryDelaySec = 2
	}
	return &Client{
		httpClient: &http.Client{Timeout: time.Duration(timeoutSec) * time.Second},
		token:      token,
		tries:      tries,
		retryDelay: time.Duration(retryDelaySec) * time.Second,
	}
}

func (c *Client) doJSON(ctx context.Context, req *http.Request, out any) error {
	if c.token != "" {
		req.Header.Set("Authorization", "OAuth "+c.token)
	}
	req.Header.Set("X-Yandex-Music-Client", "YandexMusicAndroid/24023621")
	req.Header.Set("User-Agent", "Yandex-Music-API")
	req.Header.Set("Accept-Language", "ru")
	req = req.WithContext(ctx)

	var lastErr error
	for attempt := 1; attempt <= c.tries; attempt++ {
		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
		} else {
			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr != nil {
				lastErr = readErr
			} else if resp.StatusCode < 200 || resp.StatusCode > 299 {
				lastErr = fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
			} else {
				if out == nil {
					return nil
				}
				var wrapped struct {
					Result json.RawMessage `json:"result"`
				}
				if err := json.Unmarshal(body, &wrapped); err == nil && len(wrapped.Result) > 0 && string(wrapped.Result) != "null" {
					return json.Unmarshal(wrapped.Result, out)
				}
				return json.Unmarshal(body, out)
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt < c.tries {
			time.Sleep(c.retryDelay)
		}
	}
	return lastErr
}

func (c *Client) doBytes(ctx context.Context, req *http.Request) ([]byte, error) {
	if c.token != "" {
		req.Header.Set("Authorization", "OAuth "+c.token)
	}
	req.Header.Set("X-Yandex-Music-Client", "YandexMusicAndroid/24023621")
	req.Header.Set("User-Agent", "Yandex-Music-API")
	req.Header.Set("Accept-Language", "ru")
	req = req.WithContext(ctx)

	var lastErr error
	for attempt := 1; attempt <= c.tries; attempt++ {
		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
		} else {
			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr != nil {
				lastErr = readErr
			} else if resp.StatusCode < 200 || resp.StatusCode > 299 {
				lastErr = fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
			} else {
				return body, nil
			}
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if attempt < c.tries {
			time.Sleep(c.retryDelay)
		}
	}
	return nil, lastErr
}

func (c *Client) postForm(ctx context.Context, path string, data url.Values, out any) error {
	req, err := http.NewRequest(http.MethodPost, baseURL+path, strings.NewReader(data.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.doJSON(ctx, req, out)
}

func (c *Client) get(ctx context.Context, path string, params url.Values, out any) error {
	u := baseURL + path
	if params != nil {
		u += "?" + params.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	return c.doJSON(ctx, req, out)
}

func (c *Client) Track(ctx context.Context, id int64) (*Track, error) {
	var out []Track
	err := c.postForm(ctx, "/tracks", url.Values{
		"track-ids":      {strconv.FormatInt(id, 10)},
		"with-positions": {"true"},
	}, &out)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("track not found")
	}
	return &out[0], nil
}

func (c *Client) TrackLyrics(ctx context.Context, id int64) (string, error) {
	var out struct {
		Lyrics struct {
			Short string `json:"lyrics"`
			Full  string `json:"fullLyrics"`
		} `json:"lyrics"`
	}
	if err := c.get(ctx, "/tracks/"+strconv.FormatInt(id, 10)+"/supplement", nil, &out); err != nil {
		return "", err
	}
	if v := strings.TrimSpace(out.Lyrics.Full); v != "" {
		return v, nil
	}
	return strings.TrimSpace(out.Lyrics.Short), nil
}

func (c *Client) SearchTracks(ctx context.Context, query string, page int) ([]Track, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("empty search query")
	}
	var out struct {
		Tracks struct {
			Results []Track `json:"results"`
		} `json:"tracks"`
	}
	if err := c.get(ctx, "/search", url.Values{
		"text": {query},
		"type": {"track"},
		"page": {strconv.Itoa(page)},
	}, &out); err != nil {
		return nil, err
	}
	return out.Tracks.Results, nil
}

func (c *Client) GetFileInfo(ctx context.Context, trackID int64, quality string) (*DownloadInfo, error) {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	codecs := "flac,flac-mp4,mp3,aac,he-aac,aac-mp4,he-aac-mp4"
	message := timestamp + strconv.FormatInt(trackID, 10) + quality + strings.ReplaceAll(codecs, ",", "") + "encraw"
	h := hmac.New(sha256.New, []byte(defaultSignKey))
	_, _ = h.Write([]byte(message))
	sign := base64.StdEncoding.EncodeToString(h.Sum(nil))
	if len(sign) > 0 {
		sign = sign[:len(sign)-1]
	}

	u, err := url.Parse(baseURL + "/get-file-info")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("ts", timestamp)
	q.Set("trackId", strconv.FormatInt(trackID, 10))
	q.Set("quality", quality)
	q.Set("codecs", codecs)
	q.Set("transports", "encraw")
	q.Set("sign", sign)
	u.RawQuery = q.Encode()

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		DownloadInfoSnake DownloadInfo `json:"download_info"`
		DownloadInfoCamel DownloadInfo `json:"downloadInfo"`
	}
	if err := c.doJSON(ctx, req, &out); err != nil {
		return nil, err
	}
	di := out.DownloadInfoSnake
	if di.Codec == "" {
		di = out.DownloadInfoCamel
	}
	if len(di.URLs) == 0 {
		return nil, errors.New("empty download urls")
	}
	return &di, nil
}

func (c *Client) DownloadTrack(ctx context.Context, di *DownloadInfo) ([]byte, error) {
	if di == nil || len(di.URLs) == 0 {
		return nil, errors.New("empty download info")
	}
	u := di.URLs[rand.Intn(len(di.URLs))]
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	data, err := c.doBytes(ctx, req)
	if err != nil {
		return nil, err
	}
	if di.Key == "" {
		return data, nil
	}
	key, err := hex.DecodeString(di.Key)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	iv := make([]byte, aes.BlockSize)
	stream := cipher.NewCTR(block, iv)
	decrypted := make([]byte, len(data))
	stream.XORKeyStream(decrypted, data)
	return decrypted, nil
}
