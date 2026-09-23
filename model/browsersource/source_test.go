// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package browsersource

import (
	"reflect"
	"testing"
)

func TestWithDefaultsNormalizesOrder(t *testing.T) {
	cases := []struct {
		in, want []string
	}{
		{nil, []string{Box, Extension}},
		{[]string{Extension}, []string{Extension, Box}},
		{[]string{"cdp", Extension, Extension, Box}, []string{Extension, Box}},
	}
	for _, tc := range cases {
		if got := (Settings{Order: tc.in}).WithDefaults().Order; !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("WithDefaults(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// 排在前面的用不了就换下一个；都用不了就是不用。
func TestPickFallsBackInOrder(t *testing.T) {
	usable := map[string]bool{}
	pick := func(order ...string) string {
		return Pick(order, func(source string) bool { return usable[source] })
	}
	if got := pick(Extension, Box); got != Off {
		t.Fatalf("都用不了时应是 Off，实际 %q", got)
	}
	usable[Box] = true
	if got := pick(Extension, Box); got != Box {
		t.Fatalf("扩展用不了时应换内置，实际 %q", got)
	}
	usable[Extension] = true
	if got := pick(Extension, Box); got != Extension {
		t.Fatalf("两个都能用时按顺序取扩展，实际 %q", got)
	}
	if got := pick(Box, Extension); got != Box {
		t.Fatalf("两个都能用时按顺序取内置，实际 %q", got)
	}
}
