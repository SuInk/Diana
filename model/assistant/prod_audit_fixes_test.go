// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// 组 1：并发轮次重复正文去重与中途发言后静默收尾状态验证。
func TestSiblingDuplicateSendDedupeAndInterimSilentFinish(t *testing.T) {
	var tracker sessionSendLog
	now := time.Now()
	turnStart := now.Add(-2 * time.Second)
	event := MessageEvent{Kind: EventKindGroup, Platform: PlatformOneBotV11, GroupID: "7001", UserID: "1001"}
	tracker.note(event, "turn-before", OutgoingMessage{Text: "本轮启动前已经结束的回复不应被当成并发兄弟"}, now.Add(-5*time.Second))
	tracker.note(event, "turn-a", OutgoingMessage{Text: "已经帮你撤回上一条消息啦"}, now)

	ctxB := withReplyTurnStart(withOutboundTurn(context.Background(), "turn-b"), turnStart)
	ctxA := withReplyTurnStart(withOutboundTurn(context.Background(), "turn-a"), turnStart)

	if tracker.repeatsRecentSibling(ctxB, event, "本轮启动前已经结束的回复不应被当成并发兄弟", now.Add(5*time.Second)) {
		t.Fatal("prior finished turn before turnStart must not be treated as concurrent sibling")
	}
	if !tracker.repeatsRecentSibling(ctxB, event, "已经帮你撤回上一条消息啦", now.Add(5*time.Second)) {
		t.Fatal("expected sibling turn duplicate text to be detected")
	}
	if tracker.repeatsRecentSibling(ctxA, event, "已经帮你撤回上一条消息啦", now.Add(5*time.Second)) {
		t.Fatal("same turn should not block itself as sibling")
	}

	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{OwnerID: "999", AgentEnabled: true}, channel, NewPluginManager(), nil, nil, nil, nil)
	runtime.sessionSends.note(event, "turn-sibling", OutgoingMessage{Text: "这条内容兄弟轮次刚发过一遍"}, now)

	replyCtx := withReplyTurnStart(withOutboundTurn(withInterimMessages(context.Background()), "turn-current"), turnStart)
	tool := newDianaInterimMessageTool(runtime, event)
	out, err := tool.Run(replyCtx, map[string]any{"text": "这条内容兄弟轮次刚发过一遍"})
	if err != nil {
		t.Fatalf("interim dedupe returned error: %v", err)
	}
	if !strings.Contains(out, `"ok":true`) || len(channel.sentSnapshot()) != 0 {
		t.Fatalf("duplicate interim message should be skipped idempotently, sent=%#v out=%s", channel.sentSnapshot(), out)
	}
}

