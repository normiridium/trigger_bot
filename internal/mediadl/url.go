package mediadl

import (
	"net/url"
	"strings"
)

type Service string

const (
	ServiceUnknown    Service = ""
	ServiceYouTube    Service = "youtube"
	ServiceVK         Service = "vk"
	ServiceInstagram  Service = "instagram"
	ServiceTikTok     Service = "tiktok"
	ServiceSoundCloud Service = "soundcloud"
	ServiceCoub       Service = "coub"
	ServiceX          Service = "x"
)

func NormalizeSupportedURL(raw string) (normalized string, service Service, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ServiceUnknown, false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", ServiceUnknown, false
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	switch {
	case host == "youtube.com" || host == "www.youtube.com" || host == "m.youtube.com" || host == "youtu.be":
		return raw, ServiceYouTube, true
	case host == "vk.com" || host == "www.vk.com" || host == "m.vk.com" || host == "vk.ru" || host == "www.vk.ru":
		return raw, ServiceVK, true
	case host == "instagram.com" || host == "www.instagram.com":
		return raw, ServiceInstagram, true
	case host == "tiktok.com" || host == "www.tiktok.com" || host == "m.tiktok.com" || host == "vm.tiktok.com" || host == "vt.tiktok.com":
		return raw, ServiceTikTok, true
	case host == "soundcloud.com" || host == "www.soundcloud.com" || host == "m.soundcloud.com":
		return raw, ServiceSoundCloud, true
	case host == "coub.com" || host == "www.coub.com":
		return raw, ServiceCoub, true
	case host == "x.com" || host == "www.x.com" || host == "m.x.com" || host == "twitter.com" || host == "www.twitter.com" || host == "m.twitter.com" || host == "mobile.twitter.com":
		return raw, ServiceX, true
	default:
		return "", ServiceUnknown, false
	}
}
