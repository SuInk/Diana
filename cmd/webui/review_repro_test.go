package main

// 审阅问题 13 复现：通道工厂遇到解析不了的复用连接时静默跳过，既不记日志也不报错。

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func TestReviewRepro13_ChannelFactoryReportsSkippedProfiles(t *testing.T) {
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)

	orphan := assistant.DefaultBotConfig()
	orphan.ID, orphan.Name, orphan.Enabled, orphan.ConnectionProfileID = "orphan", "孤儿机器人", true, "deleted-source"
	channel := newBotChannelSetFactory(assistant.NewOneBotReverseServer(assistant.OneBotConfig{}), &forwardWSOriginTracker{})(
		assistant.ProfileSet{Profiles: []assistant.BotConfig{orphan}},
	).(*assistant.MultiChannel)
	if len(channel.ChannelStatuses()) == 0 && !strings.Contains(logs.String(), "orphan") && !strings.Contains(logs.String(), "孤儿机器人") {
		t.Error("启用中的机器人因连接来源不存在被整台跳过，没有任何日志或状态")
	}
}
