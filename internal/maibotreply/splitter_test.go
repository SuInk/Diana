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
		"quote": {"他说「先这样，回头再改」然后走了", []string{"他说「先这样，回头再改」然后走了"}},
		// 收引号和开引号不是同一个字符：引号关上以后照常断句。
		"after corner quote": {"他说「先这样，回头再改」，然后走了", []string{"他说「先这样，回头再改」", "然后走了"}},
		"after curly quote":  {"他回了句“行，你定”，就下线了", []string{"他回了句“行，你定”", "就下线了"}},
		"nested quote":       {"「他说『好，走』，我就走了」，后来", []string{"「他说『好，走』，我就走了」", "后来"}},
		"apostrophe":         {"it's fine，没事", []string{"it's fine", "没事"}},
		// 11-21 线上：英文引号里的撇号被当成收引号，引号提前关上，提示词从逗号处拆成两条。
		"apostrophe in quote": {"提示词是：\"keep each character's hair color recognizable, dynamic funny scene\"，过了的就是这版", []string{"提示词是：\"keep each character's hair color recognizable, dynamic funny scene\"", "过了的就是这版"}},
		"colon":               {"结论：，这个不行", []string{"结论：，这个不行"}},
		"latin pair":          {"用 Go 1.26 跑的", []string{"用", "Go 1.26", "跑的"}},
		"dash":                {"上午 - 下午都在", []string{"上午 - 下午都在"}},
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

// 分界按字数均衡、优先落在句号分号换行处：几个逗号短句合成两条，不按句数切出
// 一条孤零零的尾巴。
func TestMergeBalancesByLength(t *testing.T) {
	got := Merge(SplitSentences("一，二，三，四，五，六，七，八，九，十", never))
	if len(got) < 2 || len(got) > MaxSplitNum || strings.Join(got, "，") != "一，二，三，四，五，六，七，八，九，十" {
		t.Fatalf("got %q", got)
	}
}

// 11-20 线上：真正换话题的是全角分号，从那里断开，不从逗号处把「0 到 100 衡量涨跌
// 势头，过 70 算超买」拆进两条。
func TestMergePrefersSemicolonOverComma(t *testing.T) {
	got := Merge(SplitSentences("俩常见意思，看语境：炒股说的是相对强弱指标，零到一百衡量涨跌势头，过七十算超买、低于三十算超卖；上班摸鱼受伤那个是重复性劳损，腱鞘炎腕管综合征都算，天天敲键盘的熟客", never))
	want := []string{"俩常见意思，看语境：炒股说的是相对强弱指标，零到一百衡量涨跌势头，过七十算超买、低于三十算超卖", "上班摸鱼受伤那个是重复性劳损，腱鞘炎腕管综合征都算，天天敲键盘的熟客"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestMergeCutsAtLineBreaks(t *testing.T) {
	segments := []Segment{
		{Text: "第一行前半", Separator: "，"}, {Text: "第一行后半", Separator: "\n"},
		{Text: "第二行", Separator: "\n"},
		{Text: "第三行前半", Separator: "，"}, {Text: "第三行后半", Separator: "\n"},
		{Text: "第四行"},
	}
	for _, message := range Merge(segments) {
		if strings.HasSuffix(message, "前半") || strings.HasPrefix(message, "第一行后半") || strings.HasPrefix(message, "第三行后半") {
			t.Fatalf("cut inside a line: %q", Merge(segments))
		}
	}
}

// 11-21 线上：两行回复，第二行逗号多，按长度均衡从第二行的逗号处切开，换行反而并进了
// 第一条的气泡里。行数不超过上限时，每个换行都必须是分界。
func TestMergeAlwaysCutsAtNewlineWithinLimit(t *testing.T) {
	segments := []Segment{
		{Text: "一天一本还想天天玩", Separator: "，"}, {Text: "那只能挑薄的买（）", Separator: "\n"},
		{Text: "认真的话就听书", Separator: "，"}, {Text: "通勤吃饭洗漱的时候挂着", Separator: "，"},
		{Text: "一天听完一本不难", Separator: "，"}, {Text: "眼睛该玩照玩"},
	}
	got := Merge(segments)
	if len(got) < 2 || len(got) > MaxSplitNum || got[0] != "一天一本还想天天玩，那只能挑薄的买（）" {
		t.Fatalf("newline should end the first message: %q", got)
	}
	for _, message := range got {
		if strings.Contains(message, "\n") {
			t.Fatalf("newline merged into a bubble: %q", got)
		}
	}
}
