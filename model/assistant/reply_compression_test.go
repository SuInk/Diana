package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

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

// 出站最后一道闸：被渲染成人话的工具调用不是回复正文，宁可整轮按失败记账，也不能
// 发出去。生产事故（2026-09-09 群聊）就是下面这一整行被当成回复发进了群。
func TestRenderedToolCallNeverLeavesTheOutboundPath(t *testing.T) {
	leaked := `调用工具：agent_finalize，参数：{"content":"对，确实会跳！\nWARP 用的本来就是 Cloudflare 的动态共享 IP 池，纯纯是负优化喵～"}`
	for _, reply := range []string{leaked, replySingleMarker + leaked} {
		p := &compressionTestProvider{err: errors.New("must not call")}
		got, err := compressionTestRuntime(p).prepareGeneratedReply(context.Background(), BotConfig{MaxReplyChars: 8000}, reply)
		if !errors.Is(err, errRenderedToolCallReply) {
			t.Fatalf("渲染出来的工具调用应被拦下，实际 got=%q err=%v", got, err)
		}
		if got != "" {
			t.Fatalf("拦截后不能带出任何正文：%q", got)
		}
	}
}

// 正常说到「调用工具」的句子不能被误拦。
func TestNormalReplyMentioningToolsStillSends(t *testing.T) {
	p := &compressionTestProvider{err: errors.New("must not call")}
	reply := "我先调用工具查一下再回你喵～"
	got, err := compressionTestRuntime(p).prepareGeneratedReply(context.Background(), BotConfig{MaxReplyChars: 8000}, reply)
	if err != nil || got != reply {
		t.Fatalf("正常回复被误拦：%q %v", got, err)
	}
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
	code := "```python" + notificationLineMarker + "print(1)" + notificationLineMarker + "```"
	p := &compressionTestProvider{outputs: []string{"已处理", code}}
	got, err := compressionTestRuntime(p).prepareGeneratedReply(context.Background(), BotConfig{MaxReplyChars: 40, MarkdownToPlain: boolPointer(false)}, replySingleMarker+strings.Repeat("说明", 30)+notificationLineMarker+code)
	if err != nil || got != replySingleMarker+code || len(p.requests) != 2 {
		t.Fatalf("protected code changed: %q %v", got, err)
	}
	p = &compressionTestProvider{outputs: []string{"已处理", code}}
	got, err = compressionTestRuntime(p).prepareGeneratedReply(context.Background(), BotConfig{MaxReplyChars: 10, MarkdownToPlain: boolPointer(true)}, replySingleMarker+strings.Repeat("说明", 30)+notificationLineMarker+code)
	if err != nil || got != replySingleMarker+"print(1)" || len(p.requests) != 2 {
		t.Fatalf("plain-text conversion lost protected code: %q %v", got, err)
	}
	// 上限小到连一个字符加上围栏都装不下时拆不了，仍然报错，也不能拿去压缩。
	p = &compressionTestProvider{}
	if _, err := compressionTestRuntime(p).prepareGeneratedReply(context.Background(), BotConfig{MaxReplyChars: 5, MarkdownToPlain: boolPointer(false)}, code); !errors.Is(err, errReplyCompression) || len(p.requests) != 0 {
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

// 生产事故（2026-09-24 群聊）：一段 5000 多字的代码撞上默认 3500 字的单条上限，
// 代码不许压缩，整轮回复直接报错。现在按行拆成几条，每条都是完整围栏且在上限以内。
func TestOversizedCodeBlockSplitsIntoFencedMessages(t *testing.T) {
	var lines []string
	for i := 0; len(strings.Join(lines, "\n")) < 5000; i++ {
		lines = append(lines, fmt.Sprintf("    fmt.Println(\"line %04d of a very long generated program\")", i))
	}
	code := "```go" + notificationLineMarker + strings.Join(lines, notificationLineMarker) + notificationLineMarker + "```"
	for _, tc := range []struct {
		name     string
		platform string
		plain    bool
		prefix   string
	}{
		{name: "qq plain", platform: PlatformOneBotV11, plain: true},
		{name: "qq rich", platform: PlatformOneBotV11},
		{name: "qq single", platform: PlatformOneBotV11, prefix: replySingleMarker},
		{name: "telegram", platform: PlatformTelegram},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &compressionTestProvider{err: errors.New("must not call")}
			cfg := BotConfig{Platform: tc.platform, MaxReplyChars: 3500, MarkdownToPlain: boolPointer(tc.plain)}
			event := MessageEvent{Platform: tc.platform, Kind: EventKindGroup}
			rt := compressionTestRuntime(p)
			got, err := rt.prepareGeneratedReply(context.Background(), cfg, tc.prefix+"代码如下："+notificationLineMarker+code+notificationLineMarker+"跑一下试试", event)
			if err != nil || len(p.requests) != 0 {
				t.Fatalf("err=%v calls=%d", err, len(p.requests))
			}
			if tc.prefix != "" && !strings.HasPrefix(got, replyAutoMarker) {
				t.Fatal("single mode should fall back to split delivery")
			}
			parts := splitEventChatReply(got, cfg, event)
			if len(parts) < 2 {
				t.Fatalf("code was not split: %d parts", len(parts))
			}
			renderedConfig := cfg
			renderedConfig.MarkdownToPlain = boolPointer(false)
			var restored []string
			for index, part := range parts {
				if issue := rt.replyPartLimitIssue(renderedConfig, event, part); issue != "" {
					t.Fatalf("part %d over limit: %s", index, issue)
				}
				if !strings.Contains(part, "fmt.Println") {
					continue
				}
				if tc.plain {
					if strings.Contains(part, "```") {
						t.Fatalf("plain part %d kept fences", index)
					}
				} else if strings.Count(part, "```go") != 1 || strings.Count(part, "```") != 2 {
					t.Fatalf("part %d is not one valid fenced block: %q", index, part)
				}
				for _, line := range strings.Split(part, "\n") {
					// 纯文本平台发送时整条消息会去掉首尾空白，比对时不计缩进。
					if line = strings.TrimSpace(line); strings.HasPrefix(line, "fmt.Println") {
						restored = append(restored, line)
					}
				}
			}
			var want []string
			for _, line := range lines {
				want = append(want, strings.TrimSpace(line))
			}
			if !reflect.DeepEqual(restored, want) {
				t.Fatal("code lines were lost, reordered or rewritten")
			}
			if !strings.HasPrefix(parts[0], "代码如下") || !strings.HasSuffix(parts[len(parts)-1], "跑一下试试") {
				t.Fatalf("surrounding prose lost: %q ... %q", parts[0], parts[len(parts)-1])
			}
		})
	}
}

