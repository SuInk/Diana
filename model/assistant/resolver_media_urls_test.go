// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// 群友发「链接 + 图片」时，图片段的下载地址不能当成链接去解析。
func TestResolverIgnoresQQMediaURLs(t *testing.T) {
	const tweet = "https://x.com/someone/status/12345"
	const video = "https://www.bilibili.com/video/BV1xx411c7mD"
	req := PluginRequest{
		Text: tweet + " 这是个啥 https://gchat.qpic.cn/gchatpic_new/1/2-3-ABC/0",
		Event: MessageEvent{
			RawMessage: tweet + " 这是个啥" +
				"[CQ:image,file=abc.jpg,url=https://multimedia.nt.qq.com.cn/download?appid=1407&amp;fileid=XYZ&amp;rkey=SECRET]" +
				"[CQ:record,file=a.amr,url=https://multimedia.nt.qq.com.cn/download?appid=1402&amp;fileid=V]" +
				`[CQ:json,data={"meta":{"detail_1":{"qqdocurl":"` + video + `"&#44;"preview":"https://pic.ugcimg.cn/abc/0"}}}]`,
			Segments: []MessageSegment{
				{Type: "text", Data: map[string]string{"text": tweet + " 这是个啥"}},
				{Type: "image", Data: map[string]string{"url": "https://multimedia.nt.qq.com.cn/download?appid=1407&fileid=XYZ&rkey=SECRET"}},
			},
		},
	}
	got := extractResolverRequestURLs(req)
	if !slices.Contains(got, tweet) || !slices.Contains(got, video) {
		t.Fatalf("群友发的链接和分享卡片地址应保留：%v", got)
	}
	for _, link := range got {
		if isQQMediaURL(link) {
			t.Fatalf("QQ 媒体地址被当成链接：%v", got)
		}
	}
}

// 通用抓取失败只剩给模型的备注，不能发群；Context 照常带给模型。
func TestResolverFailedFetchStaysInContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	plugin := NewResolverPlugin(server.Client())
	resp, err := plugin.Handle(context.Background(), PluginRequest{
		Text:     server.URL + " 这是个啥",
		Event:    MessageEvent{Kind: EventKindGroup, GroupID: "10001", UserID: "10002"},
		Settings: SettingValues{resolverSettingFetchTitle: true, resolverSettingDownloadMedia: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil || !strings.Contains(resp.Context, "未能获取网页内容") {
		t.Fatalf("抓取失败要在 Context 里告诉模型：%#v", resp)
	}
	if resp.Reply != "" || resp.Forward || len(resp.ForwardMessages) != 0 {
		t.Fatalf("抓取失败的备注被当成回复发群：reply=%q forward=%v nodes=%d", resp.Reply, resp.Forward, len(resp.ForwardMessages))
	}
	if got := resolverVisibleReply(*resp); got != "" {
		t.Fatalf("解析投递不该回落到 Context：%q", got)
	}
}

// 解析专用路径（只发解析结果、不走模型）拿到只有备注的结果时保持安静。
func TestResolverOnlyPathDoesNotBroadcastContext(t *testing.T) {
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, channel, NewPluginManager(contextOnlyResolverPlugin{}), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "10001", UserID: "10002", MessageID: "m1"}

	reply, err := runtime.replyWithResolverOnly(context.Background(), event, "https://example.com/a")
	if err != nil {
		t.Fatal(err)
	}
	if reply != "" || len(channel.sentSnapshot()) != 0 {
		t.Fatalf("只有给模型的 Context 却发了群：reply=%q sent=%#v", reply, channel.sentSnapshot())
	}
}

type contextOnlyResolverPlugin struct{}

func (contextOnlyResolverPlugin) Manifest() PluginManifest {
	return PluginManifest{ID: resolverPluginID, Name: "resolver context only", BuiltIn: true}
}

func (contextOnlyResolverPlugin) Handle(context.Context, PluginRequest) (*PluginResponse, error) {
	return &PluginResponse{Handled: true, Context: "- [example.com] https://example.com/a\n  备注：未能获取网页内容（可能需要登录或被站点风控），不要编造页面内容。"}, nil
}
