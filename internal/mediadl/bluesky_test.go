package mediadl

// Разбор ссылок Bluesky и вложений поста. Сеть не нужна: JSON-образцы
// соответствуют ответам app.bsky.feed.getPostThread.

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBlueskyHosts(t *testing.T) {
	for _, host := range []string{"bsky.app", "www.bsky.app", "fxbsky.app", "www.fxbsky.app", "bsky.social"} {
		if !IsBlueskyHost(host) {
			t.Fatalf("хост %q должен обслуживаться", host)
		}
	}
	for _, host := range []string{"example.com", "bsky.app.evil.tld", "reddit.com"} {
		if IsBlueskyHost(host) {
			t.Fatalf("хост %q не наш", host)
		}
	}
}

func TestBlueskyPostRef(t *testing.T) {
	cases := []struct {
		url, handle, rkey string
		ok                bool
	}{
		{"https://fxbsky.app/profile/riolu.gay/post/3mzse3ojnes2y", "riolu.gay", "3mzse3ojnes2y", true},
		{"https://bsky.app/profile/riolu.gay/post/3mzse3ojnes2y", "riolu.gay", "3mzse3ojnes2y", true},
		{"https://bsky.app/profile/did:plc:txevokwfocn2md7iy2dxpztb/post/3mzse3ojnes2y", "did:plc:txevokwfocn2md7iy2dxpztb", "3mzse3ojnes2y", true},
		{"https://bsky.app/profile/riolu.gay/post/3mzse3ojnes2y?ref=1", "riolu.gay", "3mzse3ojnes2y", true},
		{"https://bsky.app/profile/riolu.gay", "", "", false},
		{"https://example.com/profile/a/post/3mzse3ojnes2y", "a", "3mzse3ojnes2y", true},
	}
	for _, c := range cases {
		handle, rkey, ok := blueskyPostRef(c.url)
		if ok != c.ok || handle != c.handle || rkey != c.rkey {
			t.Fatalf("%s → (%q,%q,%v), ожидали (%q,%q,%v)", c.url, handle, rkey, ok, c.handle, c.rkey, c.ok)
		}
	}
}

func TestBlueskyThreadURI(t *testing.T) {
	got := blueskyThreadURI("riolu.gay", "3mzse3ojnes2y")
	want := "at://riolu.gay/app.bsky.feed.post/3mzse3ojnes2y"
	if got != want {
		t.Fatalf("URI %q, ожидали %q", got, want)
	}
}

// Реальный ответ API для поста riolu.gay (картинка).
const blueskyImagesJSON = `{
  "thread": {"post": {
    "uri": "at://did:plc:txevokwfocn2md7iy2dxpztb/app.bsky.feed.post/3mzse3ojnes2y",
    "author": {"handle": "riolu.gay", "displayName": "riolu"},
    "record": {"text": "they make me wear this before im allowed to leave the house"},
    "embed": {"$type": "app.bsky.embed.images#view",
      "images": [{"thumb": "https://cdn.bsky.app/img/feed_thumbnail/plain/did:plc:x/cid1@jpeg",
                  "fullsize": "https://cdn.bsky.app/img/feed_fullsize/plain/did:plc:x/cid1",
                  "alt": "", "aspectRatio": {"width": 1536, "height": 2048}}]}
  }}
}`

func TestBlueskyCollectImages(t *testing.T) {
	var parsed blueskyThreadResponse
	if err := json.Unmarshal([]byte(blueskyImagesJSON), &parsed); err != nil {
		t.Fatalf("разбор: %v", err)
	}
	post := parsed.Thread.Post
	if post == nil {
		t.Fatal("пост не разобрался")
	}
	if post.Author.Handle != "riolu.gay" || !strings.Contains(post.Record.Text, "wear this") {
		t.Fatalf("неверные автор/текст: %q / %q", post.Author.Handle, post.Record.Text)
	}
	var images []string
	var video string
	blueskyCollectMedia(post.Embed, &images, &video, map[string]bool{})
	if len(images) != 1 || video != "" {
		t.Fatalf("ожидали 1 картинку без видео, получили %d / %q", len(images), video)
	}
	if got := blueskyFullsizeURL(images[0]); !strings.HasSuffix(got, "@jpeg") {
		t.Fatalf("полноразмерная ссылка без формата: %q", got)
	}
}