// 一行本身就超过上限时才在行内硬切，拼回去仍是原文。
func TestOversizedSingleCodeLineIsHardSplit(t *testing.T) {
	line := strings.Repeat("x", 9000)
	fits := func(piece string) bool { return len([]rune(piece)) <= 3500 }
	pieces := splitFencedBlockToFit("```text\nshort\n"+line+"\ntail\n```", fits)
	if len(pieces) < 3 {
		t.Fatalf("pieces=%d", len(pieces))
	}
	var body []string
	for _, piece := range pieces {
		if !fits(piece) || !strings.HasPrefix(piece, "```text\n") || !strings.HasSuffix(piece, "\n```") {
			t.Fatalf("invalid piece: %q", piece[:20])
		}
		body = append(body, strings.TrimSuffix(strings.TrimPrefix(piece, "```text\n"), "\n```"))
	}
	if strings.ReplaceAll(strings.Join(body, "\n"), "\n", "") != "short"+line+"tail" {
		t.Fatal("hard split changed the code")
	}
	if body[0] != "short" || !strings.HasSuffix(body[len(body)-1], "x\ntail") {
		t.Fatalf("hard split should only cut inside the long line: %q ... %q", body[0], body[len(body)-1][len(body[len(body)-1])-10:])
	}
	if splitFencedBlockToFit("```text\nabc\n```", func(string) bool { return false }) != nil {
		t.Fatal("impossible limit should report failure")
	}
}

// 代码块放得下的普通回复不受这次兜底影响，原样通过。
func TestFittingCodeReplyUnchanged(t *testing.T) {
	p := &compressionTestProvider{err: errors.New("must not call")}
	reply := "代码如下：" + notificationLineMarker + "```go" + notificationLineMarker + "fmt.Println(1)" + notificationLineMarker + "```"
	got, err := compressionTestRuntime(p).prepareGeneratedReply(context.Background(), BotConfig{Platform: PlatformOneBotV11, MaxReplyChars: 3500, MarkdownToPlain: boolPointer(false)}, reply)
	if err != nil || got != reply || len(p.requests) != 0 {
		t.Fatalf("got=%q err=%v", got, err)
	}
}
