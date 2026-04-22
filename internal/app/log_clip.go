package app

import "strings"

var logTextClipMax = 200

func clipLogText(s string, fallback int) string {
	max := logTextClipMax
	if max < 0 {
		max = fallback
	}
	if max == 0 {
		return strings.TrimSpace(s)
	}
	if max > 0 {
		return clipText(s, max)
	}
	if fallback <= 0 {
		return strings.TrimSpace(s)
	}
	return clipText(s, fallback)
}
