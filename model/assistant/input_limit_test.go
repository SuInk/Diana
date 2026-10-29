// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

func TestTruncateInputTextTellsModelHowToReadRest(t *testing.T) {
	text := strings.Repeat("长", 30)
	got := truncateInputText(text, "12345", 10)
	if !strings.HasPrefix(got, strings.Repeat("长", 10)+"\n") {
		t.Fatalf("head not kept: %q", got)
	}
	for _, want := range []string{"共 30 字", "上限 10 字", "action=around", "message_id=12345", "只看了开头"} {
		if !strings.Contains(got, want) {
			t.Fatalf("notice missing %q: %q", want, got)
		}
	}
	if got := truncateInputText(text, "", 10); strings.Contains(got, "message_id") {
		t.Fatalf("no message id should not suggest around: %q", got)
	}
}

func TestTruncateInputTextKeepsShortAndUnlimited(t *testing.T) {
	text := strings.Repeat("长", 30)
	for _, limit := range []int{MaxInputCharsUnlimited, 30, 100} {
		if got := truncateInputText(text, "1", limit); got != text {
			t.Fatalf("limit %d changed text: %q", limit, got)
		}
	}
}

func TestMaxInputCharsDefaultsToUnlimited(t *testing.T) {
	for _, value := range []int{0, -1, -5} {
		if got := (BotConfig{MaxInputChars: value}).WithDefaults().MaxInputChars; got != MaxInputCharsUnlimited {
			t.Fatalf("%d normalized to %d, want unlimited", value, got)
		}
	}
	if got := (BotConfig{MaxInputChars: 50}).WithDefaults().MaxInputChars; got != MinMaxInputChars {
		t.Fatalf("too small = %d, want %d", got, MinMaxInputChars)
	}
	if got := (BotConfig{MaxInputChars: 3000}).WithDefaults().MaxInputChars; got != 3000 {
		t.Fatalf("explicit = %d, want 3000", got)
	}
}
