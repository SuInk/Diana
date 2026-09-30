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

func TestParseEvidenceQuery(t *testing.T) {
	cases := map[string]string{
		`{"query":" Steam 秋季特卖 2026 日期 "}`:              "Steam 秋季特卖 2026 日期",
		"```json\n{\"query\": \"GPT-6 Astra 价格\"}\n```": "GPT-6 Astra 价格",
		`看不懂`:         "",
		`{"query":3}`: "",
	}
	for raw, want := range cases {
		if got := parseEvidenceQuery(raw); got != want {
			t.Errorf("parseEvidenceQuery(%q)=%q, want %q", raw, got, want)
		}
	}
}
