package vkaudio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/proxy"
	"golang.org/x/text/encoding/charmap"
)

const vkWebBase = "https://vk.com"

type webAudioTuple struct {
	ID         int
	OwnerID    int
	URL        string
	Title      string
	Artist     string
	Duration   int
	ActionHash string
	URLHash    string
}

type vkAjaxResponse struct {
	Payload []json.RawMessage `json:"payload"`
}

func (d Downloader) webSearchTracks(ctx context.Context, query string, limit int) ([]Track, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("empty query")
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 30 {
		limit = 30
	}
	wc, err := d.newWebClient(ctx)
	if err != nil {
		return nil, err
	}
	ownerID, err := wc.userID(ctx)
	if err != nil {
		return nil, err
	}
	form := url.Values{}
	form.Set("act", "load_section")
	form.Set("type", "search")
	form.Set("owner_id", strconv.Itoa(ownerID))
	form.Set("playlist_id", "-1")
	form.Set("offset", "0")
	form.Set("context", "search")
	form.Set("access_hash", "")
	form.Set("search_q", query)
	form.Set("search_performer", "0")
	form.Set("search_lyrics", "0")
	form.Set("search_sort", "0")
	form.Set("search_history", "0")
	form.Set("search_qid", "")
	form.Set("feed_from", "")
	form.Set("feed_offset", "")
	form.Set("post_id", "")
	form.Set("wall_query", "")
	form.Set("wall_type", "")
	form.Set("claim", "0")
	form.Set("track_type", "default")
	form.Set("ref", "search")

	payload, err := wc.postAjax(ctx, "/al_audio.php", form)
	if err != nil {
		return nil, err
	}
	tuples := collectAudioTuples(payload)
	if len(tuples) > limit {
		tuples = tuples[:limit]
	}
	out := make([]Track, 0, len(tuples))
	for _, t := range tuples {
		out = append(out, Track{
			ID:          fmt.Sprintf("%d_%d", t.OwnerID, t.ID),
			Artist:      strings.TrimSpace(t.Artist),
			Title:       strings.TrimSpace(t.Title),
			DurationSec: float64(t.Duration),
		})
	}
	return out, nil
}

func (d Downloader) webDownloadTrack(ctx context.Context, trackID string) (DownloadResult, error) {
	wc, err := d.newWebClient(ctx)
	if err != nil {
		return DownloadResult{}, err
	}
	form := url.Values{}
	form.Set("audio_ids", trackID)
	payload, err := wc.postAjax(ctx, "/al_audio.php?act=reload_audios", form)
	if err != nil {
		return DownloadResult{}, err
	}
	tuples := collectAudioTuples(payload)
	if len(tuples) == 0 {
		return DownloadResult{}, errors.New("vk web returned no audio tuples")
	}
	var lastErr error
	for _, t := range tuples {
		audioURL := unmaskVKAudioURL(t.URL, wc.userIDCached())
		if !strings.HasPrefix(audioURL, "http") || strings.Contains(audioURL, "audio_api_unavailable") {
			lastErr = errors.New("vk web returned masked/empty audio url")
			continue
		}
		path, err := d.downloadAudioURL(ctx, audioURL)
		if err != nil {
			lastErr = err
			continue
		}
		return DownloadResult{
			FilePath: path,
			Artist:   strings.TrimSpace(t.Artist),
			Title:    strings.TrimSpace(t.Title),
			TrackID:  fmt.Sprintf("%d_%d", t.OwnerID, t.ID),
		}, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no downloadable VK web tracks found")
	}
	return DownloadResult{}, lastErr
}

type vkWebClient struct {
	http      *http.Client
	ua        string
	userIDVal int
	cookieOK  bool
}

