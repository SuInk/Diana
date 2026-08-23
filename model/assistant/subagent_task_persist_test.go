// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// recordingSubtaskStore 同时实现 MessageHistoryStore（注入所需）与
// InboundEventSubtaskStore（本次要验证的可选接口）。
type recordingSubtaskStore struct {
	mu    sync.Mutex
	saved []InboundEventSubtask
}

func (s *recordingSubtaskStore) AppendMessageEvent(context.Context, string, MessageEvent) error {
	return nil
}

func (s *recordingSubtaskStore) ListRecentMessageEvents(context.Context, string, int) ([]MessageEvent, error) {
	return nil, nil
}

func (s *recordingSubtaskStore) SaveInboundEventSubtask(_ context.Context, item InboundEventSubtask) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saved = append(s.saved, item)
	return nil
}

func (s *recordingSubtaskStore) snapshot() []InboundEventSubtask {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]InboundEventSubtask(nil), s.saved...)
}

// 后台子任务此前只活在内存的 RuntimeStatus 里，跑完就没了，事件详情查不到「这条
// 消息到底触发了什么」。任务跑在自己的 goroutine 上、用的是运行时根 context，拿不到
// 那一轮的出站账本，所以入站事件 ID 必须在预约时就捕获。
func TestPluginTaskPersistsAgainstItsInboundEvent(t *testing.T) {
	store := &recordingSubtaskStore{}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)

	event := MessageEvent{Kind: EventKindGroup, GroupID: "20005", UserID: "10001", MessageID: "m-1"}
	done := make(chan struct{})
	task := PluginTask{
		Kind: "image", Name: "图片生成",
		Run: func(_ context.Context, services PluginTaskServices) (PluginTaskResult, error) {
			services.Report(PluginTaskProgress{Phase: "running", Message: "正在编辑第 1/2 张", Completed: 1, Total: 2})
			close(done)
			return PluginTaskResult{Reply: "好了"}, nil
		},
	}

	ctx := withOutboundTurn(context.Background(), "evt-42")
	reservation := runtime.reservePluginTasksForTurn(ctx, event, []PluginTask{task})
	if !reservation.handled || len(reservation.reserved) != 1 {
		t.Fatalf("reservation = %#v", reservation)
	}
	if reservation.reserved[0].eventID != "evt-42" {
		t.Fatalf("入站事件 ID 没有被捕获：%q", reservation.reserved[0].eventID)
	}
	runtime.startPluginTaskReservation(reservation)

	<-done
	waitForCondition(t, 5*time.Second, func() bool {
		for _, item := range store.snapshot() {
			if item.Phase == "completed" {
				return true
			}
		}
		return false
	})

	saved := store.snapshot()
	phases := map[string]InboundEventSubtask{}
	for _, item := range saved {
		if item.EventID != "evt-42" {
			t.Fatalf("子任务挂错了事件：%#v", item)
		}
		if item.Kind != "image" || item.Name != "图片生成" {
			t.Fatalf("子任务身份丢失：%#v", item)
		}
		phases[item.Phase] = item
	}
	for _, phase := range []string{"running", "completed"} {
		if _, ok := phases[phase]; !ok {
			t.Fatalf("缺少 %s 阶段的记录：%#v", phase, saved)
		}
	}
	if progress := phases["running"]; progress.Total != 2 || progress.Completed != 1 {
		t.Fatalf("进度没有落库：%#v", progress)
	}
	if finished := phases["completed"]; finished.FinishedAt == nil {
		t.Fatalf("完成时间没有落库：%#v", finished)
	}
}

// 失败也要留痕，否则事件页上「触发了任务但什么都没发生」无从解释。
func TestPluginTaskPersistsFailure(t *testing.T) {
	store := &recordingSubtaskStore{}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)

	event := MessageEvent{Kind: EventKindPrivate, UserID: "10001", MessageID: "m-2"}
	task := PluginTask{
		Kind: "ocr", Name: "文档识别",
		Run: func(context.Context, PluginTaskServices) (PluginTaskResult, error) {
			return PluginTaskResult{}, errors.New("渲染失败")
		},
	}
	reservation := runtime.reservePluginTasksForTurn(withOutboundTurn(context.Background(), "evt-7"), event, []PluginTask{task})
	if !reservation.handled {
		t.Fatal("任务没有被受理")
	}
	runtime.startPluginTaskReservation(reservation)

	waitForCondition(t, 5*time.Second, func() bool {
		for _, item := range store.snapshot() {
			if item.Phase == "failed" {
				return true
			}
		}
		return false
	})
	for _, item := range store.snapshot() {
		if item.Phase == "failed" {
			if item.Error == "" || item.FinishedAt == nil {
				t.Fatalf("失败记录不完整：%#v", item)
			}
			return
		}
	}
}

// 没有入站事件的任务（比如运维手动触发）不落库，也不能因此崩掉。
func TestPluginTaskWithoutInboundEventIsNotPersisted(t *testing.T) {
	store := &recordingSubtaskStore{}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)

	done := make(chan struct{})
	reservation := runtime.reservePluginTasks(MessageEvent{Kind: EventKindPrivate, UserID: "1"}, []PluginTask{{
		Kind: "image", Name: "图片生成",
		Run: func(context.Context, PluginTaskServices) (PluginTaskResult, error) {
			close(done)
			return PluginTaskResult{Reply: "好了"}, nil
		},
	}})
	if !reservation.handled {
		t.Fatal("任务没有被受理")
	}
	runtime.startPluginTaskReservation(reservation)
	<-done
	time.Sleep(200 * time.Millisecond)
	if saved := store.snapshot(); len(saved) != 0 {
		t.Fatalf("没有入站事件却落库了：%#v", saved)
	}
}