// Видео: playlist лежит прямо во вложении.
const blueskyVideoJSON = `{
  "thread": {"post": {
    "author": {"handle": "bsky.app"},
    "record": {"text": "clip"},
    "embed": {"$type": "app.bsky.embed.video#view",
      "playlist": "https://video.bsky.app/watch/did%3Aplc%3Asihpw65g5f5ptzlsu4jybz2t/bafkrei/playlist.m3u8",
      "thumbnail": "https://video.bsky.app/watch/x/thumbnail.jpg",
      "aspectRatio": {"width": 1170, "height": 712}}
  }}
}`

func TestBlueskyCollectVideo(t *testing.T) {
	var parsed blueskyThreadResponse
	if err := json.Unmarshal([]byte(blueskyVideoJSON), &parsed); err != nil {
		t.Fatalf("разбор: %v", err)
	}
	var images []string
	var video string
	blueskyCollectMedia(parsed.Thread.Post.Embed, &images, &video, map[string]bool{})
	if video == "" || !strings.Contains(video, "playlist.m3u8") {
		t.Fatalf("видео не найдено: %q", video)
	}
	if len(images) != 0 {
		t.Fatalf("у видео не должно быть картинок: %v", images)
	}
}

// recordWithMedia: картинка в media, цитируемый пост с видео в record.
const blueskyRecordWithMediaJSON = `{
  "thread": {"post": {
    "author": {"handle": "someone.test"},
    "record": {"text": "quote + media"},
    "embed": {"$type": "app.bsky.embed.recordWithMedia#view",
      "media": {"$type": "app.bsky.embed.images#view",
        "images": [{"fullsize": "https://cdn.bsky.app/img/feed_fullsize/plain/did:plc:y/cid2", "alt": "photo"}]},
      "record": {"$type": "app.bsky.embed.record#view",
        "record": {"author": {"handle": "quoted.test"}, "record": {"text": "quoted"},
          "embed": {"$type": "app.bsky.embed.video#view", "playlist": "https://video.bsky.app/watch/y/playlist.m3u8"}}}}
  }}
}`

func TestBlueskyCollectRecordWithMedia(t *testing.T) {
	var parsed blueskyThreadResponse
	if err := json.Unmarshal([]byte(blueskyRecordWithMediaJSON), &parsed); err != nil {
		t.Fatalf("разбор: %v", err)
	}
	var images []string
	var video string
	blueskyCollectMedia(parsed.Thread.Post.Embed, &images, &video, map[string]bool{})
	if len(images) != 1 {
		t.Fatalf("ожидали картинку из media, получили %d", len(images))
	}
	if video == "" {
		t.Fatal("видео из цитируемого поста потерялось")
	}
}

func TestBlueskyCollectDeduplicates(t *testing.T) {
	same := "https://cdn.bsky.app/img/feed_fullsize/plain/did:plc:z/cid3"
	embed := &blueskyEmbed{Images: []blueskyImage{{Fullsize: same}, {Fullsize: same}}}
	var images []string
	var video string
	blueskyCollectMedia(embed, &images, &video, map[string]bool{})
	if len(images) != 1 {
		t.Fatalf("дубликат не отфильтрован: %v", images)
	}
}

func TestBlueskyFullsizeKeepsSuffix(t *testing.T) {
	withFormat := "https://cdn.bsky.app/img/feed_fullsize/plain/did:plc:z/cid@png"
	if got := blueskyFullsizeURL(withFormat); got != withFormat {
		t.Fatalf("готовый формат испортили: %q", got)
	}
	other := "https://video.bsky.app/watch/x/thumbnail.jpg"
	if got := blueskyFullsizeURL(other); got != other {
		t.Fatalf("постороннюю ссылку трогать нельзя: %q", got)
	}
}
