// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package secretmask

import (
	"strings"
	"testing"
)

// 以下证件号和卡号都是按校验规则合成的，不对应真人。

func TestValidIDCardChecksAndDate(t *testing.T) {
	cases := map[string]bool{
		"110105199003071239": true,
		"44030420011129456X": false, // 校验位不对
		"440304200111294567": true,
		"110105199002301234": false, // 2 月 30 日
		"11010519900307123":  false, // 少一位
	}
	for value, want := range cases {
		if got := ValidIDCard(value); got != want {
			t.Errorf("ValidIDCard(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestValidLuhnAcceptsGroupedDigits(t *testing.T) {
	if !ValidLuhn("6222021000000005") || !ValidLuhn("6222 0210 0000 0005") {
		t.Fatal("valid card number rejected")
	}
	if ValidLuhn("6222021000000006") {
		t.Fatal("bad check digit accepted")
	}
	if ValidLuhn("622202100000") {
		t.Fatal("too short accepted")
	}
}

func TestUnresolvedPlaceholderErrorFindsNestedLeftovers(t *testing.T) {
	full := PlaceholderPrefix + "api_key_0123456789ab"
	if !PlaceholderPattern.MatchString(full) {
		t.Fatalf("pattern should match %q", full)
	}
	if PlaceholderPattern.MatchString(full + "c") {
		t.Fatal("a placeholder with extra characters is another value")
	}
	for _, input := range []map[string]any{
		{"args": []any{"-H", "Authorization: Bearer " + full}},
		{"args": []string{"-c", "k='" + full[:len(full)-1] + "'"}}, // 抄短了一位
		{"input": map[string]any{"text": strings.ToUpper(full)}},
	} {
		err := UnresolvedPlaceholderError(input)
		if err == nil || !strings.Contains(err.Error(), "还原不出原值") {
			t.Fatalf("leftover placeholder not caught in %v: %v", input, err)
		}
	}
	if err := UnresolvedPlaceholderError(map[string]any{"text": "pv_count_total 是个普通变量"}); err != nil {
		t.Fatalf("ordinary identifier rejected: %v", err)
	}
}
