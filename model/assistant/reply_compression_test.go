package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

type compressionTestProvider struct {
	capturingLLMProvider
	outputs         []string
	requests        []llm.GenerateRequest
	purposes        []string
	observed        bool
	err             error
	generationCalls int
}

func (p *compressionTestProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	if llmUsagePurposeFromContext(ctx) != "reply_compression" {
		p.generationCalls++
		return p.capturingLLMProvider.Generate(ctx, req)
	}
	p.requests = append(p.requests, req)
	p.purposes = append(p.purposes, llmUsagePurposeFromContext(ctx))
	p.observed = p.observed || textDeltaObserverFromContext(ctx) != nil
	if p.err != nil {
		return nil, p.err
	}
	output := p.reply
	if len(p.outputs) > 0 {
		output, p.outputs = p.outputs[0], p.outputs[1:]
	}
	return &llm.GenerateResponse{Text: output}, nil
}

func compressionTestRuntime(provider *compressionTestProvider) *Runtime {
	return NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
}

func TestReplyCompressionSkipsWithinBudget(t *testing.T) {
	for _, limit := range []int{0, 4, 10} {
		p := &compressionTestProvider{err: errors.New("must not call")}
		got, err := compressionTestRuntime(p).prepareGeneratedReply(context.Background(), BotConfig{MaxReplyChars: limit}, replySingleMarker+"四个字符")
		if err != nil || got != replySingleMarker+"四个字符" || len(p.requests) != 0 {
			t.Fatalf("limit=%d got=%q err=%v calls=%d", limit, got, err, len(p.requests))
		}
	}
	if got := replyCompressionRunes("正文[CQ:image,file=very-long-image-reference]" + notificationSplitMarker + "结尾"); got != 5 {
		t.Fatalf("non-text payload or split marker counted as text: %d", got)
	}
}

func TestReplyCompressionPreservesChoiceAndDoesNotStream(t *testing.T) {
	p := &compressionTestProvider{outputs: []string{replyAutoMarker + "核心结论" + replySuppressionMarker}}
	ctx := withTextDeltaObserver(context.Background(), &telegramReplyDraft{channel: &deliveryDraftChannel{}})
	got, err := compressionTestRuntime(p).prepareGeneratedReply(ctx, BotConfig{MaxReplyChars: 10}, replySingleMarker+strings.Repeat("较长说明", 20)+replyRefusalMarker)
	body, intent := consumeReplyControlIntent(got)
	if err != nil || body != "核心结论" || intent.DeliveryMode != replyDeliverySingle || !intent.RefuseCurrent || intent.SuppressCurrentUser {
		t.Fatalf("body=%q intent=%+v err=%v", body, intent, err)
	}
	if len(p.requests) != 1 || p.observed || !reflect.DeepEqual(p.purposes, []string{"reply_compression"}) || len(p.requests[0].Tools) != 0 {
		t.Fatal("compression call was not isolated and accounted for")
	}
}

