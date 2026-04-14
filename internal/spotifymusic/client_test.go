package spotifymusic

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNewAndEnabled(t *testing.T) {
	c := New(" id ", " secret ")
	if c == nil {
		t.Fatal("expected non-nil client")
	}
	if !c.Enabled() {
		t.Fatal("expected client to be enabled")
	}
	if New("", "secret").Enabled() {
		t.Fatal("expected disabled when client id is empty")
	}
	if New("id", "").Enabled() {
		t.Fatal("expected disabled when secret is empty")
	}
}

func TestBuildSearchQuery(t *testing.T) {
	if got := BuildSearchQuery(nil); got != "" {
		t.Fatalf("expected empty query for nil track, got %q", got)
	}
	if got := BuildSearchQuery(&Track{Title: " Song ", Artist: " Artist "}); got != "Artist - Song" {
		t.Fatalf("unexpected query: %q", got)
	}
	if got := BuildSearchQuery(&Track{Title: " Song "}); got != "Song" {
		t.Fatalf("unexpected title-only query: %q", got)
	}
	if got := BuildSearchQuery(&Track{Artist: " Artist "}); got != "Artist" {
		t.Fatalf("unexpected artist-only query: %q", got)
	}
}

func TestSearchTracks_ParsesAndCapsLimit(t *testing.T) {
	var gotURL string
	c := New("id", "secret")
	c.token = "tok"
	c.expiresAt = time.Now().Add(5 * time.Minute)
	c.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotURL = r.URL.String()
		body := `{"tracks":{"items":[{"id":"1","name":"Song","duration_ms":210000,"artists":[{"name":"A"},{"name":"B"}]},{"id":"2","name":"Solo","duration_ms":120000,"artists":[{"name":"Z"}]}]}}`
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}

	items, err := c.SearchTracks(context.Background(), "  mega  ", 999)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].Artist != "A, B" {
		t.Fatalf("unexpected artists: %q", items[0].Artist)
	}
	if !strings.Contains(gotURL, "limit=50") {
		t.Fatalf("expected capped limit=50 in url, got %q", gotURL)
	}
	if !strings.Contains(gotURL, "market=US") {
		t.Fatalf("expected market=US in url, got %q", gotURL)
	}
}

func TestSearchTracks_EmptyQueryReturnsNil(t *testing.T) {
	calls := 0
	c := New("id", "secret")
	c.token = "tok"
	c.expiresAt = time.Now().Add(5 * time.Minute)
	c.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("boom")), Header: make(http.Header)}, nil
	})}

	items, err := c.SearchTracks(context.Background(), "   ", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if items != nil {
		t.Fatalf("expected nil items for empty query")
	}
	if calls != 0 {
		t.Fatalf("expected no network calls, got %d", calls)
	}
}

func TestDoJSON_Non2xxIncludesBody(t *testing.T) {
	c := New("id", "secret")
	c.token = "tok"
	c.expiresAt = time.Now().Add(5 * time.Minute)
	c.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"forbidden"}`))}, nil
	})}

	err := c.doJSON(context.Background(), http.MethodGet, "https://example.test", nil, &struct{}{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "spotify api error") || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGetTrack_EmptyID(t *testing.T) {
	c := New("id", "secret")
	_, err := c.GetTrack(context.Background(), " ")
	if err == nil || !strings.Contains(err.Error(), "empty track id") {
		t.Fatalf("expected empty track id error, got: %v", err)
	}
}
