// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// 多台机器人时，主人改别人的好感度必须改到本机那一行。以前手拼的事件漏了
// ProfileID，写落进空归属，工具报「已更新为 50」，读回来还是 -23。
func TestRelationshipSetWritesToCurrentBotProfile(t *testing.T) {
	memory := newMemoryUserMemoryStore()
	memory.profiles["20002"] = UserMemoryProfile{BotProfileID: "bot-a", UserID: "20002", Favorability: -23, MessageCount: 40}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetProfiles(ProfileSet{Profiles: []BotConfig{{ID: "bot-a", OwnerID: "20001"}, {ID: "bot-b", OwnerID: "20009"}}})
	runtime.SetUserMemoryStore(memory)
	tool := newDianaRelationshipTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "30001", UserID: "20001", ProfileID: "bot-a"})

	raw, err := tool.Run(context.Background(), map[string]any{"operation": "set", "target_user_id": "20002", "value": 50})
	if err != nil {
		t.Fatal(err)
	}
	var result dianaRelationshipResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.Target == nil || result.Target.Favorability != 50 {
		t.Fatalf("snapshot after set = %#v", result.Target)
	}
	if got := memory.profiles["20002"]; got.Favorability != 50 || got.BotProfileID != "bot-a" {
		t.Fatalf("stored profile = %#v", got)
	}
}

func TestSaveUserMemoryRefusesUnscopedWriteWithSeveralBots(t *testing.T) {
	memory := newMemoryUserMemoryStore()
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetProfiles(ProfileSet{Profiles: []BotConfig{{ID: "bot-a"}, {ID: "bot-b"}}})
	if _, err := runtime.saveUserMemory(context.Background(), memory, MessageEvent{UserID: "20002"}, UserMemoryUpdate{Administrative: true}); err == nil {
		t.Fatal("unscoped write with two bots should be refused")
	}
	if _, ok := memory.profiles["20002"]; ok {
		t.Fatal("refused write still reached the store")
	}

	single := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	single.SetProfiles(ProfileSet{Profiles: []BotConfig{{ID: "solo"}}})
	profile, err := single.saveUserMemory(context.Background(), memory, MessageEvent{UserID: "20003"}, UserMemoryUpdate{Administrative: true})
	if err != nil || profile.BotProfileID != "solo" {
		t.Fatalf("single bot write = %#v, %v; want scope filled with the sole bot", profile, err)
	}
}

// 主人替别人建 RSS 订阅，额度按对方算，和周期查询、一次性提醒一个口径。
func TestRSSWatchCreateForOtherUserUsesTargetQuota(t *testing.T) {
	memory := newMemoryUserMemoryStore()
	memory.profiles["20002"] = UserMemoryProfile{UserID: "20002", Favorability: -5}
	store := &stubReminderStore{items: []Reminder{
		{ID: "daily", Kind: ReminderKindQuery, OwnerID: "20002", UserID: "20002", IntervalSeconds: 86400},
	}}
	runtime := NewRuntime(BotConfig{OwnerID: "20001"}, nilChannel{}, NewPluginManager(), nil, store, nil, nil)
	runtime.SetUserMemoryStore(memory)
	tool := newDianaRSSWatchTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "30001", UserID: "20001"})

	_, err := tool.Run(context.Background(), map[string]any{"operation": "create", "target_user_id": "20002", "feed_url": "https://example.com/feed.xml", "judge_prompt": "有新文章就说"})
	if err == nil || !strings.Contains(err.Error(), "最多可创建 1 个") {
		t.Fatalf("create for cold target err = %v, want target's quota of 1", err)
	}
}

// 名额跟好感度一样按机器人分：在 A 上建的任务不占 B 的名额，没归属的老任务两边都算。
func TestRecurringQuotaIsCountedPerBot(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetProfiles(ProfileSet{Profiles: []BotConfig{{ID: "bot-a"}, {ID: "bot-b"}}})
	items := []Reminder{
		{ID: "a1", Kind: ReminderKindQuery, ProfileID: "bot-a", OwnerID: "20002", IntervalSeconds: 3600},
		{ID: "a2", Kind: ReminderKindRSSWatch, ProfileID: "bot-a", OwnerID: "20002", FeedURL: "https://example.com/feed.xml", IntervalSeconds: 3600},
		{ID: "legacy", Kind: ReminderKindQuery, OwnerID: "20002", IntervalSeconds: 3600},
		{ID: "someone-else", Kind: ReminderKindQuery, ProfileID: "bot-b", OwnerID: "20003", IntervalSeconds: 3600},
	}
	if got := runtime.activeRecurringTaskCount(items, MessageEvent{UserID: "20002", ProfileID: "bot-a"}); got != 3 {
		t.Fatalf("bot-a count = %d, want 3", got)
	}
	if got := runtime.activeRecurringTaskCount(items, MessageEvent{UserID: "20002", ProfileID: "bot-b"}); got != 1 {
		t.Fatalf("bot-b count = %d, want 1 (only the unscoped legacy task)", got)
	}
}

