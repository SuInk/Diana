// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
)

// X 图片下载到宿主机临时目录后,路径必须换成桥能访问的共享 URL 再发出去。
// 桥运行在容器或另一台机器上时读不到宿主机路径,合并转发、暂存、散装会
// 挨个失败,重试耗尽后整条事件被丢弃——视频早有这层转换,图片曾经漏掉。
func TestOutgoingLocalImagePathsAreSharedBeforeSend(t *testing.T) {
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	sharer := &recordingLocalMediaSharer{url: "https://share.example/img.jpg"}
	runtime.SetLocalMediaSharer(sharer)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20005", UserID: "10001", MessageID: "m1"}

	err := runtime.sendOutgoing(context.Background(), event, OutgoingMessage{
		ImageURLs: []string{"file:///var/folders/tmp/diana-resolver-image-1/image.jpg", "https://cdn.example/keep.png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	sent := channel.sentSnapshot()
	if len(sent) != 1 {
		t.Fatalf("sent = %d", len(sent))
	}
	if sent[0].ImageURLs[0] != "https://share.example/img.jpg" {
		t.Fatalf("本地路径没有换成共享 URL:%#v", sent[0].ImageURLs)
	}
	if sent[0].ImageURLs[1] != "https://cdn.example/keep.png" {
		t.Fatalf("远程 URL 不该被改写:%#v", sent[0].ImageURLs)
	}
	if len(sharer.paths) != 1 || !strings.HasSuffix(sharer.paths[0], "image.jpg") {
		t.Fatalf("共享的应是本地文件路径:%#v", sharer.paths)
	}
}

// 没有配置共享器(桥与宿主同机部署)时保留原路径,行为不变。
func TestOutgoingLocalImagePathsKeptWithoutSharer(t *testing.T) {
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20005", UserID: "10001", MessageID: "m1"}

	if err := runtime.sendOutgoing(context.Background(), event, OutgoingMessage{
		ImageURLs: []string{"/var/folders/tmp/diana-resolver-image-1/image.jpg"},
	}); err != nil {
		t.Fatal(err)
	}
	sent := channel.sentSnapshot()
	if len(sent) != 1 || sent[0].ImageURLs[0] != "/var/folders/tmp/diana-resolver-image-1/image.jpg" {
		t.Fatalf("无共享器时应保留原路径:%#v", sent)
	}
}
