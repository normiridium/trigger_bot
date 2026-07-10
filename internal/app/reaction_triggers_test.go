package app

import "testing"

func TestReactionKindCounts(t *testing.T) {
	upd := &rawMessageReactionCountUpdate{
		Reactions: []rawReactionCount{
			{Type: rawReactionType{Type: "emoji", Emoji: "👍"}, TotalCount: 3},
			{Type: rawReactionType{Type: "emoji", Emoji: "🔥"}, TotalCount: 2},
			{Type: rawReactionType{Type: "emoji", Emoji: "😂"}, TotalCount: 4},
			{Type: rawReactionType{Type: "emoji", Emoji: "😢"}, TotalCount: 1},
			{Type: rawReactionType{Type: "emoji", Emoji: "👎"}, TotalCount: 5},
			{Type: rawReactionType{Type: "emoji", Emoji: "🤔"}, TotalCount: 10},
		},
	}
	counts := reactionCountUpdateKinds(upd)
	if counts.Support != 3 || counts.Hype != 2 || counts.Funny != 4 || counts.Sad != 1 || counts.Angry != 5 {
		t.Fatalf("counts = %#v, want support=3 hype=2 funny=4 sad=1 angry=5", counts)
	}
}

func TestReactionWinnerPriority(t *testing.T) {
	winner, count, ok := (reactionKindCounts{Support: 2, Hype: 2, Funny: 2}).winner()
	if !ok || winner != reactionKindSupport || count != 2 {
		t.Fatalf("winner = %s/%d/%v, want support/2/true", winner, count, ok)
	}
	winner, count, ok = (reactionKindCounts{Sad: 3, Funny: 3}).winner()
	if !ok || winner != reactionKindSad || count != 3 {
		t.Fatalf("winner = %s/%d/%v, want sad/3/true", winner, count, ok)
	}
	winner, count, ok = (reactionKindCounts{Angry: 1, Sad: 1, Support: 1}).winner()
	if !ok || winner != reactionKindAngry || count != 1 {
		t.Fatalf("winner = %s/%d/%v, want angry/1/true", winner, count, ok)
	}
}

func TestReactionThresholdCrossed(t *testing.T) {
	reactionTriggerState.mu.Lock()
	reactionTriggerState.counts = make(map[string]int)
	reactionTriggerState.messageCounts = make(map[string]reactionKindCounts)
	reactionTriggerState.mu.Unlock()

	if reactionThresholdCrossed(1, -100, 10, reactionKindSupport, 3, 3) {
		t.Fatalf("count equal to threshold must not fire")
	}
	if !reactionThresholdCrossed(1, -100, 10, reactionKindSupport, 3, 4) {
		t.Fatalf("crossing above threshold must fire")
	}
	if reactionThresholdCrossed(1, -100, 10, reactionKindSupport, 3, 5) {
		t.Fatalf("staying above threshold must not fire again")
	}
	if reactionThresholdCrossed(1, -100, 10, reactionKindSupport, 3, 2) {
		t.Fatalf("dropping below threshold must not fire")
	}
	if !reactionThresholdCrossed(1, -100, 10, reactionKindSupport, 3, 4) {
		t.Fatalf("crossing above threshold again after drop must fire")
	}
}

func TestApplyReactionMessageDeltaCountsSupport(t *testing.T) {
	reactionTriggerState.mu.Lock()
	reactionTriggerState.counts = make(map[string]int)
	reactionTriggerState.messageCounts = make(map[string]reactionKindCounts)
	reactionTriggerState.mu.Unlock()

	counts := applyReactionMessageDelta(-100, 10, reactionKindCounts{Support: 1})
	if counts.Support != 1 || counts.Angry != 0 {
		t.Fatalf("counts after first support = %#v, want support=1 angry=0", counts)
	}
	counts = applyReactionMessageDelta(-100, 10, reactionKindCounts{Support: 1})
	counts = applyReactionMessageDelta(-100, 10, reactionKindCounts{Support: 1})
	if counts.Support != 3 {
		t.Fatalf("support count = %d, want 3", counts.Support)
	}
	if !reactionThresholdCrossed(58, -100, 10, reactionKindSupport, 2, counts.Support) {
		t.Fatalf("third support reaction must cross threshold 2")
	}
	if reactionThresholdCrossed(58, -100, 10, reactionKindSupport, 2, counts.Support) {
		t.Fatalf("same count must not fire twice")
	}
}

func TestReactionTypesKindCounts(t *testing.T) {
	counts := reactionTypesKindCounts([]rawReactionType{
		{Type: "emoji", Emoji: "👍"},
		{Type: "emoji", Emoji: "🔥"},
		{Type: "emoji", Emoji: "😂"},
		{Type: "emoji", Emoji: "😢"},
		{Type: "emoji", Emoji: "👎"},
		{Type: "emoji", Emoji: "🤔"},
	})
	if counts.Support != 1 || counts.Hype != 1 || counts.Funny != 1 || counts.Sad != 1 || counts.Angry != 1 {
		t.Fatalf("reaction type counts = %#v, want one per known kind", counts)
	}
}

func TestParseReactionThreshold(t *testing.T) {
	if got, ok := parseReactionThreshold(" 7 "); !ok || got != 7 {
		t.Fatalf("parse threshold = %d/%v, want 7/true", got, ok)
	}
	if _, ok := parseReactionThreshold("-1"); ok {
		t.Fatalf("negative threshold must be invalid")
	}
	if _, ok := parseReactionThreshold("three"); ok {
		t.Fatalf("non-number threshold must be invalid")
	}
}
