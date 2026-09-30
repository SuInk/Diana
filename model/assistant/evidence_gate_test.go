// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "testing"

func TestParseEvidenceGateDecision(t *testing.T) {
	cases := map[string]bool{
		`{"needs_evidence":true,"reason":"问价格"}`:   true,
		"```json\n{\"needs_evidence\": true}\n```": true,
		`{"needs_evidence":false,"reason":"闲聊"}`:   false,
		`看不懂`:                      false,
		`{"needs_evidence":"yes"}`: false,
	}
	for raw, want := range cases {
		if got := parseEvidenceGateDecision(raw); got != want {
			t.Errorf("parseEvidenceGateDecision(%q)=%v, want %v", raw, got, want)
		}
	}
}
