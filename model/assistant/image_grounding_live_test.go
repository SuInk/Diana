// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func TestLiveTerraReceivesCurrentImage(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("DIANA_LIVE_SUB2API_KEY"))
	imagePath := strings.TrimSpace(os.Getenv("DIANA_LIVE_IMAGE_PATH"))
	if apiKey == "" || imagePath == "" {
		t.Skip("DIANA_LIVE_SUB2API_KEY and DIANA_LIVE_IMAGE_PATH are required")
	}
	event := MessageEvent{
		Kind: EventKindGroup, GroupID: "live-image-grounding", UserID: "tester", MessageID: "image-1",
		Segments: []MessageSegment{
			{Type: "image", Data: map[string]string{"cached_file": imagePath}},
			{Type: "text", Data: map[string]string{"text": "请识别图片中的待查证说法"}},
		},
	}
	message, failures := llmMessageFromEventWithImagesForContextDiagnostics(context.Background(), event, "请识别图片中的待查证说法", nil)
	if len(failures) > 0 {
		t.Fatalf("prepare current image: %v", failures)
	}
	imageParts := 0
	for _, part := range message.Parts {
		if part.Type == llm.ContentPartImageURL {
			imageParts++
			if !strings.HasPrefix(part.ImageURL, "data:image/") {
				t.Fatalf("current image was not converted to an inline data URL")
			}
		}
	}
	if imageParts == 0 {
		t.Fatal("current image did not reach the LLM message")
	}
	terra, err := llm.NewClient(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible, APIKey: apiKey,
		BaseURL: "https://sub2api.earlyso.com/v1", APIStyle: llm.APIStyleResponses,
		Model: "gpt-5.6-terra", UserAgent: "diana-live-image-grounding-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	rawTopic, rawSummary := liveIdentifyImageClaim(t, terra, message)
	t.Logf("Terra raw-image result: topic=%q summary=%q", rawTopic, rawSummary)
	rawCombined := rawTopic + "\n" + rawSummary
	if !strings.Contains(rawCombined, "摩门教") || strings.Contains(rawCombined, "公交") || strings.Contains(rawCombined, "电梯") || strings.Contains(rawCombined, "老人") {
		t.Fatalf("Terra replaced the isolated image topic: %s", rawCombined)
	}

	store := newRecallImageTestStore()
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return terra, nil })
	runtime.SetMessageHistoryStore(store)
	enriched, description := runtime.ensureReplyImageDescription(context.Background(), event)
	t.Logf("foreground description: %q", description)
	if !strings.Contains(description, "摩门教") || !strings.Contains(description, "婚前") || !strings.Contains(description, "插入") || !strings.Contains(description, "推") {
		t.Fatalf("foreground description did not recover visible text: %s", description)
	}
	grounded, failures := llmMessageFromEventWithImagesForContextDiagnostics(context.Background(), enriched, "请识别图片中的待查证说法", nil)
	if len(failures) > 0 {
		t.Fatalf("prepare grounded current image: %v", failures)
	}
	grounded = appendLLMMessageText(grounded, "【当前图片的独立视觉描述，可能有识别误差；请与原图共同核对主题，搜索词必须来自这张图，不得改换成无关话题】\n"+description)
	groundedTopic, groundedSummary := liveIdentifyImageClaim(t, terra, grounded)
	t.Logf("Terra grounded result: topic=%q summary=%q", groundedTopic, groundedSummary)
	groundedCombined := groundedTopic + "\n" + groundedSummary
	if !strings.Contains(groundedCombined, "摩门教") || !strings.Contains(groundedCombined, "婚前") || (!strings.Contains(groundedCombined, "插入") && !strings.Contains(groundedCombined, "推动")) || strings.Contains(groundedCombined, "公交") || strings.Contains(groundedCombined, "电梯") || strings.Contains(groundedCombined, "吸烟") || strings.Contains(groundedCombined, "烟蒂") {
		t.Fatalf("grounded Terra result remained off-topic: %s", groundedCombined)
	}

	luna, err := llm.NewClient(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible, APIKey: apiKey,
		BaseURL: "https://sub2api.earlyso.com/v1", APIStyle: llm.APIStyleResponses,
		Model: "gpt-5.6-luna", UserAgent: "diana-live-image-audit-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	auditRuntime := NewRuntime(BotConfig{ProactiveReplyThreshold: 0.9}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return luna, nil })
	enriched.replyAuditImageContext = description
	decision, err := auditRuntime.runReplyAudit(context.Background(), enriched, "嘉然帮我查证一下这个说法", "杭州公交电梯里的老人主要因为省钱和锻炼不坐电梯", auditRuntime.ProfileConfig(""), botReplyLoopEvidence{}, replyAuditNeed{Quality: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Luna audit: send_confidence=%.2f reason=%q", decision.Confidence, decision.Reason)
	if auditRuntime.proactiveQualityError(enriched, decision, auditRuntime.ProfileConfig("")) == nil {
		t.Fatal("Luna audit allowed an elevator answer for the Mormon screenshot")
	}
}

func liveIdentifyImageClaim(t *testing.T, client llm.LLMClient, message llm.Message) (string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	response, err := client.Generate(ctx, llm.GenerateRequest{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "只根据当前图片识别待查证说法，不要联想其他新闻。必须调用 identify_claim。"},
			message,
		},
		Tools: []llm.ToolDefinition{{
			Name: "identify_claim", Description: "记录图片中实际出现的待查证主题与说法",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"topic":   map[string]any{"type": "string"},
					"summary": map[string]any{"type": "string"},
				},
				"required": []string{"topic", "summary"}, "additionalProperties": false,
			}, Strict: true,
		}},
		ToolChoice: "identify_claim", MaxOutputTokens: 256,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v", response.ToolCalls)
	}
	topic := strings.TrimSpace(configToolString(response.ToolCalls[0].Arguments, "topic"))
	summary := strings.TrimSpace(configToolString(response.ToolCalls[0].Arguments, "summary"))
	return topic, summary
}

