package app

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// logTextFingerprint returns a non-reversible description of a text blob for
// logs: length plus a short hash. Chat contents and participant portraits must
// not be written to journald in clear text, but operators still need to tell
// two payloads apart while debugging.
func logTextFingerprint(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "len=0"
	}
	sum := sha256.Sum256([]byte(trimmed))
	return fmt.Sprintf("len=%d sha256=%x", len([]rune(trimmed)), sum[:6])
}