func TestReplyCompressionRetriesOnlyOnceAgainstOriginal(t *testing.T) {
	p := &compressionTestProvider{outputs: []string{strings.Repeat("长", 20), "简洁结论"}}
	original := strings.Repeat("原始说明", 20)
	got, err := compressionTestRuntime(p).prepareGeneratedReply(context.Background(), BotConfig{MaxReplyChars: 10}, original)
	if err != nil || got != "简洁结论" || len(p.requests) != 2 {
		t.Fatalf("got=%q err=%v calls=%d", got, err, len(p.requests))
	}
	for index, req := range p.requests {
		var payload struct {
			Reply string `json:"reply"`
			Issue string `json:"previous_issue"`
		}
		if err := json.Unmarshal([]byte(req.Messages[1].Content), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Reply != original || (index == 1 && payload.Issue == "") {
			t.Fatal("repair did not retain the original or explain the failure")
		}
	}
}

func TestReplyCompressionFailureNeverReturnsOversizedOrTruncatedText(t *testing.T) {
	for _, output := range []string{"", strings.Repeat("长", 20)} {
		p := &compressionTestProvider{capturingLLMProvider: capturingLLMProvider{reply: output}}
		got, err := compressionTestRuntime(p).prepareGeneratedReply(context.Background(), BotConfig{MaxReplyChars: 10}, strings.Repeat("原文", 20))
		if got != "" || !errors.Is(err, errReplyCompression) || len(p.requests) != 2 {
			t.Fatalf("got=%q err=%v calls=%d", got, err, len(p.requests))
		}
	}
	p := &compressionTestProvider{err: errors.New("provider unavailable")}
	got, err := compressionTestRuntime(p).prepareGeneratedReply(context.Background(), BotConfig{MaxReplyChars: 10}, strings.Repeat("原文", 20))
	if got != "" || !errors.Is(err, errReplyCompression) || len(p.requests) != 1 {
		t.Fatalf("provider failure: %q %v calls=%d", got, err, len(p.requests))
	}
	if strings.Contains(publicChatErrorMessage(err), "provider unavailable") {
		t.Fatal("internal compression error leaked to user")
	}
}

func TestReplyCompressionProtectsCodeAndMedia(t *testing.T) {
	code := "```python\nprint(1)\n```"
	p := &compressionTestProvider{outputs: []string{"已处理", code}}
	got, err := compressionTestRuntime(p).prepareGeneratedReply(context.Background(), BotConfig{MaxReplyChars: 40, MarkdownToPlain: boolPointer(false)}, replySingleMarker+strings.Repeat("说明", 30)+"\n"+code)
	if err != nil || got != replySingleMarker+code || len(p.requests) != 2 {
		t.Fatalf("protected code changed: %q %v", got, err)
	}
	p = &compressionTestProvider{outputs: []string{"已处理", code}}
	got, err = compressionTestRuntime(p).prepareGeneratedReply(context.Background(), BotConfig{MaxReplyChars: 10, MarkdownToPlain: boolPointer(true)}, replySingleMarker+strings.Repeat("说明", 30)+"\n"+code)
	if err != nil || got != replySingleMarker+"print(1)" || len(p.requests) != 2 {
		t.Fatalf("plain-text conversion lost protected code: %q %v", got, err)
	}
	p = &compressionTestProvider{}
	if _, err := compressionTestRuntime(p).prepareGeneratedReply(context.Background(), BotConfig{MaxReplyChars: 5}, code); !errors.Is(err, errReplyCompression) || len(p.requests) != 0 {
		t.Fatal("attempted to shrink an indivisible code block")
	}
	media := "[CQ:image,file=image-token]"
	if compressionCandidateIssue("说明"+media, "结论", 10) == "" || compressionCandidateIssue("说明", "结论"+media, 10) == "" {
		t.Fatal("media was dropped or invented")
	}
	if compressionCandidateIssue("说明"+media, "结论"+media, 10) != "" {
		t.Fatal("unchanged media was rejected")
	}
}

func TestReplyCompressionDoesNotRegenerateTheAnswer(t *testing.T) {
	p := &compressionTestProvider{capturingLLMProvider: capturingLLMProvider{reply: strings.Repeat("说明", 30)}, outputs: []string{"简洁结论"}}
	rt := compressionTestRuntime(p)
	got, err := rt.generateReply(context.Background(), BotConfig{MaxReplyChars: 10}, MessageEvent{}, RelationshipPolicy{}, []llm.Message{{Role: llm.RoleUser, Content: "问题"}}, nil)
	if err != nil || got != "简洁结论" || len(p.requests) != 1 || p.generationCalls != 1 {
		t.Fatalf("generation/compression did not remain separate: %q %v", got, err)
	}
	assertGenerationBudget(t, p.requestSnapshot(), 10)
}

func TestReplyCompressionPreservesAutoModeAndDraftLimit(t *testing.T) {
	p := &compressionTestProvider{outputs: []string{replySingleMarker + "结论" + notificationSplitMarker + "补充"}}
	got, err := compressionTestRuntime(p).prepareGeneratedReply(context.Background(), BotConfig{MaxReplyChars: 10}, replyAutoMarker+strings.Repeat("内容", 20))
	if err != nil || !strings.HasPrefix(got, replyAutoMarker) || len(splitChatReply(got, chatSplitLimits{})) != 2 {
		t.Fatalf("auto mode changed: %q %v", got, err)
	}
	channel := &deliveryDraftChannel{}
	draft := &telegramReplyDraft{channel: channel, maxRunes: 4}
	draft.ObserveTextDelta(context.Background(), "一二三四五六")
	if len(channel.messages) != 1 || channel.messages[0].Text != "一二三四" {
		t.Fatal("draft exceeded its preview budget")
	}
}

func TestLiveReplyCompression(t *testing.T) {
	client := liveLLMClient(t)
	raw, err := os.ReadFile("testdata/document_delivery_live_20260906.json")
	if err != nil {
		t.Fatal(err)
	}
	var sources []struct {
		Input  string `json:"input"`
		Output string `json:"raw_output"`
	}
	if err := json.Unmarshal(raw, &sources); err != nil || len(sources) == 0 {
		t.Fatalf("fixture: %v", err)
	}
	budget := 2
	if value := os.Getenv("DIANA_TEST_COMPRESSION_CALL_BUDGET"); value != "" {
		budget, err = strconv.Atoi(value)
		if err != nil || budget < 1 || budget > 2 {
			t.Fatal("compression test call budget must be 1 or 2")
		}
	}
	probe := &documentDeliveryProbe{LLMClient: client, remaining: budget}
	channel := &recordingChannel{}
	cfg := BotConfig{MaxReplyChars: 300, ForwardReplyThreshold: 1, MarkdownToPlain: boolPointer(false)}.WithDefaults()
	rt := NewRuntime(cfg, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return probe, nil })
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	// Reuse a captured real reply; do not pay to generate another itinerary.
	result, runErr := rt.prepareGeneratedReply(ctx, cfg, replySingleMarker+sources[0].Output)
	var sent []string
	if runErr == nil {
		_, runErr = rt.sendDecorated(ctx, MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001", SelfID: "42"}, result, outboundDecoration{})
		for _, message := range channel.sentSnapshot() {
			sent = append(sent, message.Text)
		}
	}
	errorText := ""
	if runErr != nil {
		errorText = strings.ReplaceAll(runErr.Error(), os.Getenv("DIANA_TEST_LLM_API_KEY"), "[redacted]")
	}
	record, err := json.Marshal(map[string]any{
		"original_input": sources[0].Input, "original_output": sources[0].Output,
		"limit": cfg.MaxReplyChars, "preset_delivery_mode": "single", "requests": probe.requests,
		"responses": probe.responses, "final_output": result, "sent": sent,
		"model_calls": budget - probe.remaining, "error": errorText,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("COMPRESSION_SAMPLE %s", record)
	if runErr != nil {
		t.Fatal(errorText)
	}
	if len(sent) != 1 || replyCompressionRunes(sent[0]) > cfg.MaxReplyChars || len(channel.callsSnapshot()) != 0 {
		t.Fatalf("compression/delivery contract failed: %q", sent)
	}
}
