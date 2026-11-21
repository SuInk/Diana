// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"testing"
	"time"
)

// 这些值由 edge-tts 7.3.1 的 generate_sec_ms_gec_v2 按同一时间戳生成，
// 用来钉住 CS64 + SHA-256 的移植结果。
func TestEdgeSecMSGECV2Vectors(t *testing.T) {
	cases := []struct {
		unix int64
		want string
	}{
		{1704067200, "9586E5FBB4BD2CFF-DE636BA8369C7431ED826BDEEB00ECA1CC52826A6BB736DCB150A2AF37DFC498"},
		{1793001610, "1AD08E2D7749BFBA-BED40CAC6D5D76783DA3DEFCAC95ADCA6B6A4EE94319B1C536B45FFF6FF0ADFB"},
		{1793001899, "1AD08E2D7749BFBA-BED40CAC6D5D76783DA3DEFCAC95ADCA6B6A4EE94319B1C536B45FFF6FF0ADFB"},
		{1793001900, "D343289772E040C8-BD7C967873E094F291D63A01454C5AAECD69E4B1973F0B951446889D73BF7649"},
		{1795262400, "86F49B3648980605-0C8A8ACA5D35FA9FCBDD9415179B479E3E02D7CA0D856D373D84781A63CDC555"},
	}
	for _, tc := range cases {
		if got := edgeSecMSGEC(time.Unix(tc.unix, 0)); got != tc.want {
			t.Fatalf("edgeSecMSGEC(%d) = %q, want %q", tc.unix, got, tc.want)
		}
	}
}

func TestEdgeSecMSGECChangesOnlyOnFiveMinuteBoundaries(t *testing.T) {
	base := time.Date(2026, 10, 26, 8, 0, 10, 0, time.UTC)
	if edgeSecMSGEC(base) != edgeSecMSGEC(base.Add(4*time.Minute)) {
		t.Fatal("token changed inside the five-minute window")
	}
	if edgeSecMSGEC(base) == edgeSecMSGEC(base.Add(5*time.Minute)) {
		t.Fatal("token did not change on the five-minute boundary")
	}
}
