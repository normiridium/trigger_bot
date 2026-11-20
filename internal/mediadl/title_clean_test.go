package mediadl

import "testing"

func TestSanitizeMediaTitle(t *testing.T) {
	cases := []struct{ in, want string }{
		// Реальный случай из Pinterest → Instagram: yt-dlp склеил текст с хвостом ссылки.
		{"The Dachshund, also known as the “sausage dog,”rce=ig_web_copy_link&igsh=MzRlODBiNWFlZA==",
			"The Dachshund, also known as the “sausage dog,”"},
		{"Кот и пёс https://www.instagram.com/reel/XYZ/?utm_source=ig_web_copy_link&igsh=abc", "Кот и пёс"},
		{"Обычный заголовок без ссылок", "Обычный заголовок без ссылок"},
		{"2+2=4", "2+2=4"},
		{"Заголовок с тире — и всё", "Заголовок с тире — и всё"},
		{"Смешная нарезка igsh=MzRlODBiNWFlZA", "Смешная нарезка"},
		{"", ""},
	}
	for _, c := range cases {
		if got := sanitizeMediaTitle(c.in); got != c.want {
			t.Errorf("sanitizeMediaTitle(%q) = %q, ожидалось %q", c.in, got, c.want)
		}
	}
}

func TestDisplayTitleStripsTrackingTail(t *testing.T) {
	raw := "The Dachshund, also known as the “sausage dog,”rce=ig_web_copy_link&igsh=MzRlODBiNWFlZA=="
	got := displayTitleForService(ServiceInstagram, MediaKindVideo, raw, "")
	if got != "The Dachshund, also known as the “sausage dog,”" {
		t.Fatalf("в заголовке остался мусор: %q", got)
	}
}
