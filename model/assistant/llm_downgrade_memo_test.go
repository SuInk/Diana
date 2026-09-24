// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

type downgradeStoreStub struct {
	loaded []llm.DowngradeRecord
	saved  [][]llm.DowngradeRecord
}

func (s *downgradeStoreStub) LoadLLMParamDowngrades(context.Context) ([]llm.DowngradeRecord, error) {
	return s.loaded, nil
}

func (s *downgradeStoreStub) SaveLLMParamDowngrades(_ context.Context, records []llm.DowngradeRecord) error {
	s.saved = append(s.saved, records)
	return nil
}

func hasDowngrade(key string) bool {
	for _, record := range llm.DowngradeRecords() {
		if record.Key == key && record.Field == "tool_choice" {
			return true
		}
	}
	return false
}

// 启动时装回上次落盘的结论；没有新结论时退出也不白写一次。
func TestDowngradeMemoLoopRestoresOnStart(t *testing.T) {
	key := "openai_compatible|https://restore.invalid|thinking"
	store := &downgradeStoreStub{loaded: []llm.DowngradeRecord{{Key: key, Field: "tool_choice", LearnedAt: time.Now()}}}
	r := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.SetLLMDowngradeStore(store)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.runLLMDowngradeMemoLoop(ctx)
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !hasDowngrade(key) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if !hasDowngrade(key) {
		t.Fatal("the persisted conclusion was not restored")
	}
	if len(store.saved) != 0 {
		t.Fatalf("nothing new was learned, yet it wrote %d times", len(store.saved))
	}
}

// 真实请求新学到的结论要落盘——以前落盘挂在默认关闭的后台探测后面，默认配置下
// 从来没存过。结论没变时不重复写。
func TestDowngradeMemoPersistsOnlyChanges(t *testing.T) {
	store := &downgradeStoreStub{}
	r := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	last := llm.DowngradeRecords()

	key := "openai_compatible|https://learned.invalid|thinking"
	llm.RestoreDowngradeRecords([]llm.DowngradeRecord{{Key: key, Field: "tool_choice", LearnedAt: time.Now()}})
	r.persistLLMDowngrades(context.Background(), store, &last)
	if len(store.saved) != 1 {
		t.Fatalf("saves = %d, want 1", len(store.saved))
	}
	found := false
	for _, record := range store.saved[0] {
		if record.Key == key {
			found = true
		}
	}
	if !found {
		t.Fatalf("the new conclusion was not written: %#v", store.saved[0])
	}

	r.persistLLMDowngrades(context.Background(), store, &last)
	if len(store.saved) != 1 {
		t.Fatalf("an unchanged memo was written again: saves = %d", len(store.saved))
	}
}
