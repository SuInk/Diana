// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/internal/secretmask"
)

// 测试里的假凭据一律拆开拼接，公开仓库审计按连续字面量找凭据。

func closedLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	return address
}

// 订阅地址里的私有令牌（Gitea、RSSHub 的 ?token=）会随 HTTP 客户端报错带出来，
// 而这条报错会进工具结果、订阅的 LastError 和运行状态。
func TestRSSFetchErrorMasksFeedToken(t *testing.T) {
	token := "rsstok" + "0123456789abcdef"
	plugin := NewRSSWatchPlugin(&http.Client{Timeout: 2 * time.Second})
	feedURL := "http://" + closedLoopbackAddress(t) + "/o/r.rss?token=" + token
	_, err := plugin.fetchFresh(context.Background(), feedURL, SettingValues{})
	if err == nil {
		t.Fatal("连不上的地址应当报错")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("抓取报错里有令牌原文：%v", err)
	}
	// 之后别处（LastError、运行状态）再出现这个令牌也认得出。
	if strings.Contains(secretmask.Known("last error: "+token), token) {
		t.Fatal("抓取过的订阅地址里的令牌应当已登记")
	}
}

// 模型只见过掩码地址，改订阅时原样带回来要还原成原文，不能把掩码存下去；对不上的
// 掩码拒绝，新建订阅时交掩码也拒绝。
func TestRSSWatchToolMasksFeedURLAndRestoresOnUpdate(t *testing.T) {
	token := "gitea" + "0123456789abcdef"
	stored := "https://git.example/o/r.rss?token=" + token
	item := Reminder{ID: "rss-1", Kind: ReminderKindRSSWatch, FeedURL: stored, LastError: `抓取 Feed 失败: Get "` + stored + `": dial tcp: i/o timeout`}
	view := rssWatchForTool(item)
	body, _ := json.Marshal(view)
	if strings.Contains(string(body), token) {
		t.Fatalf("交给模型的订阅里有令牌原文：%s", body)
	}
	masked := view.FeedURL
	if !strings.Contains(masked, secretmask.Marker) || !strings.Contains(masked, "git.example/o/r.rss") {
		t.Fatalf("地址应当保留、令牌换成掩码：%s", masked)
	}

	restored, err := restoreMaskedFeedURLs([]string{masked, "https://other.example/feed"}, ReminderFeedSources(item))
	if err != nil {
		t.Fatal(err)
	}
	if restored[0] != stored || restored[1] != "https://other.example/feed" {
		t.Fatalf("原样交回的掩码应当还原成原文，其余不动：%v", restored)
	}
	if _, err := restoreMaskedFeedURLs([]string{"https://evil.example/feed?token=" + secretmask.Mask(token)}, ReminderFeedSources(item)); err == nil {
		t.Fatal("对不上已保存地址的掩码应当拒绝")
	}
	if _, err := resolveRSSWatchSources([]string{masked}, nil); err == nil {
		t.Fatal("新建订阅时交掩码地址应当拒绝，否则存下的是一串星号")
	}
}

// config 工具给模型看的运行状态：连接地址里的 access_token、上次失败的原始报错都只给掩码。
func TestConfigToolRuntimeSnapshotMasksCredentials(t *testing.T) {
	token := "obtok" + "0123456789abcdef"
	endpoint := "ws://127.0.0.1:3001/?access_token=" + token
	status := RuntimeStatus{
		Channel:        ChannelStatus{Endpoint: endpoint, LastError: `dial ` + endpoint + `: connection refused`},
		NoneBotBridges: map[string]NoneBotBridgeStatus{"bot": {Endpoint: endpoint, LastError: "bad handshake at " + endpoint}},
		LastError:      `Post "` + endpoint + `": EOF`,
	}
	body, _ := json.Marshal(dianaRuntimeFromStatus(status, "bot"))
	if strings.Contains(string(body), token) {
		t.Fatalf("运行状态里有令牌原文：%s", body)
	}
	cfg := dianaBotConfigFromConfig(BotConfig{OneBotWSEndpoint: endpoint, OneBotHTTPURL: "http://127.0.0.1:5700/?access_token=" + token})
	if body, _ := json.Marshal(cfg); strings.Contains(string(body), token) {
		t.Fatalf("机器人配置快照里有令牌原文：%s", body)
	}
}

