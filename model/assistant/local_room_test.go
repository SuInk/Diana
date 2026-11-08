// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
	"time"
)

// newLocalRoomTestRuntime 起一个有两台内置机器人的运行时，模型对所有调用都回同一句。
func newLocalRoomTestRuntime(t *testing.T, reply string) (*Runtime, *recordingChannel, *capturingLLMProvider) {
	t.Helper()
	channel := &recordingChannel{}
	provider := &capturingLLMProvider{reply: reply}
	runtime := NewRuntime(BotConfig{ID: "hong", Name: "虹夏", Platform: PlatformLocal}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	runtime.mu.Lock()
	runtime.profileConfigs["ryo"] = BotConfig{ID: "ryo", Name: "凉", Platform: PlatformLocal}
	runtime.mu.Unlock()
	return runtime, channel, provider
}

func waitLocalRoomIdle(t *testing.T, runtime *Runtime, id string) ([]LocalRoomMessage, LocalRoomStatus) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, messages, status, err := runtime.LocalRoomHistory(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if status.Thinking == 0 {
			return messages, status
		}
		if time.Now().After(deadline) {
			t.Fatalf("讨论停不下来：%#v", status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// 主人在群里说一句，每个成员都回；回复不发到任何平台。
func TestLocalRoomOwnerMessageGetsEveryMemberReply(t *testing.T) {
	runtime, channel, _ := newLocalRoomTestRuntime(t, "来了来了")
	room, err := runtime.CreateLocalRoom(context.Background(), LocalRoom{Name: "结束乐队", Members: []string{"hong", "ryo"}, MaxBotTurns: 2})
	if err != nil {
		t.Fatal(err)
	}
	var streamed []LocalRoomMessage
	owner, err := runtime.LocalRoomSend(context.Background(), room.ID, "今天排练吗", func(msg LocalRoomMessage) {
		streamed = append(streamed, msg)
	})
	if err != nil {
		t.Fatal(err)
	}
	messages, status := waitLocalRoomIdle(t, runtime, room.ID)
	if len(messages) == 0 || messages[0].ID != owner.ID || messages[0].SenderID != localRoomOwnerSender {
		t.Fatalf("第一条应是主人的话：%#v", messages)
	}
	repliedBy := map[string]bool{}
	bots := 0
	for _, msg := range messages[1:] {
		repliedBy[msg.SenderID] = true
		bots++
	}
	if !repliedBy["hong"] || !repliedBy["ryo"] {
		t.Fatalf("两个成员都该回主人：%#v", messages)
	}
	// 刹车 2 条：到数以后不再往下分发，最多再多出正在想的那几条。
	if bots > 2+len(room.Members) {
		t.Fatalf("机器人连续发言 %d 条，刹车没起作用：%#v", bots, messages)
	}
	if !status.Paused {
		t.Fatalf("到了刹车条数应暂停：%#v", status)
	}
	if len(streamed) == 0 {
		t.Fatal("讨论里的消息应推给页面")
	}
	channel.mu.Lock()
	sent := len(channel.sent)
	channel.mu.Unlock()
	if sent != 0 {
		t.Fatalf("本地群的回复不该发到平台：%#v", channel.sent)
	}
}

// 成员各自的群会话里看得到别人说的话：主人的和另一个成员的。
func TestLocalRoomMembersSeeEachOther(t *testing.T) {
	runtime, _, _ := newLocalRoomTestRuntime(t, "我去拿贝斯")
	room, err := runtime.CreateLocalRoom(context.Background(), LocalRoom{Name: "结束乐队", Members: []string{"hong", "ryo"}, MaxBotTurns: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.LocalRoomSend(context.Background(), room.ID, "集合了", nil); err != nil {
		t.Fatal(err)
	}
	waitLocalRoomIdle(t, runtime, room.ID)
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	history := runtime.history["hong:group:"+localRoomGroupPrefix+room.ID]
	var sawOwner, sawRyo bool
	for _, event := range history {
		if !event.Outbound && strings.Contains(PlainText(event.Segments), "集合了") {
			sawOwner = true
		}
		if !event.Outbound && event.UserID == "local-ryo" {
			sawRyo = true
		}
	}
	if !sawOwner || !sawRyo {
		t.Fatalf("虹夏的群会话应看到主人和凉的发言：owner=%v ryo=%v history=%d", sawOwner, sawRyo, len(history))
	}
}

func TestLocalRoomStopPausesUntilOwnerSpeaks(t *testing.T) {
	runtime, _, _ := newLocalRoomTestRuntime(t, "嗯")
	room, err := runtime.CreateLocalRoom(context.Background(), LocalRoom{Name: "群", Members: []string{"hong", "ryo"}})
	if err != nil {
		t.Fatal(err)
	}
	status, err := runtime.StopLocalRoom(context.Background(), room.ID)
	if err != nil || !status.Paused {
		t.Fatalf("停止后应暂停：%#v %v", status, err)
	}
	if _, err := runtime.LocalRoomSend(context.Background(), room.ID, "继续", nil); err != nil {
		t.Fatal(err)
	}
	_, messages, _, _ := runtime.LocalRoomHistory(context.Background(), room.ID)
	if len(messages) < 2 {
		t.Fatalf("主人说话后成员应接着回：%#v", messages)
	}
}

func TestLocalRoomRejectsUnknownMembersAndEmptyName(t *testing.T) {
	runtime, _, _ := newLocalRoomTestRuntime(t, "")
	if _, err := runtime.CreateLocalRoom(context.Background(), LocalRoom{Name: "群", Members: []string{"ghost"}}); err == nil {
		t.Fatal("不存在的机器人不能进群")
	}
	if _, err := runtime.CreateLocalRoom(context.Background(), LocalRoom{Name: " ", Members: []string{"hong"}}); err == nil {
		t.Fatal("群名不能为空")
	}
	room, err := runtime.CreateLocalRoom(context.Background(), LocalRoom{Name: "群", Members: []string{"hong", "hong", "ryo"}})
	if err != nil || len(room.Members) != 2 {
		t.Fatalf("重复成员应去重：%#v %v", room, err)
	}
	if err := runtime.DeleteLocalRoom(context.Background(), room.ID); err != nil {
		t.Fatal(err)
	}
	if rooms := runtime.LocalRooms(context.Background()); len(rooms) != 0 {
		t.Fatalf("解散后不该还在：%#v", rooms)
	}
}

func TestParseLocalRoomPlan(t *testing.T) {
	plan, err := parseLocalRoomPlan("好的：\n{\"room_name\":\"结束乐队\",\"bots\":[{\"name\":\"虹夏\",\"persona\":\"你是鼓手\"},{\"name\":\" \",\"persona\":\"x\"},{\"name\":\"小孤独\",\"persona\":\"你是吉他手\"}]}")
	if err != nil {
		t.Fatal(err)
	}
	if plan.RoomName != "结束乐队" || len(plan.Bots) != 2 || plan.Bots[1].Name != "小孤独" {
		t.Fatalf("plan = %#v", plan)
	}
	if _, err := parseLocalRoomPlan("没法建"); err == nil {
		t.Fatal("没有 JSON 时应报错")
	}
}