// 组 1：群聊多 Bot Profile 下 #diana 状态命令冷却与链接解析去重共享。
func TestGroupSharedDedupeAcrossMultipleProfiles(t *testing.T) {
	evA := MessageEvent{Kind: EventKindGroup, Platform: PlatformOneBotV11, ProfileID: "bot-1", SelfID: "10001", GroupID: "8888", UserID: "2001"}
	evB := MessageEvent{Kind: EventKindGroup, Platform: PlatformOneBotV11, ProfileID: "bot-2", SelfID: "10002", GroupID: "8888", UserID: "2001"}

	if statusCommandCooldownKey(evA) != statusCommandCooldownKey(evB) {
		t.Fatalf("group status cooldown keys differ across bots: %q vs %q", statusCommandCooldownKey(evA), statusCommandCooldownKey(evB))
	}

	runtime := NewRuntime(BotConfig{OwnerID: "999"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	if _, alreadyDelivered := runtime.reserveResolverDelivery(evA, []string{"bilibili:BV1xx411c7mD"}); alreadyDelivered {
		t.Fatal("first bot should reserve resolver key")
	}
	if _, alreadyDelivered := runtime.reserveResolverDelivery(evB, []string{"bilibili:BV1xx411c7mD"}); !alreadyDelivered {
		t.Fatal("second bot in same group should skip already-delivered resolver key")
	}
}

// 组 2：普通群成员撤回自己在当前群的消息（跳过当前撤回指令本身），且禁止撤回他人消息。
func TestMemberSelfRecallInGroupAndCommandExclusion(t *testing.T) {
	channel := &governanceStubChannel{
		botRole:    "admin",
		memberRole: map[string]string{"20001": "member", "20002": "member"},
	}
	runtime := NewRuntime(BotConfig{OwnerID: "99999", Platform: PlatformOneBotV11, BotAccount: "10000"}, channel, NewDefaultPluginManager(), nil, nil, nil, nil)

	msg1 := MessageEvent{Kind: EventKindGroup, Platform: PlatformOneBotV11, SelfID: "10000", GroupID: "5001", UserID: "20001", MessageID: "301", RawMessage: "发错的秘密口令", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "发错的秘密口令"}}}, Time: 100}
	msgOther := MessageEvent{Kind: EventKindGroup, Platform: PlatformOneBotV11, SelfID: "10000", GroupID: "5001", UserID: "20002", MessageID: "302", RawMessage: "别人的消息", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "别人的消息"}}}, Time: 101}
	cmdEvent := MessageEvent{Kind: EventKindGroup, Platform: PlatformOneBotV11, SelfID: "10000", GroupID: "5001", UserID: "20001", MessageID: "303", RawMessage: "帮我撤回上一条", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "帮我撤回上一条"}}}, Time: 102}
	runtime.remember(msg1)
	runtime.remember(msgOther)
	runtime.remember(cmdEvent)

	tool := newDianaPlatformTool(context.Background(), runtime, cmdEvent)
	if got := tool.CanonicalOperation(map[string]any{"operation": "recall_messages", "user_id": "20001", "count": 1}); got != "recall_self_messages" {
		t.Fatalf("CanonicalOperation for self recall = %q, want recall_self_messages", got)
	}
	if got := tool.CanonicalOperation(map[string]any{"operation": "recall_messages", "user_id": "20002", "count": 1}); got != "recall_messages" {
		t.Fatalf("CanonicalOperation for other user recall = %q, want recall_messages", got)
	}

	// 普通群成员不能撤回别人的消息
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "recall_messages", "message_id": "302"}); err == nil {
		t.Fatal("ordinary member was allowed to recall another member's message")
	}

	// 普通群成员按 user_id 撤回自己的最近 1 条消息：应跳过当前指令 303，精准撤回 301
	out, err := tool.Run(context.Background(), map[string]any{"operation": "recall_messages", "user_id": "20001", "count": 1})
	if err != nil {
		t.Fatalf("member self recall failed: %v", err)
	}
	if !strings.Contains(out, `"301"`) || strings.Contains(out, `"303"`) || !strings.Contains(out, "发错的秘密口令") {
		t.Fatalf("unexpected self recall output: %s", out)
	}
}

type governanceStubChannel struct {
	recordingChannel
	botRole    string
	memberRole map[string]string
}

func (c *governanceStubChannel) GroupMember(_ context.Context, groupID, userID string) (OneBotGroupMemberInfo, error) {
	role := c.memberRole[userID]
	if userID == "10000" {
		role = c.botRole
	}
	if role == "" {
		role = "member"
	}
	return OneBotGroupMemberInfo{GroupID: groupID, UserID: userID, Role: role, MembershipVerified: true}, nil
}

func (c *governanceStubChannel) GroupMembers(_ context.Context, groupID string) (GroupMemberDirectory, error) {
	return GroupMemberDirectory{
		Members: []OneBotGroupMemberInfo{
			{GroupID: groupID, UserID: "20001", Role: "member", MembershipVerified: true},
			{GroupID: groupID, UserID: "20002", Role: "member", MembershipVerified: true},
		},
		Complete: true,
		Source:   "test",
	}, nil
}

