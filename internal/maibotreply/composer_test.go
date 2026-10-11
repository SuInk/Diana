// SPDX-License-Identifier: GPL-3.0-only
package maibotreply

import (
	"strings"
	"testing"
	"time"
)

func TestComposePreservesVisibleHistoryReferenceAndTarget(t *testing.T) {
	in := Input{
		Identity:  "identity",
		Style:     "style",
		Attention: "attention",
		History:   []Message{{Role: "user", Content: "history"}},
		Target:    &Target{ID: "m2", Sender: "member", Content: "current", QuoteIDs: []string{"m1"}},
		Time:      time.Date(2026, 10, 11, 12, 0, 0, 0, time.Local),
		Reference: "verified draft",
	}
	got := Compose(in)
	if got[1] != in.History[0] || got[2].Content != in.Reference {
		t.Fatalf("history or reference moved: %#v", got)
	}
	joined := ""
	for _, message := range got {
		joined += message.Content
	}
	for _, required := range []string{"identity", "style", "attention", "quote=m1", "current", "verified draft"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("composition lost %q: %s", required, joined)
		}
	}
}
