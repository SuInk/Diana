// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
)

func residentBlock(snapshot ResidentContextSnapshot, key string) ResidentContextBlock {
	for _, block := range snapshot.Blocks {
		if block.Key == key {
			return block
		}
	}
	return ResidentContextBlock{}
}

// 快照要能回答「它每轮到底被灌了些什么」：人设正文、固定规则、世界书常驻设定和
// 自述都要给原文，token 用的是编排请求时同一个估算函数，数字对得上。
func TestResidentContextSnapshotCarriesEveryAlwaysOnBlock(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetProfiles(ProfileSet{Profiles: []BotConfig{{
		ID: "bot-a", Platform: PlatformOneBotV11, BotAccount: "42",
		SystemPrompt:    "你叫甲，说话简短。",
		SelfNoteEnabled: boolPointer(true),
	}}})
	runtime.SetWorldBookStore(stubWorldBookStore{ok: true, tree: WorldBook{Nodes: []WorldBookNode{
		{ID: "world", Title: "世界", Content: "故事发生在虚构城市枝江。", AlwaysOn: true},
		{ID: "street", Title: "长江路", Content: "长江路上有家糖水铺。", Keywords: []string{"长江路"}},
	}}.WithDefaults()})
	runtime.SetSelfNoteStore(&stubSelfNoteStore{notes: []SelfNote{{ID: "n1", Topic: "说话方式", Content: "一句说完就不铺三句"}}})

	snapshot := runtime.ResidentContextForGroup(context.Background(), "bot-a", "123456")
	if snapshot.GroupID != "123456" || snapshot.ContextWindow <= 0 {
		t.Fatalf("snapshot = %#v", snapshot)
	}

	persona := residentBlock(snapshot, ResidentBlockPersona)
	if persona.Content != "你叫甲，说话简短。" || persona.Tokens <= 0 {
		t.Fatalf("persona = %#v", persona)
	}
	rules := residentBlock(snapshot, ResidentBlockPromptRules)
	if rules.Content == "" || strings.Contains(rules.Content, "你叫甲") {
		// 规则那块是 head 去掉人设之后的部分：人设不能在两块里各出现一次，
		// 否则 total 会把它算两遍。
		t.Fatalf("rules = %q", rules.Content)
	}
	world := residentBlock(snapshot, ResidentBlockWorldBook)
	if !strings.Contains(world.Content, "枝江") {
		t.Fatalf("world book = %q", world.Content)
	}
	// 触发式设定不是常驻，不该出现在快照里，否则会让人以为每轮都在付这笔钱。
	if strings.Contains(world.Content, "糖水铺") {
		t.Fatalf("keyword-triggered node leaked into the resident snapshot: %q", world.Content)
	}
	notes := residentBlock(snapshot, ResidentBlockSelfNotes)
	if !strings.Contains(notes.Content, "一句说完就不铺三句") || notes.Budget <= 0 {
		t.Fatalf("self notes = %#v", notes)
	}

	var sum int64
	for _, block := range snapshot.Blocks {
		sum += block.Tokens
	}
	if snapshot.TotalTokens != sum {
		t.Fatalf("total = %d, want %d", snapshot.TotalTokens, sum)
	}
}

// 没开的功能给空块而不是消失：看的人要能分清「这层没内容」和「这层不存在」。
func TestResidentContextKeepsEmptyBlocksVisible(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetProfiles(ProfileSet{Profiles: []BotConfig{{
		ID: "bot-a", Platform: PlatformOneBotV11, BotAccount: "42", SystemPrompt: "你叫甲。",
	}}})

	snapshot := runtime.ResidentContextForGroup(context.Background(), "bot-a", "")
	keys := make(map[string]bool, len(snapshot.Blocks))
	for _, block := range snapshot.Blocks {
		keys[block.Key] = true
	}
	for _, key := range []string{ResidentBlockPersona, ResidentBlockPromptRules, ResidentBlockWorldBook, ResidentBlockSelfNotes, ResidentBlockSessionNote} {
		if !keys[key] {
			t.Fatalf("block %q missing: %#v", key, snapshot.Blocks)
		}
	}
	if notes := residentBlock(snapshot, ResidentBlockSelfNotes); notes.Content != "" || notes.Note == "" {
		t.Fatalf("self notes without a store = %#v", notes)
	}
	// 没选群时按私聊场景取，会话便签自然是空的。
	if note := residentBlock(snapshot, ResidentBlockSessionNote); note.Content != "" {
		t.Fatalf("session note without a group = %#v", note)
	}
}
