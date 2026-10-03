package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

const chickenFollowupCandidate = "从解剖学来说翅膀确实是前肢没毛病……[diana-msg]但要是真改叫鸡手，去店里点一份「新奥尔良烤鸡手」听着也太吓人了吧（"

type followupSelfRepeatProvider struct {
	semanticGateProvider
}

func (p *followupSelfRepeatProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	for _, message := range req.Messages {
		if strings.Contains(message.Content, "你是机器人回复的发送前审核器") {
			p.mu.Lock()
			p.audits = append(p.audits, req.Messages[len(req.Messages)-1].Content)
			p.mu.Unlock()
			// 原候选被误判为复读；去重合并后的重答必须重新审核，不能复用旧结论。
			var payload struct {
				Candidate string `json:"candidate_reply"`
			}
			_ = json.Unmarshal([]byte(strings.TrimPrefix(req.Messages[len(req.Messages)-1].Content, "请审核以下回复：\n")), &payload)
			repeated := !strings.Contains(payload.Candidate, "鸡爪就是鸡的脚")
			return &llm.GenerateResponse{Text: selfRepeatVerdict(repeated, 0.95, "")}, nil
		}
		if strings.Contains(message.Content, semanticReplyPrompt) {
			return p.semanticGateProvider.Generate(ctx, req)
		}
	}
	return &llm.GenerateResponse{Text: chickenFollowupCandidate}, nil
}

func TestFollowupMergedAnswerIsReauditedBeforeDelivery(t *testing.T) {
	for _, tc := range []struct {
		name, result, want string
	}{
		{"confirmed_followup_kept", `{"action":"keep","confidence":0.99}`, "从解剖学来说"},
		{"uncertain_keep_does_not_bypass_audit", `{"action":"keep","confidence":0.85}`, ""},
		{"uncertain_rewrite_does_not_bypass_audit", `{"action":"rewrite","confidence":0.85,"content":"翅膀是前肢"}`, ""},
		{"rewrite", `{"action":"rewrite","confidence":0.99,"content":"对，鸡爪就是鸡的脚，翅膀才是前肢"}`, "鸡爪就是鸡的脚"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &followupSelfRepeatProvider{semanticGateProvider: semanticGateProvider{result: tc.result}}
			r := topicTestRuntime(p)
			follow := botReplyLoopEvent(r, "chicken", "user", 0, time.Now().Add(-time.Minute), 10*time.Second, "鸡爪不应该叫鸡脚")
			follow.Quoted.RawMessage = "因为鸡爪已经把手的位置占了（？"
			follow.Quoted.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": follow.Quoted.RawMessage}}}
			gate, release, err := r.lockSemanticReply(context.Background(), follow)
			if err != nil {
				t.Fatal(err)
			}
			gate.remember("鸡翅为什么不叫鸡手", "因为鸡爪已经把手的位置占了（？而且奥尔良烤鸡手听着太怪了", "user")
			gate.sent[0].SentAt = time.Unix(follow.Time-10, 0)
			release()
			outcome, err := r.replyAndRecord(context.Background(), follow, "鸡爪不应该叫鸡脚", "replied")
			sent := r.channel.(*recordingChannel).sentSnapshot()
			if tc.want == "" {
				if err != nil || outcome != "ignored_self_repeat" || len(sent) != 0 {
					t.Fatalf("原候选仍应接受防复读审核：outcome=%s err=%v sent=%#v", outcome, err, sent)
				}
				return
			}
			if err != nil || outcome != "replied" || len(sent) == 0 || !strings.Contains(sent[0].Text, tc.want) {
				t.Fatalf("追问应收到回答：outcome=%s err=%v sent=%#v", outcome, err, sent)
			}
			if _, blocked := r.activeReplySuppression(follow, time.Now()); blocked {
				t.Fatal("候选答案的复读误判不能让追问者被暂停")
			}
			if len(p.audits) == 0 || !strings.Contains(p.audits[0], "因为鸡爪已经把手的位置占了") {
				t.Fatal("审核必须看得到追问引用的那条答案")
			}
			if tc.name == "rewrite" && (len(p.audits) != 2 || !strings.Contains(p.audits[1], tc.want)) {
				t.Fatal("去重合并后的重答必须按新正文重新审核")
			}
		})
	}
}

func TestDirectSelfRepeatGuardRemainsEnabled(t *testing.T) {
	for _, marked := range []bool{false, true} {
		p := &sequenceLLMProvider{auditReplies: []string{selfRepeatVerdict(true, 0.95, "反复道别")}}
		cfg := BotConfig{BotAccount: "42"}
		if marked {
			cfg.MarkedBotIDs = []string{"user"}
		}
		r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return p, nil })
		event := botReplyLoopEvent(r, "repeat", "user", 0, time.Now().Add(-time.Minute), time.Second, "晚安")
		_, err := r.auditReplyBeforeSend(context.Background(), event, "晚安", "晚安，明天见", r.effectiveConfigForEvent(event), false)
		if !errors.Is(err, errReplySelfRepeatDropped) {
			t.Fatalf("不能绕过直接回复的防复读审核：marked=%v err=%v", marked, err)
		}
	}
}

func TestReplyAuditOnlyIncludesQuoteForLoopCheck(t *testing.T) {
	p := &qualityTestProvider{reply: `{"send_confidence":0.99,"account_safe":0.98}`}
	r := topicTestRuntime(p)
	event := botReplyLoopEvent(r, "quote", "user", 0, time.Now().Add(-time.Minute), time.Second, "鸡爪不应该叫鸡脚")
	for _, loop := range []bool{false, true} {
		if _, err := r.runReplyAudit(context.Background(), event, event.RawMessage, "鸡爪确实是脚", BotConfig{}, botReplyLoopEvidence{}, replyAuditNeed{Loop: loop}); err != nil {
			t.Fatal(err)
		}
		request := p.requests[len(p.requests)-1]
		content := request.Messages[len(request.Messages)-1].Content
		var payload map[string]json.RawMessage
		if err := json.Unmarshal([]byte(strings.TrimPrefix(content, "请审核以下回复：\n")), &payload); err != nil {
			t.Fatal(err)
		}
		if _, exists := payload["original_request_context"]; exists != loop {
			t.Fatalf("quoted context included=%v, loop=%v", exists, loop)
		}
	}
}