func (c *governanceStubChannel) CallAPI(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	_, _ = c.recordingChannel.CallAPI(ctx, action, params)
	switch action {
	case "get_group_member_info":
		uid := stringFromAny(params["user_id"])
		role := c.memberRole[uid]
		if uid == "10000" {
			role = c.botRole
		}
		if role == "" {
			role = "member"
		}
		return map[string]any{"data": map[string]any{"user_id": uid, "role": role}}, nil
	case "delete_msg":
		return map[string]any{"status": "ok"}, nil
	}
	return map[string]any{}, nil
}

// 组 3：无法还原的脱敏身份标识绝不静默回退到当前发言者（防跨会话和提醒发错人）。
func TestUnresolvableAliasNeverFallsBackToSpeaker(t *testing.T) {
	scope := newIdentityPrivacyScope()
	calls := scope.restoreToolCalls([]llm.ToolCall{
		{
			ID:   "cs1",
			Name: dianaCrossSessionToolName,
			Arguments: map[string]any{
				"user_id": "im_user_deadbeef1234",
				"message": "提醒你去开会",
			},
		},
		{
			ID:   "rem1",
			Name: "reminder",
			Arguments: map[string]any{
				"operation":      "create",
				"target_user_id": "im_user_deadbeef1234",
				"delay":          "10m",
				"message":        "喝水",
			},
		},
	})
	if got := calls[0].Arguments["user_id"]; got != unresolvableIdentityPlaceholder {
		t.Fatalf("cross_session_message user_id = %q, want %q", got, unresolvableIdentityPlaceholder)
	}
	if got := calls[1].Arguments["target_user_id"]; got != unresolvableIdentityPlaceholder {
		t.Fatalf("reminder target_user_id = %q, want %q", got, unresolvableIdentityPlaceholder)
	}

	channel := newFriendRosterChannel("999", "555")
	runtime := crossSessionTestRuntime(channel, BotConfig{OwnerID: "999"})
	ownerGroupEvent := crossSessionGroupSource("999")
	csTool := newDianaCrossSessionTool(runtime, ownerGroupEvent, true)
	if _, err := csTool.Run(context.Background(), calls[0].Arguments); err == nil {
		t.Fatal("cross_session_message silently fell back to owner private chat on unresolvable alias")
	}
	if len(channel.sentSnapshot()) != 0 {
		t.Fatalf("unexpected message sent: %#v", channel.sentSnapshot())
	}

	if _, err := taskTargetUserID(context.Background(), runtime, ownerGroupEvent, calls[1].Arguments); err == nil {
		t.Fatal("taskTargetUserID silently fell back to speaker on unresolvable alias")
	}
}

func TestNoticeTextExplicitMention(t *testing.T) {
	if !noticeTextHasExplicitMention("<diana-at id=\"20002\"/> 该开会啦") {
		t.Fatal("expected <diana-at> mention to be detected")
	}
	if noticeTextHasExplicitMention("普通订阅播报正文") {
		t.Fatal("plain text should not be treated as containing explicit mention")
	}
}

