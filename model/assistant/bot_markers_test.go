package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

type testBotMarkersSaver struct {
	ids  map[string][]string
	fail bool
}

func (s *testBotMarkersSaver) SaveBotConfig(BotConfig) {}
func (s *testBotMarkersSaver) SaveMarkedBotIDs(id string, ids []string) error {
	if s.fail {
		return fmt.Errorf("disk failure")
	}
	if s.ids == nil {
		s.ids = map[string][]string{}
	}
	s.ids[id] = append([]string(nil), ids...)
	return nil
}

func TestBotMarkersOwnerScopesAndSuppression(t *testing.T) {
	ctx := context.Background()
	saver := &testBotMarkersSaver{}
	r := NewRuntime(BotConfig{ID: "a", OwnerID: "900", BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, saver, nil)
	r.SetProfiles(ProfileSet{ActiveID: "a", Profiles: []BotConfig{{ID: "a", OwnerID: "900", BotAccount: "42"}, {ID: "b", OwnerID: "901", BotAccount: "43"}}})
	r.SetGroupConfigStore(&testWritableGroupConfigStore{})
	e := MessageEvent{Kind: EventKindGroup, ProfileID: "a", GroupID: "100", UserID: "900", Quoted: &QuotedMessage{UserID: "200"}}
	tool := &dianaBotMarkersTool{runtime: r, event: e}
	if _, err := tool.Run(ctx, map[string]any{"operation": "mark", "scope": "bot"}); err != nil {
		t.Fatal(err)
	}
	for _, platform := range []string{PlatformTelegram, PlatformOneBotV11} {
		for _, tc := range []struct {
			profile, group string
			want           bool
		}{{"a", "100", true}, {"a", "101", true}, {"b", "100", false}} {
			event := MessageEvent{Kind: EventKindGroup, Platform: platform, ProfileID: tc.profile, GroupID: tc.group, UserID: "200"}
			if got := r.requiresTelegramBotMentionJudgment(event); got != tc.want {
				t.Fatalf("%+v got %t", event, got)
			}
		}
	}
	if _, err := tool.Run(ctx, map[string]any{"operation": "mark", "scope": "group", "user_id": "201"}); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Run(ctx, map[string]any{"operation": "unmark", "scope": "group", "user_id": "200"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(r.effectiveConfigForEvent(e).MarkedBotIDs, "200") {
		t.Fatal("group removal erased bot-wide mark")
	}
	other := e
	other.GroupID = "101"
	if slices.Contains(r.effectiveConfigForEvent(other).MarkedBotIDs, "201") {
		t.Fatal("group marker leaked")
	}
	tool.event.UserID = "admin"
	tool.event.SenderRole = "admin"
	if _, err := tool.Run(ctx, map[string]any{"operation": "mark", "scope": "bot", "user_id": "202"}); err == nil {
		t.Fatal("admin changed owner-only markers")
	}
	tool.event = e
	tool.event.ProfileID = "b"
	if _, err := tool.Run(ctx, map[string]any{"operation": "mark", "scope": "bot", "user_id": "202"}); err == nil {
		t.Fatal("owner of a changed robot b")
	}
	tool.event = e
	saver.fail = true
	if _, err := tool.Run(ctx, map[string]any{"operation": "mark", "scope": "bot", "user_id": "202"}); err == nil {
		t.Fatal("persistence failure hidden")
	}
	if slices.Contains(r.Config().MarkedBotIDs, "202") {
		t.Fatal("failed write changed runtime")
	}
	copy := ConfigFromPayload(PayloadFromConfig(r.Config()), BotConfig{}).WithDefaults()
	copy.MarkedBotIDs[0] = "changed"
	if !slices.Contains(r.Config().MarkedBotIDs, "200") {
		t.Fatal("configuration slice alias")
	}
}

func TestManuallyMarkedBotSuppressionBeforeReply(t *testing.T) {
	for _, platform := range []string{PlatformOneBotV11, PlatformTelegram} {
		provider := &capturingLLMProvider{reply: `{"mentions_self":false}`}
		r := NewRuntime(BotConfig{ID: "a", BotAccount: "42", MarkedBotIDs: []string{"200"}, TelegramSuppressBotMessages: boolPointer(false)}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
		event := MessageEvent{Platform: platform, ProfileID: "a", Kind: EventKindGroup, GroupID: "100", UserID: "200", MessageID: "m", RawMessage: "自动推送一条新闻", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "自动推送一条新闻"}}}}
		_, _, handled, outcome := r.prepareMessageEvent(context.Background(), event)
		if handled || outcome != "ignored_bot_message" {
			t.Fatalf("%s: handled=%t outcome=%s", platform, handled, outcome)
		}
		if len(provider.requestSnapshot().Messages) == 0 {
			t.Fatal("missing semantic bot gate")
		}
	}
}

func TestLiveBotMarkerNaturalLanguage(t *testing.T) {
	client := liveLLMClient(t)
	for _, tc := range []struct{ name, input, scope, operation string }{{"qq_quote", "把我引用的这个账号标记为机器人，只在这个群生效。", "group", "mark"}, {"tg_bot", "把账号 200 标记为机器人，对这台机器人的所有群生效。", "bot", "mark"}, {"tg_unmark", "把账号 200 的机器人全局标记取消掉。", "bot", "unmark"}} {
		t.Run(tc.name, func(t *testing.T) {
			saver := &testBotMarkersSaver{}
			r := NewRuntime(BotConfig{ID: "a", OwnerID: "900", BotAccount: "42", MarkedBotIDs: []string{"200"}}, nilChannel{}, NewPluginManager(), nil, nil, saver, nil)
			r.SetGroupConfigStore(&testWritableGroupConfigStore{})
			event := MessageEvent{Kind: EventKindGroup, ProfileID: "a", GroupID: "100", UserID: "900", Quoted: &QuotedMessage{UserID: "200"}}
			tool := &dianaBotMarkersTool{runtime: r, event: event}
			req := llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleSystem, Content: "当前发言者是本机主人。当前群 ID=100，机器人 ID=a，被引用消息发送者 ID=200。根据用户明确请求使用工具，不猜测身份。"}, {Role: llm.RoleUser, Content: tc.input}}, Tools: []llm.ToolDefinition{{Name: tool.Name(), Description: tool.Description(), Parameters: tool.InputSchema()}}}
			encoded, _ := json.Marshal(req)
			t.Logf("RAW_INPUT=%s", encoded)
			probe := &liveTopicProbe{LLMProvider: client, t: t}
			resp, err := probe.Generate(context.Background(), req)
			if err != nil {
				t.Fatal("real model failed; see redacted log")
			}
			if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != tool.Name() {
				t.Fatalf("tool calls=%+v", resp.ToolCalls)
			}
			args := resp.ToolCalls[0].Arguments
			if args["scope"] != tc.scope || args["operation"] != tc.operation {
				t.Fatalf("arguments=%+v", args)
			}
			result, err := tool.Run(context.Background(), args)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("LOCAL_TOOL_RESULT=%s", result)
		})
	}
}
