package assistant

import "testing"

func TestNormalizeReplyHidesLeadingReasoning(t *testing.T) {
	for _, plain := range []bool{false, true} {
		if got := normalizeReply("<think>private analysis</think>真正回复", 0, plain); got != "真正回复" {
			t.Fatalf("visible reply=%q", got)
		}
		if got := normalizeReply("<think>unfinished private analysis", 0, plain); got != "" {
			t.Fatalf("unfinished reasoning leaked: %q", got)
		}
	}
}