func (d Downloader) newWebClient(ctx context.Context) (*vkWebClient, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	if err := loadNetscapeCookies(jar, strings.TrimSpace(d.CookiesFile)); err != nil {
		return nil, err
	}
	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}
	if proxyURL := normalizeProxyURL(strings.TrimSpace(d.ProxyURL)); proxyURL != "" {
		dialer, err := socks5Dialer(proxyURL)
		if err != nil {
			return nil, err
		}
		tr.Proxy = nil
		tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			type contextDialer interface {
				DialContext(context.Context, string, string) (net.Conn, error)
			}
			if cd, ok := dialer.(contextDialer); ok {
				return cd.DialContext(ctx, network, address)
			}
			return dialer.Dial(network, address)
		}
	}
	wc := &vkWebClient{
		http: &http.Client{
			Timeout:   30 * time.Second,
			Jar:       jar,
			Transport: tr,
		},
		ua:        firstNonEmpty(strings.TrimSpace(d.UserAgent), "Mozilla/5.0 (X11; Linux x86_64; rv:126.0) Gecko/20100101 Firefox/126.0"),
		userIDVal: d.WebUserID,
	}
	if d.WebUserID <= 0 {
		_, _ = wc.userID(ctx)
	}
	return wc, nil
}

func (wc *vkWebClient) userIDCached() int {
	return wc.userIDVal
}

func (wc *vkWebClient) userID(ctx context.Context) (int, error) {
	if wc.userIDVal > 0 {
		return wc.userIDVal, nil
	}
	body, err := wc.getText(ctx, vkWebBase+"/audios")
	if err != nil {
		return 0, err
	}
	for _, needle := range []string{"id: ", `"id":`} {
		if idx := strings.Index(body, needle); idx >= 0 {
			tail := body[idx+len(needle):]
			n := 0
			for n < len(tail) && tail[n] >= '0' && tail[n] <= '9' {
				n++
			}
			if n > 0 {
				id, _ := strconv.Atoi(tail[:n])
				if id > 0 {
					wc.userIDVal = id
					return id, nil
				}
			}
		}
	}
	return 0, errors.New("cannot detect VK web user id from cookies")
}

func (wc *vkWebClient) postAjax(ctx context.Context, path string, form url.Values) (json.RawMessage, error) {
	var payload json.RawMessage
	var code int
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		payload, code, err = wc.postAjaxOnce(ctx, path, form)
		if err != nil {
			return nil, err
		}
		if code != 3 {
			break
		}
		if err := wc.relogin(ctx, payload); err != nil {
			return nil, err
		}
	}
	if code != 0 {
		return nil, fmt.Errorf("vk web ajax code=%d", code)
	}
	return payload, nil
}

func (wc *vkWebClient) postAjaxOnce(ctx context.Context, path string, form url.Values) (json.RawMessage, int, error) {
	if form == nil {
		form = url.Values{}
	}
	form.Set("al", "1")
	endpoint := vkWebBase + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", wc.ua)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Origin", vkWebBase)
	req.Header.Set("Referer", vkWebBase+"/audios")
	resp, err := wc.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := readVKBody(resp.Body)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, 0, fmt.Errorf("vk web status=%d", resp.StatusCode)
	}
	body = strings.TrimPrefix(body, "<!--")
	var parsed vkAjaxResponse
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return nil, 0, fmt.Errorf("vk web bad json: %w", err)
	}
	if len(parsed.Payload) < 2 {
		return nil, 0, errors.New("vk web empty ajax payload")
	}
	var code int
	if err := json.Unmarshal(parsed.Payload[0], &code); err != nil {
		var codeStr string
		if strErr := json.Unmarshal(parsed.Payload[0], &codeStr); strErr != nil {
			return nil, 0, err
		}
		code, _ = strconv.Atoi(codeStr)
	}
	return parsed.Payload[1], code, nil
}

func (wc *vkWebClient) relogin(ctx context.Context, payload json.RawMessage) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		return fmt.Errorf("vk relogin payload: %w", err)
	}
	if len(raw) < 3 {
		return errors.New("vk relogin payload is too short")
	}
	vals := make([]string, 3)
	for i := 0; i < 3; i++ {
		if err := json.Unmarshal(raw[i], &vals[i]); err != nil {
			return err
		}
		vals[i] = decodePossiblyNestedJSONString(vals[i])
	}
	q := url.Values{}
	q.Set("role", "al_frame")
	q.Set("_origin", vkWebBase)
	q.Set("ip_h", vals[0])
	q.Set("to", vals[1])
	q.Set("lrt", vals[2])
	_, err := wc.getText(ctx, "https://login.vk.com/?"+q.Encode())
	return err
}

