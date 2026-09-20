package main

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

// 空 token 曾经等于「不鉴权」。现在握手一律被拒，升级上来的旧配置会静默掉线：
// 客户端每几秒被拒一次，机器人一条消息都收不到。启动时必须直说，而不是让人去翻
// 握手日志——线上那次从掉线到定位隔了半个多小时。
func TestChannelSetFactoryWarnsOnEmptyReverseToken(t *testing.T) {
	build := func(token string) string {
		profile := assistant.DefaultBotConfig()
		profile.ID = "qq"
		profile.Name = "Diana"
		profile.Platform = assistant.PlatformOneBotV11
		profile.Enabled = true
		profile.OneBotTransport = assistant.OneBotTransportReverseWS
		profile.OneBotReverseWSEndpoint = "ws://127.0.0.1:18080/onebot/v11/ws"
		profile.OneBotAccessToken = token

		var logs bytes.Buffer
		previous := log.Writer()
		log.SetOutput(&logs)
		defer log.SetOutput(previous)
		factory := newBotChannelSetFactory(assistant.NewOneBotReverseServer(assistant.OneBotConfig{}), &forwardWSOriginTracker{})
		factory(assistant.ProfileSet{Profiles: []assistant.BotConfig{profile}})
		return logs.String()
	}

	empty := build("")
	if !strings.Contains(empty, "server_token_unset") || !strings.Contains(empty, "Diana") {
		t.Fatalf("空 token 没有给出可操作的提示：%q", empty)
	}
	if configured := build("0123456789abcdef"); strings.Contains(configured, "server_token_unset") {
		t.Fatalf("配好 token 的机器人不该被警告：%q", configured)
	}
}