// 组 4：GitHub 工具对公共凭据暴露只读、chat_history 消息 ID 范围读取、语义索引离线指数退避。
func TestGitHubPublicCredentialExposureAndHistoryMessageRange(t *testing.T) {
	plugins := NewDefaultPluginManager()
	event := MessageEvent{Kind: EventKindGroup, Platform: PlatformOneBotV11, GroupID: "6001", UserID: "3001"}
	runtime := NewRuntime(BotConfig{OwnerID: "9999", AgentEnabled: true, Platform: PlatformOneBotV11}, nilChannel{}, plugins, nil, nil, nil, nil)

	_, defaultSettings, _ := runtime.pluginWithSettingsForEvent(repositoryPublishPluginID, event)
	if runtime.repositoryPublishToolExposedForEvent(event, defaultSettings) {
		t.Fatal("empty default settings should not expose github tool")
	}
	if _, err := plugins.UpdateSettings(repositoryPublishPluginID, map[string]any{
		repositoryPublishSettingAuthMode: repositoryPublishAuthGH,
	}); err != nil {
		t.Fatal(err)
	}
	_, ghSettings, _ := runtime.pluginWithSettingsForEvent(repositoryPublishPluginID, event)
	if !runtime.repositoryPublishToolExposedForEvent(event, ghSettings) {
		t.Fatal("auth_mode=gh should expose github tool for public repository reads")
	}

	// 测试 chat_history 按 from_message_id ~ through_message_id 精确切片
	for i, id := range []string{"101", "102", "103", "104"} {
		runtime.remember(MessageEvent{
			Kind:       EventKindGroup,
			Platform:   PlatformOneBotV11,
			GroupID:    "6001",
			UserID:     "3001",
			MessageID:  id,
			Time:       int64(1700000000 + i),
			RawMessage: "msg-" + id,
			Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "msg-" + id}}},
		})
	}
	histTool := newDianaChatHistoryTool(runtime, MessageEvent{Kind: EventKindGroup, Platform: PlatformOneBotV11, GroupID: "6001", UserID: "3001", Time: 1700000010})
	raw, err := histTool.Run(context.Background(), map[string]any{
		"from_message_id":    "102",
		"through_message_id": "103",
	})
	if err != nil {
		t.Fatalf("chat_history message range failed: %v", err)
	}
	var parsed dianaChatHistoryResult
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Items) != 2 || parsed.Items[0].MessageID != "102" || parsed.Items[1].MessageID != "103" {
		t.Fatalf("unexpected range items: %#v", parsed.Items)
	}
}

func TestSemanticIndexBreakerExponentialBackoff(t *testing.T) {
	var breaker semanticIndexBreaker
	now := time.Now()
	key := "openai\x00http://leisure:8000\x00bge-m3"

	breaker.record(key, false, now)
	if breaker.isCoolingDown(key, now) {
		t.Fatal("should not open after 1 failure")
	}
	breaker.record(key, false, now)
	if !breaker.isCoolingDown(key, now.Add(30*time.Second)) {
		t.Fatal("should cool down for 1m after 2nd failure")
	}
	if !breaker.allow(key, now.Add(61*time.Second)) {
		t.Fatal("should allow one probe after 1m cooldown")
	}
	breaker.record(key, false, now.Add(61*time.Second))
	if !breaker.isCoolingDown(key, now.Add(61*time.Second+90*time.Second)) {
		t.Fatal("3rd failure should back off to 2m")
	}
	breaker.record(key, true, now.Add(5*time.Minute))
	if breaker.isCoolingDown(key, now.Add(5*time.Minute)) {
		t.Fatal("success should reset index breaker")
	}
	_ = errors.New
}

// 组 5：dianaMemberCommandTool 的本群授权 inputs 文件传入与跨群隔离校验。
func TestMemberCommandAuthorizedInputsAndCrossGroupIsolation(t *testing.T) {
	stubMemberCommandAvailable(t, true)
	r, event := memberCommandTestRuntime(t, true)
	workRoot := AgentWorkspaceDir()
	relInput := filepath.ToSlash(filepath.Join("saved", "group-a", "report.csv"))
	absInput := filepath.Join(workRoot, filepath.FromSlash(relInput))
	if err := os.MkdirAll(filepath.Dir(absInput), 0o700); err != nil {
		t.Fatal(err)
	}
	payload := []byte("col1,col2\n1,2\n")
	if err := os.WriteFile(absInput, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(absInput) })
	if err := r.grantWorkspaceGroupFile(event, relInput, payload); err != nil {
		t.Fatal(err)
	}

	tool := newDianaMemberCommandTool(r, event)
	var captured agent.MemberCommandRequest
	tool.run = func(_ context.Context, req agent.MemberCommandRequest) (agent.MemberCommandResult, error) {
		captured = req
		return agent.MemberCommandResult{Command: req.Command, Sandbox: "fake"}, nil
	}
	_, err := tool.Run(context.Background(), map[string]any{
		"command":     "python3",
		"args":        []any{"analyze.py"},
		"stdin":       "extra-line",
		"write_files": map[string]any{"analyze.py": "print('ok')"},
		"inputs":      []any{relInput},
	})
	if err != nil {
		t.Fatalf("authorized input failed: %v", err)
	}
	if string(captured.Inputs["report.csv"]) != string(payload) || string(captured.WriteFiles["analyze.py"]) != "print('ok')" || captured.Stdin != "extra-line" {
		t.Fatalf("captured request mismatch: %#v", captured)
	}

	// 另一个群无权通过 inputs 借读本群保存的文件
	otherGroup := event
	otherGroup.GroupID = "group-b"
	otherTool := newDianaMemberCommandTool(r, otherGroup)
	otherTool.run = tool.run
	if _, err := otherTool.Run(context.Background(), map[string]any{
		"command": "python3",
		"inputs":  []any{relInput},
	}); err == nil {
		t.Fatal("other group was allowed to stage group-a's file into member sandbox")
	}
}

