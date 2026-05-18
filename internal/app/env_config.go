package app

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func envOr(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

func envBool(key string, fallback bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if v == "" {
		return fallback
	}
	return v == "1" || v == "true" || v == "yes"
}

func envInt(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func resolveDefaultCookiesFile() string {
	candidates := []string{"cookies.txt"}
	if exePath, err := os.Executable(); err == nil {
		exeDir := strings.TrimSpace(filepath.Dir(exePath))
		if exeDir != "" {
			candidates = append(candidates, filepath.Join(exeDir, "cookies.txt"))
		}
	}
	for _, path := range candidates {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		st, err := os.Stat(path)
		if err == nil && st != nil && !st.IsDir() && st.Size() > 0 {
			return path
		}
	}
	return ""
}

func gptReplyReactionChancePercent() int {
	v := envInt("GPT_REPLY_REACTION_CHANCE_PERCENT", 25)
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
