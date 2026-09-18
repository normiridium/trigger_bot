package app

import "testing"

func TestDefaultBotCommandsHideTranslateGPT(t *testing.T) {
	for _, command := range allVisibleBotCommands() {
		if command.Command == cmdTranslateGPT {
			t.Fatalf("/%s must stay hidden from the Telegram command menu", cmdTranslateGPT)
		}
	}
}
