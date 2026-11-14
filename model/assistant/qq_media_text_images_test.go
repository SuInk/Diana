// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

func TestIsQQImageURL(t *testing.T) {
	cases := map[string]bool{
		"https://multimedia.nt.qq.com.cn/download?appid=1407&fileid=X&rkey=Y": true,
		"https://multimedia.nt.qq.com.cn/download?appid=1406&fileid=X&rkey=Y": true,
		"https://multimedia.nt.qq.com.cn/download?appid=1402&fileid=X":        false, // 语音
		"https://multimedia.nt.qq.com.cn&rkey=Y&spec=0":                       false, // 残缺地址
		"https://gchat.qpic.cn/gchatpic_new/1/2-3-ABC/0":                      true,
		"https://pic.ugcimg.cn/abc/0":                                         true,
		"https://x.com/someone/status/12345":                                  false,
	}
	for link, want := range cases {
		if got := isQQImageURL(link); got != want {
			t.Errorf("isQQImageURL(%q) = %v, want %v", link, got, want)
		}
	}
}

// 正文里贴出来的 QQ 图片地址换成图片段，正文里不再留这串地址。
func TestPromoteTextQQImageURLs(t *testing.T) {
	const image = "https://multimedia.nt.qq.com.cn/download?appid=1407&fileid=X&rkey=Y"
	segments := []MessageSegment{
		{Type: "at", Data: map[string]string{"qq": "10001"}},
		{Type: "text", Data: map[string]string{"text": "- [multimedia.nt.qq.com.cn] " + image + "。看看 https://x.com/a/status/1"}},
	}
	got := promoteTextQQImageURLs(segments)
	if len(got) != 3 || got[0].Type != "at" || got[1].Type != "text" || got[2].Type != "image" {
		t.Fatalf("segments = %#v", got)
	}
	if got[2].Data["url"] != image {
		t.Fatalf("image url = %q", got[2].Data["url"])
	}
	if text := got[1].Data["text"]; strings.Contains(text, "multimedia.nt.qq.com.cn/download") || !strings.Contains(text, "。看看 https://x.com/a/status/1") {
		t.Fatalf("text = %q", text)
	}
	if segments[1].Data["text"] == got[1].Data["text"] {
		t.Fatal("原消息段被就地改写")
	}

	// 只有地址的文本段整段换成图片；已有同一张图片段时不重复添加。
	only := []MessageSegment{
		{Type: "text", Data: map[string]string{"text": image}},
		{Type: "image", Data: map[string]string{"url": image}},
	}
	if got := promoteTextQQImageURLs(only); len(got) != 1 || got[0].Type != "image" {
		t.Fatalf("segments = %#v", got)
	}

	// 没有 QQ 图片地址时原样返回。
	plain := []MessageSegment{{Type: "text", Data: map[string]string{"text": "https://multimedia.nt.qq.com.cn/download?appid=1402&fileid=V"}}}
	if got := promoteTextQQImageURLs(plain); len(got) != 1 || got[0].Type != "text" {
		t.Fatalf("segments = %#v", got)
	}
}
