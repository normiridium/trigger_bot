package app

import "testing"

func TestReactionPolarityCounts(t *testing.T) {
	upd := &rawMessageReactionCountUpdate{
		Reactions: []rawReactionCount{
			{Type: rawReactionType{Type: "emoji", Emoji: "👍"}, TotalCount: 3},
			{Type: rawReactionType{Type: "emoji", Emoji: "🔥"}, TotalCount: 2},
			{Type: rawReactionType{Type: "emoji", Emoji: "👎"}, TotalCount: 4},
			{Type: rawReactionType{Type: "emoji", Emoji: "🤔"}, TotalCount: 10},
		},
	}
	pos, neg := reactionPolarityCounts(upd)
	if pos != 5 {
		t.Fatalf("positive count = %d, want 5", pos)
	}
	if neg != 4 {
		t.Fatalf("negative count = %d, want 4", neg)
	}
}

func TestReactionThresholdCrossed(t *testing.T) {
	reactionTriggerState.mu.Lock()
	reactionTriggerState.counts = make(map[string]int)
	reactionTriggerState.messageCounts = make(map[string]reactionMessageCounts)
	reactionTriggerState.mu.Unlock()

	if reactionThresholdCrossed(1, -100, 10, reactionPolarityPositive, 3, 3) {
		t.Fatalf("count equal to threshold must not fire")
	}
	if !reactionThresholdCrossed(1, -100, 10, reactionPolarityPositive, 3, 4) {
		t.Fatalf("crossing above threshold must fire")
	}
	if reactionThresholdCrossed(1, -100, 10, reactionPolarityPositive, 3, 5) {
		t.Fatalf("staying above threshold must not fire again")
	}
	if reactionThresholdCrossed(1, -100, 10, reactionPolarityPositive, 3, 2) {
		t.Fatalf("dropping below threshold must not fire")
	}
	if !reactionThresholdCrossed(1, -100, 10, reactionPolarityPositive, 3, 4) {
		t.Fatalf("crossing above threshold again after drop must fire")
	}
}

func TestApplyReactionMessageDeltaCountsPositiveLikes(t *testing.T) {
	reactionTriggerState.mu.Lock()
	reactionTriggerState.counts = make(map[string]int)
	reactionTriggerState.messageCounts = make(map[string]reactionMessageCounts)
	reactionTriggerState.mu.Unlock()

	counts := applyReactionMessageDelta(-100, 10, 1, 0)
	if counts.Positive != 1 || counts.Negative != 0 {
		t.Fatalf("counts after first like = %#v, want positive=1 negative=0", counts)
	}
	counts = applyReactionMessageDelta(-100, 10, 1, 0)
	counts = applyReactionMessageDelta(-100, 10, 1, 0)
	if counts.Positive != 3 {
		t.Fatalf("positive count = %d, want 3", counts.Positive)
	}
	if !reactionThresholdCrossed(58, -100, 10, reactionPolarityPositive, 2, counts.Positive) {
		t.Fatalf("third positive reaction must cross threshold 2")
	}
	if reactionThresholdCrossed(58, -100, 10, reactionPolarityPositive, 2, counts.Positive) {
		t.Fatalf("same count must not fire twice")
	}
}

func TestReactionTypesPolarityCounts(t *testing.T) {
	pos, neg := reactionTypesPolarityCounts([]rawReactionType{
		{Type: "emoji", Emoji: "👍"},
		{Type: "emoji", Emoji: "🔥"},
		{Type: "emoji", Emoji: "👎"},
		{Type: "emoji", Emoji: "🤔"},
	})
	if pos != 2 || neg != 1 {
		t.Fatalf("reaction type counts = %d/%d, want 2/1", pos, neg)
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
