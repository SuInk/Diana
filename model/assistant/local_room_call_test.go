// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/voicecall"
)

// 群通话里主人说一句，每个成员的回复都带着是谁说的；成员按群通话的口吻回，挂断后
// 群里记一条通话记录。
func TestLocalRoomCallTagsSpeakersAndUsesCallPrompt(t *testing.T) {
	runtime, _, provider := newLocalRoomTestRuntime(t, "我带贝斯")
	room, err := runtime.CreateLocalRoom(context.Background(), LocalRoom{Name: "结束乐队", Members: []string{"hong", "ryo"}, MaxBotTurns: 1})
	if err != nil {
		t.Fatal(err)
	}
	call := &LocalRoomCall{runtime: runtime, room: room, started: time.Now().Add(-2 * time.Minute)}
	runtime.setLocalRoomCalling(room.ID, 1)
	var mu sync.Mutex
	spoken := map[string]string{}
	if err := call.ReplyBySpeaker(context.Background(), "今晚几点排练", func(speaker voicecall.Speaker, text string) {
		mu.Lock()
		spoken[speaker.ID] = speaker.Name + ":" + text
		mu.Unlock()
	}); err != nil {
		t.Fatal(err)
	}
	waitLocalRoomIdle(t, runtime, room.ID)
	mu.Lock()
	if !strings.HasPrefix(spoken["hong"], "虹夏:") || !strings.HasPrefix(spoken["ryo"], "凉:") {
		t.Fatalf("每条回复都要标发言人：%#v", spoken)
	}
	mu.Unlock()
	provider.mu.Lock()
	prompt := ""
	for _, message := range provider.request.Messages {
		prompt += message.Content
	}
	provider.mu.Unlock()
	if !strings.Contains(prompt, "群语音通话") || strings.Contains(prompt, "hang_up") {
		t.Fatalf("群通话要用群通话的口吻提示、不提 hang_up：%s", prompt)
	}

	call.Finish()
	call.Finish()
	if runtime.localRoomCalling(room.ID) {
		t.Fatal("挂断后不该再按通话口吻回复")
	}
	_, messages, _, err := runtime.LocalRoomHistory(context.Background(), room.ID)
	if err != nil {
		t.Fatal(err)
	}
	records := 0
	for _, msg := range messages {
		if msg.Kind == localRoomCallKind {
			records++
		}
	}
	if records != 1 {
		t.Fatalf("挂断应记一条通话记录：%#v", messages)
	}
}

// 成员都没配语音插槽时打不了群电话，原因要说清楚。
func TestLocalRoomCallNeedsVoiceSlots(t *testing.T) {
	runtime, _, _ := newLocalRoomTestRuntime(t, "嗯")
	room, err := runtime.CreateLocalRoom(context.Background(), LocalRoom{Name: "结束乐队", Members: []string{"hong", "ryo"}})
	if err != nil {
		t.Fatal(err)
	}
	info := runtime.LocalRoomCallInfo(context.Background(), room.ID)
	if info.BotName != "结束乐队" || !strings.Contains(info.Problem, "语音识别") {
		t.Fatalf("info = %#v", info)
	}
	if _, err := runtime.NewLocalRoomCall(context.Background(), room.ID); err == nil {
		t.Fatal("没配语音插槽不该建得起来")
	}
	if runtime.localRoomCalling(room.ID) {
		t.Fatal("没打通不该算在通话")
	}
}
