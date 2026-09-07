package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func quotedPowerEvent(id, watts string) MessageEvent {
	event := directedGroupMessage(id, "user", "")
	event.Quoted = &QuotedMessage{MessageID: "quote-" + id, UserID: "author", SenderName: "MilkSU", RawMessage: "按" + watts + "W算吧"}
	return event
}

func TestReplyClassifierRetainsOriginalAndAcceptedQuotes(t *testing.T) {
	root := quotedPowerEvent("root", "45")
	prior := quotedPowerEvent("prior", "80")
	p := &topicTestProvider{result: `{"relation":"repeat","confidence":0.99}`}
	p.onTopic = func(req llm.GenerateRequest) {
		var payload struct {
			Original map[string]any        `json:"original_question_quoted"`
			Prior    []replyRequestContext `json:"accepted_supplement_requests"`
		}
		if err := json.Unmarshal([]byte(req.Messages[1].Content), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Original["text"] != "按45W算吧" || len(payload.Prior) != 1 || payload.Prior[0].Quoted["text"] != "按80W算吧" {
			t.Fatalf("lost quote conditions: %+v", payload)
		}
	}
	r := topicTestRuntime(p)
	follow := directedGroupMessage("next", "user", "继续按刚才的算")
	r.classifyDirectReplyTopic(context.Background(), root, []proactiveReplyCandidate{{Event: prior}}, follow, follow.RawMessage)
}

func TestReplyRequestDistinguishesUnavailableAndInferredQuotes(t *testing.T) {
	event := MessageEvent{Segments: []MessageSegment{{Type: "reply", Data: map[string]string{"id": "missing"}}}}
	q := requestContextForReply(event, "").Quoted
	if q["message_id"] != "missing" || q["content_available"] != false {
		t.Fatalf("unavailable quote lost: %v", q)
	}
	event.Quoted = &QuotedMessage{MessageID: "inferred", RawMessage: "参考背景", Semantic: true}
	q = requestContextForReply(event, "").Quoted
	if q["source"] != "semantic_reference" {
		t.Fatalf("inferred quote promoted: %v", q)
	}
	event.Quoted = &QuotedMessage{MessageID: "image", Segments: []MessageSegment{{Type: "image", Data: map[string]string{"file": "image"}}}}
	q = requestContextForReply(event, "").Quoted
	if q["images"] != 1 || q["text"] != "" {
		t.Fatalf("image context=%v", q)
	}
	if requestContextForReply(MessageEvent{}, "ordinary").Quoted != nil {
		t.Fatal("invented quote")
	}
}

func TestSemanticGateRetainsCurrentPendingAndSentQuotes(t *testing.T) {
	p := &semanticGateProvider{result: `{"action":"keep","confidence":0.99}`}
	r := topicTestRuntime(p)
	root := quotedPowerEvent("root", "45")
	ctx, finish := r.beginDirectReply(context.Background(), root)
	defer finish()
	prior := quotedPowerEvent("prior", "80")
	r.replyInterruptMu.Lock()
	r.activeDirectReplies[directReplyMergeKey(root)].supplements = []proactiveReplyCandidate{{Event: prior}}
	r.replyInterruptMu.Unlock()
	g, release, _ := r.lockSemanticReply(ctx, root)
	defer release()
	g.rememberRequest(requestContextForReply(root, ""), replyRequestContexts([]proactiveReplyCandidate{{Event: prior}}), "按80W计算")
	current := directedGroupMessage("current", "user", "")
	current.Quoted = &QuotedMessage{MessageID: "repeat", UserID: "user", RawMessage: "请完整重复一次"}
	if _, err := r.deduplicateReply(ctx, current, "", "按80W计算", BotConfig{}, g); err != nil {
		t.Fatal(err)
	}
	if len(p.requests) != 1 {
		t.Fatal("missing gate call")
	}
	var payload struct {
		Current replyRequestContext   `json:"current_request_context"`
		Prior   []replyRequestContext `json:"accepted_supplement_requests"`
		Sent    []semanticSentReply   `json:"recent_sent"`
	}
	if err := json.Unmarshal([]byte(p.requests[0].Messages[1].Content), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Current.Quoted["text"] != "请完整重复一次" || len(payload.Prior) != 1 || payload.Prior[0].Quoted["text"] != "按80W算吧" {
		t.Fatalf("current context lost: %+v", payload)
	}
	if len(payload.Sent) != 1 || payload.Sent[0].RequestContext.Quoted["text"] != "按45W算吧" || payload.Sent[0].Supplements[0].Quoted["text"] != "按80W算吧" {
		t.Fatalf("sent context lost: %+v", payload)
	}
}

type quotedCorrectionProvider struct {
	base *directReplyMergeProvider
	t    *testing.T
}

func (p *quotedCorrectionProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	for _, m := range req.Messages {
		if strings.Contains(m.Content, directReplyTopicPrompt) {
			return &llm.GenerateResponse{Text: `{"relation":"correction","confidence":0.99}`}, nil
		}
	}
	resp, err := p.base.Generate(ctx, req)
	if err == nil && resp != nil && resp.Text == "两条一起回答" {
		var found bool
		for _, m := range req.Messages {
			if strings.Contains(m.Content, "当前同轮补充消息") && strings.Contains(m.Content, "按80W算吧") {
				found = true
			}
		}
		if !found {
			p.t.Error("regenerated request lost quoted 80W correction")
		}
		last := req.Messages[len(req.Messages)-1].Content
		if !strings.Contains(last, "本轮已接受的后续请求") || !strings.Contains(last, "按80W算吧") {
			p.t.Error("final user message reverted to the original parameters")
		}
		resp.Text = "按80W计算，一小时耗电0.08度"
	}
	return resp, err
}

func TestQuotedCorrectionRegeneratesAndRecordsEffectiveRequest(t *testing.T) {
	base := &directReplyMergeProvider{firstStarted: make(chan struct{}), releaseFirst: make(chan struct{})}
	provider := &quotedCorrectionProvider{base: base, t: t}
	r := topicTestRuntime(provider)
	root := directedGroupMessage("root", "user", "45W工作一小时耗电多少")
	r.remember(root)
	done := make(chan error, 1)
	go func() {
		_, err := r.replyAndRecord(context.Background(), root, root.RawMessage, "replied")
		done <- err
	}()
	select {
	case <-base.firstStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("no first generation")
	}
	follow := quotedPowerEvent("correction", "80")
	r.noteDirectedInbound(follow)
	_, _, handled, outcome := r.prepareMessageEvent(context.Background(), follow)
	close(base.releaseFirst)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reply did not finish")
	}
	if handled || outcome != "merged_into_reply" {
		t.Fatalf("correction not merged: %s", outcome)
	}
	if sent := r.channel.(*recordingChannel).sentSnapshot(); len(sent) != 1 || sent[0].Text != "按80W计算，一小时耗电0.08度" {
		t.Fatalf("stale reply sent: %+v", sent)
	}
	g, release, _ := r.lockSemanticReply(context.Background(), root)
	defer release()
	if len(g.sent) != 1 || len(g.sent[0].Supplements) != 1 || g.sent[0].Supplements[0].Quoted["text"] != "按80W算吧" {
		t.Fatalf("success record lost correction: %+v", g.sent)
	}
}
