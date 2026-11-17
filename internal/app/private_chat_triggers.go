package app

import (
	"os"
	"strconv"
	"strings"
)

// privateChatTriggersAllows reports whether a plain-text message received in a
// private chat should go through the regular trigger pipeline instead of being
// dropped. Private chats historically only served commands and helper replies.
//
// Env:
//
//	PRIVATE_CHAT_TRIGGERS_ENABLED  - "true" enables the feature (default off).
//	PRIVATE_CHAT_TRIGGER_USER_IDS  - comma separated user ids allowed to use it;
//	                                 empty means "any user" once enabled.
func privateChatTriggersAllows(userID int64) bool {
	if !envBool("PRIVATE_CHAT_TRIGGERS_ENABLED", false) {
		return false
	}
	raw := strings.TrimSpace(os.Getenv("PRIVATE_CHAT_TRIGGER_USER_IDS"))
	if raw == "" {
		return true
	}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			continue
		}
		if id == userID {
			return true
		}
	}
	return false
}
