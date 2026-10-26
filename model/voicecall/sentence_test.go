// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package voicecall

import (
	"reflect"
	"strings"
	"testing"
)

func splitAll(chunks ...string) []string {
	var splitter SentenceSplitter
	var out []string
	for _, chunk := range chunks {
		out = append(out, splitter.Push(chunk)...)
	}
	return append(out, splitter.Flush()...)
}

func TestSentenceSplitterCutsOnSentenceEnds(t *testing.T) {
	got := splitAll("今天天气真不错。我们去公园散步吧！你觉得怎么样？")
	want := []string{"今天天气真不错。", "我们去公园散步吧！", "你觉得怎么样？"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSentenceSplitterWaitsAcrossChunks(t *testing.T) {
	var splitter SentenceSplitter
	if got := splitter.Push("今天天气"); len(got) != 0 {
		t.Fatalf("半句不该交出：%q", got)
	}
	if got := splitter.Push("真不错。我们"); !reflect.DeepEqual(got, []string{"今天天气真不错。"}) {
		t.Fatalf("got %q", got)
	}
	if got := splitter.Flush(); !reflect.DeepEqual(got, []string{"我们"}) {
		t.Fatalf("Flush 应交出剩下的：%q", got)
	}
}

func TestSentenceSplitterMergesShortSentences(t *testing.T) {
	got := splitAll("嗯。好的！那我们明天早上八点见面吧。")
	want := []string{"嗯。好的！那我们明天早上八点见面吧。"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("太短的句子应并到下一句：got %q", got)
	}
}

func TestSentenceSplitterKeepsTrailingMarksAndDecimals(t *testing.T) {
	got := splitAll("真的吗？！我不信……", "价格是 3.5 元. Then we go.")
	want := []string{"真的吗？！我不信……", "价格是 3.5 元.", "Then we go."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSentenceSplitterBreaksLongSentencesAtCommas(t *testing.T) {
	long := strings.Repeat("这是一段很长很长的话，", 12) + "结束。"
	got := splitAll(long)
	if len(got) < 2 {
		t.Fatalf("超长句应在逗号处切开：%q", got)
	}
	for _, sentence := range got {
		if n := len([]rune(sentence)); n > maxSentenceRunes {
			t.Fatalf("切出的句子 %d 字，超过上限：%q", n, sentence)
		}
	}
	if strings.Join(got, "") != long {
		t.Fatalf("切分不应丢字：%q", got)
	}
}

func TestSpeakableTextDropsMarkdownAndURLs(t *testing.T) {
	got := SpeakableText("**重点**：看 [文档](https://example.com/a) 或者 https://example.com/b `code`")
	want := "重点：看 文档 或者 链接 code"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
