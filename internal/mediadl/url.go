package mediadl

import (
	"net/url"
	"strings"
)

func NormalizeSupportedURL(raw string) (normalized string, service string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", false
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	switch {
	case host == "youtube.com" || host == "www.youtube.com" || host == "m.youtube.com" || host == "youtu.be":
		return raw, "youtube", true
	case host == "instagram.com" || host == "www.instagram.com":
		return raw, "instagram", true
	case host == "tiktok.com" || host == "www.tiktok.com" || host == "m.tiktok.com" || host == "vm.tiktok.com" || host == "vt.tiktok.com":
		return raw, "tiktok", true
	case host == "soundcloud.com" || host == "www.soundcloud.com" || host == "m.soundcloud.com":
		return raw, "soundcloud", true
	default:
		return "", "", false
	}
}
