// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
	"time"
)

// 造一条「一张 4 MiB 内联图 + 一段文字」的消息，图同时留了本地路径和哈希。
func bigInlineImageEvent(t *testing.T) MessageEvent {
	t.Helper()
	payload := "data:image/png;base64," + strings.Repeat("A", 4<<20)
	return MessageEvent{
		Kind: EventKindGroup, GroupID: "100200301", UserID: "100200711", MessageID: "m-1",
		Segments: []MessageSegment{
			{Type: "text", Data: map[string]string{"text": "看这张"}},
			{Type: "image", Data: map[string]string{
				imageContentSHA256Key: strings.Repeat("ab", 32),
				"cached_file":         "/var/lib/diana/media/abc.png",
				"file":                payload,
				"url":                 payload,
			}},
		},
	}
}

func segmentBytes(segments []MessageSegment) int {
	total := 0
	for _, segment := range segments {
		total += len(segment.Type)
		for key, value := range segment.Data {
			total += len(key) + len(value)
		}
	}
	return total
}

// 入队副本不能带着图片本体。这条量的是实际字节数，不是「看起来像削过了」。
func TestQueueEventDropsImagePayload(t *testing.T) {
	event := bigInlineImageEvent(t)
	before := segmentBytes(event.Segments)
	queued := historyImageDescriptionQueueEvent(event)
	after := segmentBytes(queued.Segments)

	t.Logf("入队前 %d 字节 → 入队后 %d 字节", before, after)
	if before < 4<<20 {
		t.Fatalf("测试数据没造对，原消息只有 %d 字节", before)
	}
	if after > 4096 {
		t.Fatalf("入队副本还带着图片本体：%d 字节", after)
	}
	// 32 个排队任务加起来要远低于一张图。
	if total := after * historyImageDescriptionQueueLimit; total > before {
		t.Fatalf("排满 %d 个任务仍占 %d 字节，比一张原图还多", historyImageDescriptionQueueLimit, total)
	}
}

// 削归削，下游要的东西一样不能少：哈希算得出来、图还定位得到。
func TestStrippedSegmentKeepsHashAndSource(t *testing.T) {
	original := bigInlineImageEvent(t).Segments[1]
	stripped := stripImageSegmentForQueue(original)

	wantHash, ok := imageSegmentContentSHA256(original)
	if !ok {
		t.Fatal("原始段应当算得出哈希")
	}
	gotHash, ok := imageSegmentContentSHA256(stripped)
	if !ok || gotHash != wantHash {
		t.Fatalf("削完哈希对不上：%q vs %q", gotHash, wantHash)
	}
	if source := firstImageSource(stripped); source != "/var/lib/diana/media/abc.png" {
		t.Fatalf("削完定位不到图片：%q", source)
	}
	for _, key := range []string{"file", "url"} {
		if strings.Contains(stripped.Data[key], "base64") {
			t.Fatalf("%s 上还挂着 base64", key)
		}
	}
}

// 只以内联形式存在、没有本地路径的图不入队：留下的那份 base64 正是 OOM 的来源。
// 它仍会在真正被引用、走完整路径时描述。
func TestInlineOnlyImageIsNotQueued(t *testing.T) {
	segment := MessageSegment{Type: "image", Data: map[string]string{
		imageContentSHA256Key: strings.Repeat("cd", 32),
		"file":                "data:image/png;base64," + strings.Repeat("A", 1<<20),
	}}
	if source, retained := queuedImageSourceRetained(segment); retained {
		t.Fatalf("只有内联本体的图不该入队，却拿到来源 %q（%d 字节）", source[:min(40, len(source))], len(source))
	}
}

// 有本地路径的图照常入队。
func TestPathBackedImageIsQueued(t *testing.T) {
	segment := MessageSegment{Type: "image", Data: map[string]string{
		imageContentSHA256Key: strings.Repeat("ef", 32),
		"cached_file":         "/var/lib/diana/media/xyz.png",
		"file":                "data:image/png;base64," + strings.Repeat("A", 1<<20),
	}}
	source, retained := queuedImageSourceRetained(segment)
	if !retained || source != "/var/lib/diana/media/xyz.png" {
		t.Fatalf("有本地路径的图该入队，实际 retained=%v source=%q", retained, source)
	}
}

// Quoted 已经被 historyImageDescriptionEvents 拆成独立事件，副本上再挂一份就是
// 把同一批图片重复钉住一遍。
func TestQueueEventDropsQuoted(t *testing.T) {
	event := bigInlineImageEvent(t)
	quoted := bigInlineImageEvent(t)
	event.Quoted = &QuotedMessage{MessageID: "m-0", Segments: quoted.Segments}
	if queued := historyImageDescriptionQueueEvent(event); queued.Quoted != nil {
		t.Fatal("入队副本不该带 Quoted")
	}
}

// 回填重放的老消息不自动补描述；当前消息照补。
func TestAutoDescriptionWindow(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name  string
		event MessageEvent
		want  bool
	}{
		{"当前消息", MessageEvent{Time: now.Unix()}, true},
		{"一小时前", MessageEvent{Time: now.Add(-time.Hour).Unix()}, true},
		{"窗口边界内", MessageEvent{Time: now.Add(-historyImageDescriptionMaxAge + time.Minute).Unix()}, true},
		{"窗口外一分钟", MessageEvent{Time: now.Add(-historyImageDescriptionMaxAge - time.Minute).Unix()}, false},
		{"半年前的回填消息", MessageEvent{Time: now.AddDate(0, -6, 0).Unix()}, false},
		{"没有时间戳的合成事件", MessageEvent{}, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := withinHistoryImageDescriptionWindow(testCase.event, now); got != testCase.want {
				t.Fatalf("withinHistoryImageDescriptionWindow = %v，想要 %v", got, testCase.want)
			}
		})
	}
}