func TestQuotedSourceEventKeepsBotIdentity(t *testing.T) {
	event := MessageEvent{
		Platform: "onebot-v11", ProfileID: "bot-a", ContextNamespace: "bot-a", SelfID: "40001",
		Kind: EventKindGroup, GroupID: "30001", UserID: "20001",
		Quoted: &QuotedMessage{MessageID: "m1", UserID: "20002"},
	}
	quoted := quotedSourceEvent(event)
	if quoted.ProfileID != "bot-a" || quoted.Platform != "onebot-v11" || quoted.ContextNamespace != "bot-a" || quoted.SelfID != "40001" {
		t.Fatalf("quoted event lost identity: %#v", quoted)
	}
	if sessionKey(quoted) != sessionKey(event) {
		t.Fatalf("quoted session key = %q, want %q", sessionKey(quoted), sessionKey(event))
	}
}

// 人员档案的写入只能走 saveUserMemory：那里统一补归属、拒绝说不清归属的写。
func TestUserMemoryWritesGoThroughSaveUserMemory(t *testing.T) {
	for _, call := range productionCalls(t, "UpdateUserMemory") {
		if call.function != "saveUserMemory" {
			t.Errorf("%s: %s calls UpdateUserMemory directly; use Runtime.saveUserMemory", call.position, call.function)
		}
	}
}

// 按机器人分归属的数据（档案、额度、配置、会话键）都认 MessageEvent 上的
// ProfileID。在运行时里凭空拼一个事件而不带它，几台机器人时就会读写到默认那台
// 或空归属那一行——这类 bug 已经反复出现。新拼事件要么从现有事件复制再改字段，
// 要么显式带上 ProfileID。
//
// 豁免的只有两类：平台适配器（事件交给 MultiChannel 包过的 handler，那里统一
// 补身份）和只拿来解析、不进任何存储的临时事件。
func TestMessageEventLiteralsCarryProfileID(t *testing.T) {
	exemptFiles := map[string]string{
		"bot_mute.go":               "适配器事件，经 MultiChannel 补身份",
		"dingtalk.go":               "平台适配器",
		"feishu.go":                 "平台适配器",
		"imessage.go":               "平台适配器",
		"onebot.go":                 "平台适配器",
		"onebot_request.go":         "平台适配器",
		"qq_official.go":            "平台适配器",
		"telegram.go":               "平台适配器",
		"wecom.go":                  "平台适配器",
		"weixin.go":                 "平台适配器",
		"history_identity.go":       "只用来算角色标签，配置显式传入",
		"repository_publish_web.go": "只做 GitHub 写权限检查，不碰按机器人分的存储",
	}
	parseOnlyKeys := map[string]bool{"Segments": true, "RawMessage": true}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") || exemptFiles[file] != "" {
			continue
		}
		node, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(node, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || len(lit.Elts) == 0 {
				return true
			}
			if ident, ok := lit.Type.(*ast.Ident); !ok || ident.Name != "MessageEvent" {
				return true
			}
			hasProfile, parseOnly := false, true
			for _, element := range lit.Elts {
				kv, ok := element.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, _ := kv.Key.(*ast.Ident)
				if key == nil {
					continue
				}
				hasProfile = hasProfile || key.Name == "ProfileID"
				parseOnly = parseOnly && parseOnlyKeys[key.Name]
			}
			if !hasProfile && !parseOnly {
				t.Errorf("%s: MessageEvent literal without ProfileID; copy an existing event or set ProfileID explicitly", fset.Position(lit.Pos()))
			}
			return true
		})
	}
}

type productionCall struct {
	position string
	function string
}

// productionCalls 列出本包非测试代码里所有 x.<method>(...) 调用及其所在函数。
func productionCalls(t *testing.T, method string) []productionCall {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var calls []productionCall
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		node, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range node.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if selector, ok := call.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == method {
					calls = append(calls, productionCall{position: fset.Position(call.Pos()).String(), function: fn.Name.Name})
				}
				return true
			})
		}
	}
	return calls
}