// 外发消息的最后一道：已登记的凭据原文不能被原样发进群。
func TestOutgoingMessagesMaskRegisteredSecrets(t *testing.T) {
	botToken := "tg" + "0123456789abcdefghijkl"
	registerBotConfigSecrets(BotConfig{TelegramBotToken: botToken})
	segmentData := map[string]string{"text": "token=" + botToken}
	msg := maskOutgoingSecrets(OutgoingMessage{
		Text:     "我的令牌是 " + botToken,
		Segments: []MessageSegment{{Type: "text", Data: segmentData}, {Type: "image", Data: map[string]string{"file": "a.png"}}},
	})
	if strings.Contains(msg.Text, botToken) || strings.Contains(msg.Segments[0].Data["text"], botToken) {
		t.Fatalf("外发消息里有令牌原文：%#v", msg)
	}
	if segmentData["text"] != "token="+botToken {
		t.Fatal("不该改调用方传进来的消息段")
	}
	if msg.Segments[1].Data["file"] != "a.png" {
		t.Fatalf("非文本段不该被动：%#v", msg.Segments[1])
	}
	plain := maskOutgoingSecrets(OutgoingMessage{Text: "https://example.com/?token=abcdef0123456789"})
	if plain.Text != "https://example.com/?token=abcdef0123456789" {
		t.Fatalf("没登记的内容（别人贴的链接）不该被改：%q", plain.Text)
	}
}

// 中转网关把 Key 原样回显在报错里时（没有 key= 这种形态），聊天里的错误说明也只给掩码。
func TestPublicErrorMasksRegisteredBareKey(t *testing.T) {
	apiKey := "sk-" + "relay0123456789abcdefgh"
	secretmask.Register(apiKey)
	got := sanitizePublicErrorDetail("upstream: invalid api key " + apiKey + " provided")
	if strings.Contains(got, apiKey) {
		t.Fatalf("聊天里的错误说明有 Key 原文：%s", got)
	}
}

// 插件凭据：Secret 设置整值登记，JSON 形式的按仓库 Token 表逐个登记；普通设置里的
// 地址只登记嵌着的凭据。
func TestRegisterPluginSecrets(t *testing.T) {
	repoToken := "ghp_" + "plugin0123456789abcdefghij"
	cookieValue := "sess" + "0123456789abcd"
	proxyPassword := "px" + "0123456789ab"
	registerPluginSecrets(PluginState{
		Manifest: PluginManifest{Settings: []PluginSettingSpec{
			{Key: "tokens", Secret: true},
			{Key: "cookie", Secret: true},
			{Key: "proxy_url"},
			{Key: "title"},
		}},
		Settings: map[string]any{
			"tokens":    `{"cred-1":"` + repoToken + `"}`,
			"cookie":    "SESSDATA=" + cookieValue + "; lang=zh",
			"proxy_url": "http://" + "u:" + proxyPassword + "@127.0.0.1:7890",
			"title":     "diana-plugin-title",
		},
	})
	text := "GitHub API 401 for " + repoToken + " cookie " + cookieValue + " proxy " + proxyPassword + " title diana-plugin-title"
	got := secretmask.Known(text)
	for _, secret := range []string{repoToken, cookieValue, proxyPassword} {
		if strings.Contains(got, secret) {
			t.Fatalf("插件凭据 %s 没登记：%s", secret, got)
		}
	}
	if !strings.Contains(got, "diana-plugin-title") {
		t.Fatalf("普通设置不该登记：%s", got)
	}
}
