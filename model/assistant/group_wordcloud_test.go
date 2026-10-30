// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"image/png"
	"os"
	"strings"
	"testing"
)

func TestWordCloudCounterHeuristic(t *testing.T) {
	counter := &WordCloudCounter{counts: map[string]int{}}
	for _, text := range []string{
		"哈哈哈哈哈", "哈哈哈", "下班了吗", "下班", "我觉得 OK", "@某人 下班[图片]", "草", "草",
		"1234 5678", "cool cool cool",
	} {
		counter.Add(text)
	}
	got := map[string]int{}
	for _, word := range counter.Top(0) {
		got[word.Text] = word.Count
	}
	if got["哈哈哈"] != 2 {
		t.Fatalf("叠字没收拢：%v", got)
	}
	if got["下班"] != 3 {
		t.Fatalf("下班 = %d, want 3：%v", got["下班"], got)
	}
	if got["草"] != 2 {
		t.Fatalf("整句单字回复应保留：%v", got)
	}
	for _, unwanted := range []string{"我", "觉得", "ok", "图片", "1234", "某人"} {
		if _, ok := got[unwanted]; ok {
			t.Fatalf("%q 不该进词云：%v", unwanted, got)
		}
	}
	if _, ok := got["cool"]; ok {
		t.Fatalf("只出现在一条消息里的词不该算多次：%v", got)
	}
}

func TestWordCloudCounterDictionary(t *testing.T) {
	if !awaitCJKSegmenter() {
		t.Skip("词典加载失败")
	}
	counter := &WordCloudCounter{counts: map[string]int{}, dict: true}
	counter.Add("今天的虎皮凤爪很好吃")
	counter.Add("虎皮凤爪又涨价了")
	top := counter.Top(0)
	if len(top) == 0 {
		t.Fatal("词典分词没切出词")
	}
	for _, word := range top {
		if word.Text == "爪很" {
			t.Fatalf("词典分词切出了跨词碎片：%v", top)
		}
	}
}

func TestRenderGroupWordCloudPNG(t *testing.T) {
	if _, _, err := LoadCJKFont(); err != nil {
		t.Skipf("这台机器上没有能画中文的字体：%v", err)
	}
	cloud := GroupWordCloud{GroupID: "55501", Messages: 320, Speakers: 12}
	for index, text := range []string{"哈哈哈", "下班", "火锅", "周末", "摸鱼", "老板", "奶茶", "加班", "草", "Switch", "显卡", "番剧"} {
		cloud.Words = append(cloud.Words, GroupWordCloudWord{Text: text, Count: 90 - index*7})
	}
	encoded, err := RenderGroupWordCloudPNG(cloud, "群 55501 · 词云", "最近 7 天")
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if bounds := img.Bounds(); bounds.Dx() != wordCloudImageWidth || bounds.Dy() != wordCloudImageHeight {
		t.Fatalf("尺寸 = %v", bounds)
	}
	if path := os.Getenv("DIANA_WORDCLOUD_PNG"); path != "" {
		_ = os.WriteFile(path, encoded, 0o644)
	}
}

func TestGroupWordCloudToolWithoutStoreOrGroup(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	for _, event := range []MessageEvent{
		{Kind: EventKindPrivate, UserID: "10001"},
		{Kind: EventKindGroup, GroupID: "55501", UserID: "10001"},
	} {
		output, err := newDianaGroupWordCloudTool(runtime, event, SettingValues{}).Run(context.Background(), map[string]any{})
		if err != nil || !strings.Contains(output, `"ok":false`) {
			t.Fatalf("output = %s, err = %v", output, err)
		}
	}
}
