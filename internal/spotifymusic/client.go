package spotifymusic

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	spotifyHTTPTimeout    = 10 * time.Second
	spotifyRequestRetries = 3
	spotifyRetryDelay     = 250 * time.Millisecond
)

type Track struct {
	ID          string
	Title       string
	Artist      string
	DurationSec float64
}

type Client struct {
	httpClient *http.Client
	clientID   string
	secret     string

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

func New(clientID, secret string) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: spotifyHTTPTimeout},
		clientID:   strings.TrimSpace(clientID),
		secret:     strings.TrimSpace(secret),
	}
}

func (c *Client) Enabled() bool {
	return c != nil && c.clientID != "" && c.secret != ""
}

func (c *Client) SearchTracks(ctx context.Context, query string, limit int) ([]Track, error) {
	if !c.Enabled() {
		return nil, errors.New("spotify client is not configured")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	u := "https://api.spotify.com/v1/search?type=track&market=US&limit=" +
		url.QueryEscape(fmt.Sprintf("%d", limit)) + "&q=" + url.QueryEscape(query)

	var payload struct {
		Tracks struct {
			Items []struct {
				ID         string `json:"id"`
				Name       string `json:"name"`
				DurationMS int64  `json:"duration_ms"`
				Artists    []struct {
					Name string `json:"name"`
				} `json:"artists"`
			} `json:"items"`
		} `json:"tracks"`
	}
	if err := c.doJSON(ctx, http.MethodGet, u, nil, &payload); err != nil {
		return nil, err
	}
	out := make([]Track, 0, len(payload.Tracks.Items))
	for _, item := range payload.Tracks.Items {
		artists := make([]string, 0, len(item.Artists))
		for _, a := range item.Artists {
			name := strings.TrimSpace(a.Name)
			if name != "" {
				artists = append(artists, name)
			}
		}
		out = append(out, Track{
			ID:          strings.TrimSpace(item.ID),
			Title:       strings.TrimSpace(item.Name),
			Artist:      strings.Join(artists, ", "),
			DurationSec: float64(item.DurationMS) / 1000.0,
		})
	}
	return out, nil
}

func (c *Client) GetTrack(ctx context.Context, id string) (*Track, error) {
	if !c.Enabled() {
		return nil, errors.New("spotify client is not configured")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("empty track id")
	}
	u := "https://api.spotify.com/v1/tracks/" + url.PathEscape(id)
	var payload struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		DurationMS int64  `json:"duration_ms"`
		Artists    []struct {
			Name string `json:"name"`
		} `json:"artists"`
	}
	if err := c.doJSON(ctx, http.MethodGet, u, nil, &payload); err != nil {
		return nil, err
	}
	artists := make([]string, 0, len(payload.Artists))
	for _, a := range payload.Artists {
		name := strings.TrimSpace(a.Name)
		if name != "" {
			artists = append(artists, name)
		}
	}
	return &Track{
		ID:          strings.TrimSpace(payload.ID),
		Title:       strings.TrimSpace(payload.Name),
		Artist:      strings.Join(artists, ", "),
		DurationSec: float64(payload.DurationMS) / 1000.0,
	}, nil
}

func BuildSearchQuery(track *Track) string {
	if track == nil {
		return ""
	}
	title := strings.TrimSpace(track.Title)
	artist := strings.TrimSpace(track.Artist)
	if title == "" {
		return artist
	}
	if artist == "" {
		return title
	}
	return artist + " - " + title
}

func ExtractTrackID(input string) (string, bool) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", false
	}
	lower := strings.ToLower(input)
	if strings.HasPrefix(lower, "spotify:track:") {
		id := strings.TrimSpace(input[len("spotify:track:"):])
		return id, id != ""
	}
	u, err := url.Parse(input)
	if err != nil {
		return "", false
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if host != "open.spotify.com" && host != "play.spotify.com" {
		return "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || strings.ToLower(parts[0]) != "track" {
		return "", false
	}
	id := strings.TrimSpace(parts[1])
	if id == "" {
		return "", false
	}
	return id, true
}

func (c *Client) doJSON(ctx context.Context, method, endpoint string, body io.Reader, out any) error {
	token, err := c.ensureToken(ctx)
	if err != nil {
		return err
	}
	resp, err := c.doWithRetry(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return req, nil
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("spotify api error (%d): %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) ensureToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Until(c.expiresAt) > 30*time.Second {
		return c.token, nil
	}
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	auth := base64.StdEncoding.EncodeToString([]byte(c.clientID + ":" + c.secret))
	resp, err := c.doWithRetry(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://accounts.spotify.com/api/token", strings.NewReader(form.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Authorization", "Basic "+auth)
		return req, nil
	})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("spotify token error (%d): %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var payload struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return "", errors.New("spotify token missing access_token")
	}
	c.token = strings.TrimSpace(payload.AccessToken)
	c.expiresAt = time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second)
	return c.token, nil
}

func (c *Client) doWithRetry(ctx context.Context, build func() (*http.Request, error)) (*http.Response, error) {
	var lastErr error
	for attempt := 1; attempt <= spotifyRequestRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, err
		}
		req, err := build()
		if err != nil {
			return nil, err
		}
		resp, err := c.httpClient.Do(req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if attempt == spotifyRequestRetries || ctx.Err() != nil || !isSpotifyRetryableRequestError(err) {
			return nil, err
		}
		if err := sleepSpotifyRetry(ctx, attempt); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func isSpotifyRetryableRequestError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	errText := strings.ToLower(err.Error())
	return strings.Contains(errText, "timeout") ||
		strings.Contains(errText, "deadline exceeded") ||
		strings.Contains(errText, "connection reset") ||
		strings.Contains(errText, "connection refused") ||
		strings.Contains(errText, "temporary failure")
}

func sleepSpotifyRetry(ctx context.Context, attempt int) error {
	if attempt < 1 {
		attempt = 1
	}
	timer := time.NewTimer(time.Duration(attempt) * spotifyRetryDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
