// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/json"
	"testing"
)

const (
	dedupGoodURL   = "https://multimedia.nt.qq.com.cn/download?appid=1407&fileid=EhTEST&spec=0&rkey=RKEY"
	dedupBrokenURL = "https://multimedia.nt.qq.com.cn&rkey=RKEY&spec=0"
)

func imageSegments(segments []MessageSegment) []MessageSegment {
	var out []MessageSegment
	for _, segment := range segments {
		if segment.Type == "image" {
			out = append(out, segment)
		}
	}
	return out
}

// 生产上的原样：CQ 字符串里同一张图两遍，第二遍地址残缺（扩展名一个 .jpeg 一个 .jpg）。
// 残缺那一遍丢掉，完整那一遍和 @ 原样保留。
func TestParseOneBotMessageDropsBrokenDuplicateImage(t *testing.T) {
	cq := "[CQ:image,file=a6b66ea50e5951ad.jpeg,url=https://multimedia.nt.qq.com.cn/download?appid=1407&amp;fileid=EhTEST&amp;spec=0&amp;rkey=RKEY]" +
		"[CQ:image,file=a6b66ea50e5951ad.jpg,url=https://multimedia.nt.qq.com.cn&amp;rkey=RKEY&amp;spec=0][CQ:at,qq=42]"
	raw, _ := json.Marshal(cq)
	segments := parseOneBotMessage(raw, "")
	images := imageSegments(segments)
	if len(images) != 1 || images[0].Data["url"] != dedupGoodURL {
		t.Fatalf("应当只留下地址完整的那一张：%#v", segments)
	}
	if len(segments) != 2 || segments[1].Type != "at" {
		t.Fatalf("其余段应当原样保留：%#v", segments)
	}

	// segment 数组形式同样处理。
	array, _ := json.Marshal([]MessageSegment{
		{Type: "image", Data: map[string]string{"file": "cb98.gif", "url": dedupGoodURL}},
		{Type: "image", Data: map[string]string{"file": "cb98.gif", "url": dedupBrokenURL}},
	})
	if images := imageSegments(parseOneBotMessage(array, "")); len(images) != 1 || images[0].Data["url"] != dedupGoodURL {
		t.Fatalf("数组形式也应当去掉残缺的重复：%#v", images)
	}
}

// 不该动的：只有残缺那一份（读失败要照常报出来）、两张不同的图、只给 file 不给 url 的图。
func TestDropBrokenDuplicateImagesKeepsEverythingElse(t *testing.T) {
	cases := []struct {
		name     string
		segments []MessageSegment
		want     int
	}{
		{name: "只有残缺那一份", segments: []MessageSegment{{Type: "image", Data: map[string]string{"file": "a.png", "url": dedupBrokenURL}}}, want: 1},
		{name: "残缺的是另一张图", segments: []MessageSegment{
			{Type: "image", Data: map[string]string{"file": "a.png", "url": dedupGoodURL}},
			{Type: "image", Data: map[string]string{"file": "b.png", "url": dedupBrokenURL}},
		}, want: 2},
		{name: "两张不同的完整图", segments: []MessageSegment{
			{Type: "image", Data: map[string]string{"file": "a.png", "url": dedupGoodURL}},
			{Type: "image", Data: map[string]string{"file": "b.png", "url": dedupGoodURL}},
		}, want: 2},
		{name: "同一张图两遍都完整", segments: []MessageSegment{
			{Type: "image", Data: map[string]string{"file": "a.png", "url": dedupGoodURL}},
			{Type: "image", Data: map[string]string{"file": "a.png", "url": dedupGoodURL}},
		}, want: 2},
		{name: "只给 file 不给 url", segments: []MessageSegment{
			{Type: "image", Data: map[string]string{"file": "a.png", "url": dedupGoodURL}},
			{Type: "image", Data: map[string]string{"file": "a.png"}},
		}, want: 2},
	}
	for _, tc := range cases {
		if got := len(imageSegments(dropBrokenDuplicateImages(tc.segments))); got != tc.want {
			t.Fatalf("%s: 剩 %d 张，want %d", tc.name, got, tc.want)
		}
	}
	for raw, want := range map[string]bool{
		dedupBrokenURL:                  true,
		dedupGoodURL:                    false,
		"":                              false,
		"file:///tmp/a.png":             false,
		"base64://AAAA":                 false,
		"https://example.com/a.png?x=1": false,
	} {
		if got := brokenMediaURL(raw); got != want {
			t.Fatalf("brokenMediaURL(%q)=%v, want %v", raw, got, want)
		}
	}
}
