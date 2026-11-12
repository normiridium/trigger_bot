package mediadl

// Живая проверка Reddit: картинка и видео с настоящих постов.
//
//	LIVE_REDDIT_IMAGE=<url> LIVE_REDDIT_VIDEO=<url> go test -run TestRedditLive -v ./internal/mediadl

import (
	"context"
	"os"
	"testing"
	"time"
)

func redditLiveCase(t *testing.T, name, rawURL string) {
	t.Helper()
	if rawURL == "" {
		t.Skipf("%s: URL не задан", name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	started := time.Now()
	var d Downloader
	post, supported, err := d.RedditPost(ctx, rawURL)
	if !supported {
		t.Fatalf("%s: ссылка не распознана как reddit", name)
	}
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	defer os.RemoveAll(post.Dir)

	total := int64(0)
	for _, path := range post.Paths {
		if st, statErr := os.Stat(path); statErr == nil {
			total += st.Size()
		}
	}
	t.Logf("%s: kind=%s файлов=%d всего=%.2f МБ за %.1fs", name, post.Kind, len(post.Paths), float64(total)/1_000_000.0, time.Since(started).Seconds())
	t.Logf("%s: title=%q author=%q source=%q", name, post.Title, post.Author, post.SourceURL)

	if len(post.Paths) == 0 {
		t.Fatalf("%s: нет файлов", name)
	}
	for _, path := range post.Paths {
		st, err := os.Stat(path)
		if err != nil || st.Size() == 0 {
			t.Fatalf("%s: пустой файл %s", name, path)
		}
	}
}

func TestRedditLiveImage(t *testing.T) {
	redditLiveCase(t, "image", os.Getenv("LIVE_REDDIT_IMAGE"))
}

func TestRedditLiveVideo(t *testing.T) {
	redditLiveCase(t, "video", os.Getenv("LIVE_REDDIT_VIDEO"))
}

func TestRedditPostRef(t *testing.T) {
	cases := []struct {
		raw    string
		postID string
		direct string
	}{
		{"https://www.reddit.com/r/XFiles/comments/1lp1gpp/david/", "1lp1gpp", ""},
		{"https://www.reddit.com/r/XFiles/comments/1lp1gpp/david/?tl=ru", "1lp1gpp", ""},
		{"https://old.reddit.com/r/XFiles/comments/1lp1gpp/", "1lp1gpp", ""},
		{"https://redd.it/1lp1gpp", "1lp1gpp", ""},
		{"https://i.redd.it/ckif37erj9af1.jpeg", "", "https://i.redd.it/ckif37erj9af1.jpeg"},
		{"https://v.redd.it/a08z8ef2nyzh1", "", "https://v.redd.it/a08z8ef2nyzh1"},
	}
	for _, tc := range cases {
		postID, direct, ok := redditPostRef(tc.raw)
		if !ok || postID != tc.postID || direct != tc.direct {
			t.Fatalf("%s -> postID=%q direct=%q ok=%v, ожидали %q/%q", tc.raw, postID, direct, ok, tc.postID, tc.direct)
		}
	}
	if _, _, ok := redditPostRef("https://example.com/nothing"); ok {
		t.Fatalf("чужая ссылка распознана как reddit")
	}
}

func TestRedditMediaExtraction(t *testing.T) {
	feed := []byte(`<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><entry>
<title>David as DEA agent</title>
<author><name>/u/Fluid_Ad_9580</name></author>
<link href="https://www.reddit.com/r/XFiles/comments/1lp1gpp/david/"/>
<content type="html">&lt;a href=&quot;https://i.redd.it/ckif37erj9af1.jpeg&quot;&gt;[link]&lt;/a&gt;</content>
</entry><entry><title>comment</title><content type="html">&lt;a href=&quot;https://i.redd.it/other999.jpeg&quot;&gt;x&lt;/a&gt;</content></entry></feed>`)

	title, author, link, images, videoID, err := redditParsePost(feed)
	if err != nil {
		t.Fatalf("разбор: %v", err)
	}
	if title != "David as DEA agent" || author != "Fluid_Ad_9580" {
		t.Fatalf("метаданные: %q %q", title, author)
	}
	if link != "https://www.reddit.com/r/XFiles/comments/1lp1gpp/david/" {
		t.Fatalf("ссылка: %q", link)
	}
	if videoID != "" {
		t.Fatalf("видео не ожидалось: %q", videoID)
	}
	if len(images) != 1 || images[0] != "https://i.redd.it/ckif37erj9af1.jpeg" {
		t.Fatalf("картинки поста: %v (комментарии не должны попадать)", images)
	}
}
