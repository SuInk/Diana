// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"maps"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"
)

// referenceWeightedStructuredMemoryTerms 是改成子串切词之前的写法，留作对照：
// 新写法只改了分配方式，切出来的词和权重必须一字不差。
func referenceWeightedStructuredMemoryTerms(text string) map[string]float64 {
	terms := make(map[string]float64)
	var ascii strings.Builder
	cjk := make([]rune, 0, 16)
	flushASCII := func() {
		if ascii.Len() > 1 {
			terms[strings.ToLower(ascii.String())] = 1.1
		}
		ascii.Reset()
	}
	flushCJK := func() {
		for index, value := range cjk {
			terms[string(value)] = 0.18
			if index+2 <= len(cjk) {
				terms[string(cjk[index:index+2])] = 1
			}
			if index+3 <= len(cjk) {
				terms[string(cjk[index:index+3])] = 1.25
			}
		}
		for _, word := range cjkSegmentWords(string(cjk)) {
			weight := 1.35
			switch length := len([]rune(word)); {
			case length >= 4:
				weight = 1.55
			case length == 3:
				weight = 1.45
			}
			if weight > terms[word] {
				terms[word] = weight
			}
		}
		cjk = cjk[:0]
	}
	for _, value := range strings.ToLower(text) {
		switch {
		case value <= unicode.MaxASCII && (unicode.IsLetter(value) || unicode.IsDigit(value)):
			flushCJK()
			ascii.WriteRune(value)
		case unicode.In(value, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul):
			flushASCII()
			cjk = append(cjk, value)
		default:
			flushASCII()
			flushCJK()
		}
	}
	flushASCII()
	flushCJK()
	return terms
}

var structuredMemoryTermSamples = []string{
	"",
	"a",
	"虎皮凤爪很好吃",
	"Winter 喜欢 Rust 和 Go1.26，不喜欢香菜",
	"ＡＢＣ全角字母和KELVIN符号K混在一起",
	"ひらがなとカタカナ、한국어도 섞여요ok",
	"末尾是汉字",
	"x中y文z",
	"emoji😀之间的中文😀English",
	"invalid\xff\xfe字节\xc3",
	strings.Repeat("长段落没有标点", 20),
}

func TestWeightedStructuredMemoryTermsMatchesReference(t *testing.T) {
	for _, sample := range structuredMemoryTermSamples {
		got := weightedStructuredMemoryTerms(sample)
		want := referenceWeightedStructuredMemoryTerms(sample)
		if !maps.Equal(got, want) {
			t.Fatalf("terms for %q differ:\n got  %v\n want %v", sample, got, want)
		}
	}
}

func TestStructuredMemoryMatchedTermsIsIntersection(t *testing.T) {
	query := weightedStructuredMemoryTerms("凤爪 go 好吃吗 rust")
	for _, sample := range structuredMemoryTermSamples {
		want := map[string]struct{}{}
		for term := range structuredMemoryTerms(sample) {
			if _, ok := query[term]; ok {
				want[term] = struct{}{}
			}
		}
		got := structuredMemoryMatchedTerms(sample, query)
		if len(got) != len(want) {
			t.Fatalf("matched terms for %q: got %v want %v", sample, got, want)
		}
		for term := range want {
			if _, ok := got[term]; !ok {
				t.Fatalf("matched terms for %q miss %q", sample, term)
			}
		}
	}
}

func BenchmarkRankStructuredMemories(b *testing.B) {
	defer cjkSegmentEnabled.Store(cjkSegmentEnabled.Load())
	cjkSegmentEnabled.Store(false) // 生产默认不开词典分词
	topics := []string{"饮食偏好", "游戏", "工作", "宠物", "旅行", "音乐", "学习", "作息"}
	contents := []string{
		"喜欢吃虎皮凤爪和麻辣小龙虾，周末常去夜市，不吃香菜",
		"最近在玩 Elden Ring，卡在第二个 boss 好几天了",
		"在杭州做后端开发，主要写 Go 和 Rust，经常加班到很晚",
		"养了一只橘猫叫团子，特别能吃，体重已经超标",
		"去年国庆去了一趟成都，说那边的火锅比重庆的好吃",
		"喜欢听坂本龙一和久石让，偶尔自己弹钢琴",
		"在准备考研，目标是计算机专业，每天刷题到凌晨",
		"作息很乱，经常凌晨三点还在群里说话",
	}
	items := make([]StructuredMemoryItem, 0, 400)
	for index := range 400 {
		items = append(items, StructuredMemoryItem{
			ID:         "m" + strconv.Itoa(index),
			Kind:       MemoryKindFact,
			Entity:     "群友" + strconv.Itoa(index%37),
			Topic:      topics[index%len(topics)],
			Key:        "k" + strconv.Itoa(index),
			Content:    contents[index%len(contents)] + "，第" + strconv.Itoa(index) + "条",
			Evidence:   "他在群里说过：" + contents[(index+3)%len(contents)],
			Importance: 0.6,
			Confidence: 0.8,
		})
	}
	event := MessageEvent{UserID: "10001", GroupID: "20002", Kind: EventKindGroup}
	b.ReportAllocs()
	for b.Loop() {
		rankStructuredMemories(items, event, "你还记得他喜欢吃什么吗，凤爪还是火锅", time.Now())
	}
}