func TestRenderMediaPDFSpecAndChatHistoryParallel(t *testing.T) {
	runtime := NewRuntime(BotConfig{OwnerID: "999", AgentEnabled: true, Platform: PlatformOneBotV11}, nilChannel{}, NewDefaultPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, Platform: PlatformOneBotV11, GroupID: "7001", UserID: "999"}
	tool := newDianaRenderMediaTool(runtime, event, SettingValues{}, RelationshipPolicy{Owner: true})

	defaultSpec, err := tool.parse(map[string]any{
		"format":  "html",
		"output":  "pdf",
		"content": "<!DOCTYPE html><html><body><h1>Paper</h1></body></html>",
	})
	if err != nil {
		t.Fatalf("parse pdf default spec failed: %v", err)
	}
	if defaultSpec.output != renderMediaOutputPDF || defaultSpec.filename != "document.pdf" {
		t.Fatalf("unexpected default pdf spec: %#v", defaultSpec)
	}

	namedSpec, err := tool.parse(map[string]any{
		"format":   "svg",
		"output":   "pdf",
		"filename": "arxiv_paper",
		"content":  `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100"><circle cx="50" cy="50" r="40"/></svg>`,
	})
	if err != nil {
		t.Fatalf("parse named svg->pdf spec failed: %v", err)
	}
	if namedSpec.filename != "arxiv_paper.pdf" {
		t.Fatalf("expected arxiv_paper.pdf, got %q", namedSpec.filename)
	}

	if _, err := tool.parse(map[string]any{
		"format":   "html",
		"output":   "pdf",
		"filename": "../escape.pdf",
		"content":  "<html><body>bad</body></html>",
	}); err == nil {
		t.Fatal("expected path traversal filename to be rejected")
	}

	histTool := newDianaChatHistoryTool(runtime, event)
	var parallelCheck any = histTool
	p, ok := parallelCheck.(agent.CodeModeParallelTool)
	if !ok || !p.CodeModeParallel() {
		t.Fatal("expected dianaChatHistoryTool to implement agent.CodeModeParallelTool")
	}

	// 不支持文件附件的平台应提前友好降级提示改用 output=image，而不白跑浏览器
	unsupportedRuntime := NewRuntime(BotConfig{OwnerID: "999", AgentEnabled: true, Platform: "discord"}, nilChannel{}, NewDefaultPluginManager(), nil, nil, nil, nil)
	unsupportedEvent := MessageEvent{Kind: EventKindGroup, Platform: "discord", GroupID: "7001", UserID: "999"}
	unsupportedTool := newDianaRenderMediaTool(unsupportedRuntime, unsupportedEvent, SettingValues{}, RelationshipPolicy{Owner: true})
	rawOut, err := unsupportedTool.Run(context.Background(), map[string]any{
		"format":  "html",
		"output":  "pdf",
		"content": "<!DOCTYPE html><html><body><h1>Paper</h1></body></html>",
	})
	if err != nil || !strings.Contains(rawOut, "output=image") {
		t.Fatalf("expected graceful fallback hint on unsupported attachment platform, got out=%s err=%v", rawOut, err)
	}
}
