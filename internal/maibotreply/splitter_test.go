// SPDX-License-Identifier: GPL-3.0-only
package maibotreply

import (
	"strings"
	"testing"
)

func never() float64  { return 0.99 } // 从不合并：每个分隔符都拆开
func always() float64 { return 0 }    // 能合就合

func texts(segments []Segment) []string {
	out := make([]string, 0, len(segments))
	for _, segment := range segments {
		out = append(out, segment.Text)
	}
	return out
}

func TestSplitSentencesAtSeparatorsAndDropsThem(t *testing.T) {
	got := texts(SplitSentences("今天好冷，出门忘了带围巾。明天再说;算了", never))
	want := []string{"今天好冷", "出门忘了带围巾", "明天再说", "算了"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestSplitSentencesKeepsQuotesColonsAndLatinSpaces(t *testing.T) {
	for name, tc := range map[string]struct {
		text string
		want []string
	}{
		"quote":      {"他说「先这样，回头再改」然后走了", []string{"他说「先这样，回头再改」然后走了"}},
		"colon":      {"结论：，这个不行", []string{"结论：，这个不行"}},
		"latin pair": {"用 Go 1.26 跑的", []string{"用", "Go 1.26", "跑的"}},
		"dash":       {"上午 - 下午都在", []string{"上午 - 下午都在"}},
	} {
		got := texts(SplitSentences(tc.text, never))
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Fatalf("%s: got %q want %q", name, got, tc.want)
		}
	}
}

func TestSplitSentencesNewlineAlwaysSplits(t *testing.T) {
	got := texts(SplitSentences("第一行写长一点好让它合并\n第二行也写长一点", always))
	if len(got) != 2 {
		t.Fatalf("newline must not merge: %q", got)
	}
}

func TestSplitSentencesMergesPairsWhenRandomAllows(t *testing.T) {
	got := texts(SplitSentences("一二三四五六七，八九十一二三四，五六七八九十一，二三四五六七八", always))
	want := []string{"一二三四五六七，八九十一二三四", "五六七八九十一，二三四五六七八"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestProcessMergesToThreeMessages(t *testing.T) {
	got := Process("一，二，三，四，五，六，七", never)
	want := []string{"一，二，三", "四，五", "六，七"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestProcessKeepsWholeTextWhenTooManySentences(t *testing.T) {
	text := "一，二，三，四，五，六，七，八，九"
	if got := Process(text, never); len(got) != 1 || got[0] != text {
		t.Fatalf("over %d sentences should stay whole: %q", MaxSentenceNum, got)
	}
}
