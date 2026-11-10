// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// 同一个群里不管谁说话，历史之前那段 skill 目录和续接校验键里对应的那一段都得一样：
// 主人、管理员、普通成员交替说话不能让续接作废。目录列出全部 skill，不是人人能用的注明
// 对谁开放；停用的不出现；没权限的人 read_skill 被拒绝，有权限的照常读到。
func TestGroupSkillCatalogIsIdenticalForEverySpeaker(t *testing.T) {
	dbDir := t.TempDir()
	t.Setenv("APP_DB_PATH", filepath.Join(dbDir, "diana.db"))
	workDir := AgentWorkspaceDir()
	skillRoot := filepath.Join(dbDir, "skills")
	for _, name := range []string{"open-guide", "private-runbook", "retired-notes"} {
		dir := filepath.Join(skillRoot, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		body := "---\nname: " + name + "\ndescription: " + name + " 说明\n---\n" + name + " 正文"
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	overrides := workspaceStateTestPath(t, workDir, "extension-overrides.json")
	if err := os.WriteFile(overrides, []byte(`{"bot-a":{"members:skill:open-guide":true,"skill:retired-notes":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := standardModeBotConfig()
	cfg.AgentSkillRoots = []string{skillRoot}
	cfg.AgentMCPConfigPath = filepath.Join(dbDir, "missing-mcp.json")
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)

	type speaker struct {
		name         string
		userID       string
		relationship RelationshipPolicy
		canRead      bool
	}
	speakers := []speaker{
		{name: "主人", userID: "owner", relationship: RelationshipPolicy{Owner: true}, canRead: true},
		{name: "管理员", userID: "manager", relationship: RelationshipPolicy{Admin: true}, canRead: true},
		{name: "成员甲", userID: "member-a", relationship: RelationshipPolicy{Score: 60}},
		{name: "成员乙", userID: "member-b", relationship: RelationshipPolicy{Score: -20}},
	}
	var firstCatalog, firstKey, firstName string
	for _, who := range speakers {
		event := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: who.userID, ProfileID: "bot-a"}
		registry, err := runtime.newAgentRegistry(context.Background(), cfg.WithDefaults(), event, who.relationship)
		if err != nil {
			t.Fatalf("%s: %v", who.name, err)
		}
		provider := &agentSequenceLLMProvider{responses: []string{`{"action":"final","content":"好"}`}}
		runner, err := agent.NewRunner(provider, agent.Config{MaxSteps: 3}, registry)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := runner.Run(context.Background(), agent.Request{Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "人设"},
			{Role: llm.RoleUser, Content: "【当前需要回复的消息】在吗"},
		}})
		if err != nil {
			t.Fatalf("%s: %v", who.name, err)
		}
		catalog := ""
		for _, message := range provider.requests[0].Messages {
			if strings.Contains(message.Content, "### Available skills") {
				catalog = message.Content
			}
		}
		if catalog == "" {
			t.Fatalf("%s 的请求里没有 skill 目录", who.name)
		}
		if !strings.Contains(catalog, "- open-guide: open-guide 说明\n") {
			t.Fatalf("%s：对所有成员开放的 skill 不该带开放说明：%q", who.name, catalog)
		}
		if !strings.Contains(catalog, "- private-runbook: private-runbook 说明〔"+skillRestrictedAudience+"〕") {
			t.Fatalf("%s：受限 skill 应当列出并注明对谁开放：%q", who.name, catalog)
		}
		if strings.Contains(catalog, "retired-notes") {
			t.Fatalf("%s：停用的 skill 不该进目录：%q", who.name, catalog)
		}
		if strings.Contains(catalog, "private-runbook 正文") {
			t.Fatalf("%s：受限 skill 的正文不该进对谁都一样的那一段：%q", who.name, catalog)
		}
		// 校验键是「系统提示.稳定段.工具定义」三段指纹，这里只比 skill 目录所在的中间那段。
		parts := strings.Split(resp.ConversationKey, ".")
		if len(parts) != 3 {
			t.Fatalf("%s: conversation key=%q", who.name, resp.ConversationKey)
		}
		if firstName == "" {
			firstCatalog, firstKey, firstName = catalog, parts[1], who.name
		} else if catalog != firstCatalog || parts[1] != firstKey {
			t.Fatalf("%s 和 %s 看到的 skill 目录不一样：\n%q\n%q", who.name, firstName, catalog, firstCatalog)
		}

		read, ok := registry.Get("read_skill")
		if !ok {
			t.Fatalf("%s：read_skill 不见了", who.name)
		}
		body, err := read.Run(context.Background(), map[string]any{"name": "private-runbook"})
		switch {
		case who.canRead && (err != nil || !strings.Contains(body, "private-runbook 正文")):
			t.Fatalf("%s 应当读得到受限 skill：body=%q err=%v", who.name, body, err)
		case !who.canRead && (err == nil || !strings.Contains(err.Error(), skillRestrictedAudience) || !strings.Contains(err.Error(), "当前发言者不能用")):
			t.Fatalf("%s 读受限 skill 应当被拒绝并说明对谁开放：body=%q err=%v", who.name, body, err)
		}
		if body, err := read.Run(context.Background(), map[string]any{"name": "open-guide"}); err != nil || !strings.Contains(body, "open-guide 正文") {
			t.Fatalf("%s 应当读得到公共 skill：body=%q err=%v", who.name, body, err)
		}
		if _, err := read.Run(context.Background(), map[string]any{"name": "retired-notes"}); err == nil {
			t.Fatalf("%s 读到了停用的 skill", who.name)
		}
		_ = registry.Close()
	}
}
