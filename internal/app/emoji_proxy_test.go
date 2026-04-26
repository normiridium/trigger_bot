package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type rewriteHostTransport struct {
	base *url.URL
	rt   http.RoundTripper
}

func (t rewriteHostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = t.base.Scheme
	clone.URL.Host = t.base.Host
	return t.rt.RoundTrip(clone)
}

func testEmojiProxyClient(ts *httptest.Server) *http.Client {
	u, _ := url.Parse(ts.URL)
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: rewriteHostTransport{
			base: u,
			rt:   http.DefaultTransport,
		},
	}
}

func TestResolveSetByEmojiID_PreservesTelegramOrder(t *testing.T) {
	token := "TEST_TOKEN"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bot" + token + "/getCustomEmojiStickers":
			_, _ = io.WriteString(w, `{"ok":true,"result":[{"custom_emoji_id":"2","set_name":"myset","emoji":"🎉","file_id":"f2"}]}`)
		case "/bot" + token + "/getStickerSet":
			_, _ = io.WriteString(w, `{"ok":true,"result":{"name":"myset","title":"My Set","stickers":[{"custom_emoji_id":"2","set_name":"myset","emoji":"🎉","file_id":"f2"},{"custom_emoji_id":"10","set_name":"myset","emoji":"😀","file_id":"f10"}]}}`)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	svc := emojiProxyService{
		Token:  token,
		Client: testEmojiProxyClient(ts),
	}

	set, err := svc.ResolveSetByEmojiID(context.Background(), "2")
	if err != nil {
		t.Fatalf("ResolveSetByEmojiID failed: %v", err)
	}
	if set.SetName != "myset" || set.Title != "My Set" {
		t.Fatalf("unexpected set meta: %#v", set)
	}
	if len(set.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(set.Items))
	}
	if set.Items[0].CustomEmojiID != "2" || set.Items[1].CustomEmojiID != "10" {
		t.Fatalf("unexpected items order: %#v", set.Items)
	}
}

func TestFetchFile_UsesTelegramGetFileAndDownload(t *testing.T) {
	token := "TEST_TOKEN"
	const fileBody = "WEBPTEST"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/bot"+token+"/getFile":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"result": map[string]any{
					"file_path": "stickers/file_1.webp",
				},
			})
		case r.URL.Path == "/file/bot"+token+"/stickers/file_1.webp":
			w.Header().Set("Content-Type", "image/webp")
			_, _ = io.WriteString(w, fileBody)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	svc := emojiProxyService{
		Token:  token,
		Client: testEmojiProxyClient(ts),
	}
	body, ctype, err := svc.FetchFile(context.Background(), "file_id_1")
	if err != nil {
		t.Fatalf("FetchFile failed: %v", err)
	}
	if string(body) != fileBody {
		t.Fatalf("unexpected body: %q", string(body))
	}
	if ctype != "image/webp" {
		t.Fatalf("unexpected content type: %q", ctype)
	}
}

func TestResolveStickerSetByName(t *testing.T) {
	token := "TEST_TOKEN"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot"+token+"/getStickerSet" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":{"name":"BlahajSpin","title":"Blahaj Spin","stickers":[{"set_name":"BlahajSpin","emoji":"🦈","file_id":"CAAC1","thumb":{"file_id":"TH1"}},{"set_name":"BlahajSpin","emoji":"🦈","file_id":"CAAC2"}]}}`)
	}))
	defer ts.Close()

	svc := emojiProxyService{
		Token:  token,
		Client: testEmojiProxyClient(ts),
	}
	set, err := svc.ResolveStickerSetByName(context.Background(), "BlahajSpin")
	if err != nil {
		t.Fatalf("ResolveStickerSetByName failed: %v", err)
	}
	if set.SetName != "BlahajSpin" || set.Title != "Blahaj Spin" {
		t.Fatalf("unexpected set: %#v", set)
	}
	if len(set.Items) != 2 {
		t.Fatalf("expected 2 stickers, got %d", len(set.Items))
	}
	if set.Items[0].FileID != "CAAC1" || set.Items[0].ThumbFileID != "TH1" {
		t.Fatalf("unexpected first item: %#v", set.Items[0])
	}
}

func TestExtractStickerFileIDFromTemplate_HTMLTagged(t *testing.T) {
	raw := "<b>code</b> <code>CAACAgIAAxkBAAABCd1mX-abc_DEF-1234567890:BlahajSpin</code>"
	got := extractStickerFileIDFromTemplate(raw)
	if !strings.HasPrefix(got, "CAACAgIAAxkBAAABCd1mX-abc_DEF-1234567890") {
		t.Fatalf("unexpected file id parsed: %q", got)
	}
}