func decodePossiblyNestedJSONString(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return s
	}
	var out string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return s
	}
	return out
}

func (wc *vkWebClient) getText(ctx context.Context, endpoint string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", wc.ua)
	req.Header.Set("Referer", vkWebBase+"/")
	resp, err := wc.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := readVKBody(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("vk web status=%d", resp.StatusCode)
	}
	return body, nil
}

func readVKBody(r io.Reader) (string, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	if out, decErr := charmap.Windows1251.NewDecoder().Bytes(b); decErr == nil {
		return string(out), nil
	}
	return string(b), nil
}

func loadNetscapeCookies(jar *cookiejar.Jar, path string) error {
	if path == "" {
		return errors.New("VK cookies file is not configured")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	byHost := map[string][]*http.Cookie{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 7 {
			continue
		}
		domain := strings.TrimPrefix(parts[0], ".")
		name := strings.TrimSpace(parts[5])
		value := parts[6]
		if domain == "" || name == "" {
			continue
		}
		ck := &http.Cookie{
			Name:   name,
			Value:  value,
			Path:   firstNonEmpty(parts[2], "/"),
			Domain: domain,
		}
		if exp, err := strconv.ParseInt(parts[4], 10, 64); err == nil && exp > 0 {
			ck.Expires = time.Unix(exp, 0)
		}
		byHost[domain] = append(byHost[domain], ck)
	}
	for host, cookies := range byHost {
		u := &url.URL{Scheme: "https", Host: host, Path: "/"}
		jar.SetCookies(u, cookies)
	}
	return nil
}

func socks5Dialer(raw string) (proxy.Dialer, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "socks5" && u.Scheme != "socks5h" {
		proxyURL, err := url.Parse(raw)
		if err != nil {
			return nil, err
		}
		return proxy.FromURL(proxyURL, proxy.Direct)
	}
	var auth *proxy.Auth
	if u.User != nil {
		pass, _ := u.User.Password()
		auth = &proxy.Auth{User: u.User.Username(), Password: pass}
	}
	return proxy.SOCKS5("tcp", u.Host, auth, proxy.Direct)
}

func normalizeProxyURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "://") {
		return raw
	}
	return "socks5h://" + raw
}

func collectAudioTuples(raw json.RawMessage) []webAudioTuple {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	var out []webAudioTuple
	var walk func(any)
	walk = func(x any) {
		if len(out) >= 50 {
			return
		}
		switch val := x.(type) {
		case []any:
			if t, ok := parseAudioTuple(val); ok {
				out = append(out, t)
				return
			}
			for _, item := range val {
				walk(item)
			}
		case map[string]any:
			for _, item := range val {
				walk(item)
			}
		}
	}
	walk(v)
	return out
}

func parseAudioTuple(v []any) (webAudioTuple, bool) {
	if len(v) < 21 {
		return webAudioTuple{}, false
	}
	id, okID := numberToInt(v[0])
	owner, okOwner := numberToInt(v[1])
	title, okTitle := v[3].(string)
	artist, okArtist := v[4].(string)
	if !okID || !okOwner || !okTitle || !okArtist {
		return webAudioTuple{}, false
	}
	duration, _ := numberToInt(v[5])
	urlStr, _ := v[2].(string)
	actionHash, _ := v[13].(string)
	urlHash, _ := v[20].(string)
	return webAudioTuple{
		ID:         id,
		OwnerID:    owner,
		URL:        strings.TrimSpace(urlStr),
		Title:      strings.TrimSpace(title),
		Artist:     strings.TrimSpace(artist),
		Duration:   duration,
		ActionHash: strings.TrimSpace(actionHash),
		URLHash:    strings.TrimSpace(urlHash),
	}, true
}

func numberToInt(v any) (int, bool) {
	switch x := v.(type) {
	case float64:
		if math.Trunc(x) == x {
			return int(x), true
		}
	case int:
		return x, true
	case json.Number:
		i, err := x.Int64()
		return int(i), err == nil
	}
	return 0, false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
