package assistant

import "testing"

func TestQQMarkdownLineBreaks(t *testing.T) {
	cases := map[string]string{
		"第一行\n第二行":               "第一行\n\n第二行",
		"说明\n- a\n- b\n  续行\n结尾": "说明\n\n- a\n- b\n  续行\n\n结尾",
		"> 引一\n> 引二\n正文":         "> 引一\n> 引二\n\n正文",
		"1. 一\n2. 二":             "1. 一\n2. 二",
		"公式\n$$\na+b\n=c\n$$\n完": "公式\n\n$$\na+b\n=c\n$$\n\n完",
		"已有空行\n\n不再加":            "已有空行\n\n不再加",
	}
	for input, want := range cases {
		if got := qqMarkdownLineBreaks(input); got != want {
			t.Errorf("qqMarkdownLineBreaks(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestQQOfficialMarkdownContentLineBreaksAndLatex(t *testing.T) {
	got, ok := qqOfficialMarkdownContent("**结论**先行\n- 第一点\n- 第二点\n```go\nx := 1\ny := 2\n```\n完")
	want := "**结论**先行\n\n- 第一点\n- 第二点\n\n```go\nx := 1\ny := 2\n```\n\n完"
	if !ok || got != want {
		t.Fatalf("content = %q ok=%v, want %q", got, ok, want)
	}
	for _, text := range []string{"$$E=mc^2$$", "面积是 $\\pi r^2$"} {
		if _, ok := qqOfficialMarkdownContent(text); !ok {
			t.Fatalf("%q 是公式，应当走 Markdown", text)
		}
	}
	for _, text := range []string{"一杯 $5，两杯 $10", "就一句话"} {
		if _, ok := qqOfficialMarkdownContent(text); ok {
			t.Fatalf("%q 不是 Markdown，不该换消息类型", text)
		}
	}
}
