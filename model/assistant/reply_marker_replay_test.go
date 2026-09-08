package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

type replyMarkerReplayProvider struct {
	request llm.GenerateRequest
	content string
}

func (p *replyMarkerReplayProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.request = req
	return &llm.GenerateResponse{ToolCalls: []llm.ToolCall{{
		ID: "call_45955", Name: "agent.finalize",
		Arguments: map[string]any{"content": p.content},
	}}}, nil
}

// Regression replay of the recorded final response; the identity scope is local.
// No real model or Telegram service is contacted.
func TestReplayTelegramReplyMarkerIncident(t *testing.T) {
	withFastSendTiming(t)
	const badAlias = "im_message_702fc1baad94"
	const body = "收到你的问题[diana-line]这是补充说明"
	for _, tc := range []struct {
		name      string
		mode      ReplyDecorationMode
		known     bool
		wantReply string
	}{
		{"unknown_alias_on", ReplyDecorationOn, false, "106020"},
		{"unknown_alias_auto", ReplyDecorationAuto, false, ""},
		{"unknown_alias_off", ReplyDecorationOff, false, ""},
		{"known_alias_auto", ReplyDecorationAuto, true, "106020"},
		{"known_alias_on", ReplyDecorationOn, true, "106020"},
		{"known_alias_off", ReplyDecorationOff, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newFakeTelegramAPI(t, nil)
			runtime := NewRuntime(BotConfig{
				Platform:                  PlatformTelegram,
				ReplyReferenceMode:        tc.mode,
				NaturalReplySplitEnabled:  boolPointer(false),
				LLMIdentityMaskingEnabled: boolPointer(true),
			}, api.channel(), NewPluginManager(), nil, nil, nil, nil)
			event := MessageEvent{
				Platform: PlatformTelegram, Kind: EventKindGroup,
				GroupID: "-100123", UserID: "10001", SelfID: "4242", MessageID: "106020",
			}
			ctx := runtime.withIdentityPrivacyContext(context.Background(), event, nil)
			scope := identityPrivacyScopeFromContext(ctx)
			alias := badAlias
			if tc.known {
				alias = scope.registerMessageID(event.MessageID)
			}
			provider := &replyMarkerReplayProvider{content: replyMarkerPrefix + alias + "]" + body}
			client := &identityPrivacyProvider{provider: provider, scope: scope}
			response, err := client.Generate(ctx, llm.GenerateRequest{Messages: []llm.Message{{
				Role: llm.RoleUser, Content: `{"message_id":"106020","text":"你觉得呢"}`,
			}}})
			if err != nil {
				t.Fatal(err)
			}
			requestJSON, err := json.Marshal(provider.request)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(requestJSON), event.MessageID) {
				t.Fatal("real message ID leaked into model request")
			}
			if present := strings.Contains(string(requestJSON), alias); present != tc.known {
				t.Fatalf("alias present in model request=%v, want %v", present, tc.known)
			}
			restored := response.ToolCalls[0].Arguments["content"].(string)
			parsedID, _, parsed := extractOutgoingReplyMarker(restored)
			if parsed != tc.known {
				t.Fatalf("parsed=%v, want %v", parsed, tc.known)
			}
			if err := runtime.send(ctx, event, restored); err != nil {
				t.Fatal(err)
			}
			calls := api.callsOf("sendMessage")
			if len(calls) == 0 {
				t.Fatal("no Telegram sendMessage call")
			}
			params := calls[0].Params
			replyID, _ := params["reply_to_message_id"].(string)
			text, _ := params["text"].(string)
			leaked := strings.Contains(text, badAlias)
			if replyID != tc.wantReply || leaked {
				t.Fatalf("unexpected replay result: reply=%q leaked=%v text=%q", replyID, leaked, text)
			}
			if strings.Contains(text, replyMarkerPrefix) {
				t.Fatal("reply marker was not consumed")
			}
			t.Logf("known_alias=%v parsed=%v parsed_id=%q reply_to_message_id=%q leaked=%v text=%q",
				tc.known, parsed, parsedID, replyID, leaked, text)
		})
	}
}
