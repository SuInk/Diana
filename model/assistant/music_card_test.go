// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
)

// 卡片模式：QQ 点开直接播曲库原音质，不再下载、转码成 40k 的语音。
func TestMusicRequestToolSendsCardWhenConfigured(t *testing.T) {
	server := musicTestServer(t, 213000, []byte(strings.Repeat("audio", 512)))
	plugin := newMusicTestPlugin(server)
	sharer := &recordingLocalMediaSharer{url: "http://127.0.0.1:18080/media/music-token"}
	plugin.SetLocalMediaSharer(sharer)
	settings := musicRequestSettings(server)
	settings[musicSettingQQSendAs] = "card"

	tool := musicRequestTool(t, plugin, settings)
	output, err := tool.Run(context.Background(), map[string]any{"query": "雾里 姚六一"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(sharer.paths) != 0 {
		t.Fatalf("card mode still downloaded audio: %#v", sharer.paths)
	}
	reply, done := tool.TerminalResult(output)
	if !done {
		t.Fatalf("TerminalResult(%q) did not finish the turn", output)
	}
	segments := CQToSegments(reply)
	if len(segments) != 1 || segments[0].Type != "music" {
		t.Fatalf("reply = %q, want a single music card", reply)
	}
	data := segments[0].Data
	if data["type"] != "custom" || data["title"] != "雾里" || data["content"] != "姚六一" ||
		data["url"] != "https://music.163.com/song?id=1974443814" || !strings.HasSuffix(data["audio"], "/audio/1974443814.mp3") {
		t.Fatalf("card data = %#v", data)
	}
}

// 分享链接触发的那条路径同样发卡片。
func TestMusicPluginSendsCardForSharedSong(t *testing.T) {
	server := musicTestServer(t, 213000, []byte(strings.Repeat("audio", 512)))
	plugin := newMusicTestPlugin(server)
	sharer := &recordingLocalMediaSharer{url: "http://127.0.0.1:18080/media/netease-token"}
	plugin.SetLocalMediaSharer(sharer)
	req := musicTestRequest(server, PlatformOneBotV11)
	req.Settings[musicSettingQQSendAs] = "card"

	resp, err := plugin.Handle(context.Background(), req)
	if err != nil || resp == nil || !resp.Handled {
		t.Fatalf("Handle() = %#v, %v", resp, err)
	}
	if !strings.HasPrefix(resp.Reply, "[CQ:music,type=custom,") || len(sharer.paths) != 0 {
		t.Fatalf("reply = %q, downloads = %#v", resp.Reply, sharer.paths)
	}
}

// 只有 OneBot 认 music 段；Telegram 设成卡片也照旧发音频文件。
func TestMusicCardOnlyForOneBot(t *testing.T) {
	cfg := musicConfigFromSettings(SettingValues{musicSettingQQSendAs: "card"})
	if !musicSendsCard(cfg, PlatformOneBotV11) {
		t.Fatal("OneBot should send a card")
	}
	for _, platform := range []string{PlatformTelegram, PlatformQQOfficial} {
		if musicSendsCard(cfg, platform) {
			t.Fatalf("%s should keep sending audio", platform)
		}
	}
	if musicSendsCard(musicConfigFromSettings(SettingValues{}), PlatformOneBotV11) {
		t.Fatal("default should stay voice")
	}
}

// 播放地址里的逗号、方括号会截断 CQ 码，必须转义。
func TestMusicCardCodeEscapesFields(t *testing.T) {
	code := musicCardCode(song{Source: "qq", ID: "003RMaRI1iFoYd", Name: "A,B [Live]", Artists: "甲&乙", PlayURL: "https://cdn.example.invalid/a.m4a?vkey=x,y&guid=1"})
	segments := CQToSegments(code)
	if len(segments) != 1 {
		t.Fatalf("CQToSegments(%q) = %#v", code, segments)
	}
	data := segments[0].Data
	if data["title"] != "A,B [Live]" || data["content"] != "甲&乙" || data["audio"] != "https://cdn.example.invalid/a.m4a?vkey=x,y&guid=1" || data["url"] != "https://y.qq.com/n/ryqq/songDetail/003RMaRI1iFoYd" {
		t.Fatalf("decoded card = %#v", data)
	}
}

// 签名服务按表单规则解码，原样的 + 会变成空格，网易云地址的签名参数就坏了。
func TestMusicCardCodeProtectsPlusSigns(t *testing.T) {
	code := musicCardCode(song{Source: "netease", ID: "1330348068", Name: "起风了", PlayURL: "http://m801.music.126.net/x.mp3?vuutv=ab+cd/ef+A="})
	data := CQToSegments(code)[0].Data
	if data["audio"] != "http://m801.music.126.net/x.mp3?vuutv=ab%2Bcd/ef%2BA=" {
		t.Fatalf("audio = %q", data["audio"])
	}
}