// 回放真实的先文字后图片事件，只调用模型，不向生产群发送消息。
func TestLiveFollowUpImageGrounding(t *testing.T) {
	path := os.Getenv("DIANA_LIVE_FOLLOWUP_SAMPLE")
	if path == "" {
		t.Skip("DIANA_LIVE_FOLLOWUP_SAMPLE is required")
	}
	client := liveLLMClient(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var events []MessageEvent
	if err := json.Unmarshal(data, &events); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatal("expected text and image events")
	}
	for _, otherSender := range []bool{false, true} {
		t.Run(fmt.Sprintf("other_sender=%v", otherSender), func(t *testing.T) {
			current := events[0]
			if otherSender {
				current.UserID = "replay-user"
				current.SenderName = "回放提问者"
			}
			runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return client, nil })
			runtime.SetMessageHistoryStore(newRecallImageTestStore())
			images := runtime.replyDependencyImages([]MessageEvent{current, events[1]}, current, nil, current.SelfID)
			if len(images) != 1 {
				t.Fatalf("follow-up images=%d", len(images))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()
			runtime.beginHistoryImageDescriptionForeground()
			defer runtime.endHistoryImageDescriptionForeground()
			message := runtime.senderDependencyMessage(ctx, current, &senderDependencyContext{images: images})
			if strings.Contains(message.Content, "【还没读到的图】") {
				t.Fatal("image description unavailable")
			}
			response, err := client.Generate(ctx, llm.GenerateRequest{Messages: []llm.Message{
				{Role: llm.RoleSystem, Content: "根据提供的图片描述，简短回答用户。不要假装认识图中具体人物。"},
				message, {Role: llm.RoleUser, Content: historyPlainText(current)},
			}})
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(response.Text) == "" || strings.Contains(response.Text, "没看到图") || strings.Contains(response.Text, "没收到图") {
				t.Fatalf("ungrounded reply: %s", response.Text)
			}
			t.Logf("model=%s description=%s reply=%s", response.Model, message.Content, response.Text)

		})
	}
}

// 仅回放原图选择，不生成图片、不向群里发送消息。
func TestLiveEditSourceSelection(t *testing.T) {
	path := os.Getenv("DIANA_LIVE_EDIT_SAMPLE")
	if path == "" {
		t.Skip("DIANA_LIVE_EDIT_SAMPLE is required")
	}
	client := liveLLMClient(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var sample struct {
		Current  MessageEvent
		History  []MessageEvent
		Expected string
	}
	if err := json.Unmarshal(data, &sample); err != nil {
		t.Fatal(err)
	}
	// 生产请求本身有引用；第二种扰动去掉引用、插入较近的无关图片，用真实图片摘要中的特征指认旧图。
	for _, quoted := range []bool{true, false} {
		t.Run(fmt.Sprintf("quoted=%v", quoted), func(t *testing.T) {
			current := sample.Current
			text := historyPlainText(current)
			history := append([]MessageEvent(nil), sample.History...)
			if !quoted && len(history) > 1 {
				history[len(history)-1].Time = current.Time - 1
			}
			runtime := NewRuntime(BotConfig{BotAccount: current.SelfID}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return client, nil })
			for _, event := range history {
				runtime.remember(event)
			}
			if !quoted {
				current.Quoted = nil
				current.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": "把临雀发的那张被窝里的女孩图改成黑白，保留原来的字。不是最近那张。"}}}
				text = historyPlainText(current)
			}
			decision, ok := runtime.classifyVisualIntent(context.Background(), current, text)
			if !ok || decision.Action != visualIntentEditImage || (!slices.Contains(decision.SourceMessageIDs, sample.Expected) || (!quoted && len(decision.SourceMessageIDs) != 1)) {
				t.Fatalf("incorrect source: %+v", decision)
			}
			t.Logf("selected=%s action=%s", sample.Expected, decision.Action)
		})
	}
}
