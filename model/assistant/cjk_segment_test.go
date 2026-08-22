// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"slices"
	"testing"
)

// 词典词必须压过跨词碎片:检索词表限量时,先被选走的应当是真实词,
// 「爪很」这类跨词二字组排在它们后面。
func TestStructuredMemorySearchTermsPreferDictionaryWords(t *testing.T) {
	if !awaitCJKSegmenter() {
		t.Fatal("分词词典加载失败")
	}
	terms := structuredMemorySearchTerms("虎皮凤爪很好吃", 4)
	for _, want := range []string{"虎皮", "凤爪", "好吃"} {
		if !slices.Contains(terms, want) {
			t.Fatalf("前 4 个检索词应含 %q,实际 %v", want, terms)
		}
	}
	if slices.Contains(terms, "爪很") || slices.Contains(terms, "皮凤") {
		t.Fatalf("跨词碎片不该挤进前 4:%v", terms)
	}
}

// 词典没收录的词靠 n-gram 兜底,召回下限不因引入词典而下降。
func TestWeightedStructuredMemoryTermsKeepNGramFloor(t *testing.T) {
	if !awaitCJKSegmenter() {
		t.Fatal("分词词典加载失败")
	}
	terms := weightedStructuredMemoryTerms("玄玥岚是谁")
	if terms["玄玥"] < 1 || terms["玥岚"] < 1 {
		t.Fatalf("生僻词的二字组兜底丢了:%v", terms)
	}
	// 词典词权重要高于同长度 n-gram,排序才会先选它们。
	if terms["好吃"] != 0 {
		t.Fatalf("不相关文本混入:%v", terms)
	}
	weighted := weightedStructuredMemoryTerms("会议纪要整理一下")
	if weighted["会议"] <= 1 || weighted["纪要"] <= 1 {
		t.Fatalf("词典词应当拿到高于 n-gram 的权重:%v", weighted)
	}
}
