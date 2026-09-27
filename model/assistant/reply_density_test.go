// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func densityTestRuntime(cfg BotConfig, provider LLMProvider) *Runtime {
	if cfg.BotAccount == "" {
		cfg.BotAccount = "42"
	}
	if len(cfg.GroupTriggers) == 0 {
		cfg.GroupTriggers = []string{"Diana"}
	}
	var factory LLMProviderFactory
	if provider != nil {
		factory = func() (LLMProvider, error) { return provider, nil }
	}
	return NewRuntime(cfg, &recordingChannel{}, NewPluginManager(), nil, nil, nil, factory)
}

func densityTestEvent(messageID, text string) MessageEvent {
	return MessageEvent{
		Kind: EventKindGroup, GroupID: "123456", UserID: "20002", SelfID: "42", MessageID: messageID,
		RawMessage: text, Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
	}
}

// recordDenseSends 记 count 次已发出的回复，最后一次在 last，之前每次间隔 10 秒。
func recordDenseSends(r *Runtime, count int, last time.Time) {
	for i := 0; i < count; i++ {
		at := last.Add(-time.Duration(count-1-i) * 10 * time.Second)
		r.recordReplyDensitySend(densityTestEvent(fmt.Sprintf("sent-%d", i), "Diana 在吗"), at)
	}
}

func loopAuditVerdict(purposeless bool, reason string) string {
	return fmt.Sprintf(`{"send_confidence":0.9,"account_safe":true,"count_refusal":false,"reply_loop_meaningless":false,"reply_loop_purposeless":%v,"reply_loop_confidence":0.97,"reply_loop_reason":%q}`, purposeless, reason)
}

// 只回过一条时审核不带密度、不判目的。
// 一来一回两次之后才谈得上「一连串来回」，再早就没有东西可判。
func TestReplyAuditOnlyJudgesPurposeWhenDense(t *testing.T) {
	provider := &sequenceLLMProvider{auditReplies: []string{loopAuditVerdict(true, "续写剧情")}}
	r := densityTestRuntime(BotConfig{}, provider)
	now := time.Now()
	recordDenseSends(r, replyDensityDenseLimit-1, now.Add(-time.Minute))
	event := botReplyLoopEvent(r, "sparse", "20002", 0, now.Add(-30*time.Second), 10*time.Second, "Diana 接着演")
	if _, err := r.auditReplyBeforeSend(context.Background(), event, "Diana 接着演", "好呀", r.effectiveConfigForEvent(event), false); err != nil {
		t.Fatal(err)
	}
	if payload := requestTextContent(provider.requestsSnapshot()[0]); strings.Contains(payload, `"exchange_density":`) {
		t.Fatalf("不密时审核载荷不该带密度：%s", payload)
	}
}

// 对方被标记为机器人时，回到第二条就问目的：审核本来就要跑这一次，密度只是同一份载荷里
// 多一个字段，让它先转够十轮再问等于白放前面那些。
func TestReplyAuditAsksPurposeAtSecondReply(t *testing.T) {
	if replyDensityDenseLimit != 2 {
		t.Fatalf("这道门定的是两条，现在是 %d", replyDensityDenseLimit)
	}
	provider := &sequenceLLMProvider{auditReplies: []string{loopAuditVerdict(true, "反复寒暄，没有要完成的事")}}
	r := densityTestRuntime(BotConfig{MarkedBotIDs: []string{"20002"}}, provider)
	now := time.Now()
	recordDenseSends(r, replyDensityDenseLimit, now.Add(-time.Minute))
	event := botReplyLoopEvent(r, "early", "20002", 0, now.Add(-30*time.Second), 10*time.Second, "Diana 晚安")
	if _, err := r.auditReplyBeforeSend(context.Background(), event, "Diana 晚安", "晚安喵", r.effectiveConfigForEvent(event), false); err != nil {
		t.Fatal(err)
	}
	payload := requestTextContent(provider.requestsSnapshot()[0])
	if !strings.Contains(payload, fmt.Sprintf(`"bot_replies_to_sender":%d`, replyDensityDenseLimit)) {
		t.Fatalf("回到 %d 条就该把密度递给审核：%s", replyDensityDenseLimit, payload)
	}
}

// 真人回得再密也不问目的：斗嘴、调侃在群聊里再正常不过，线上被「没目的」暂停的
// 真人就是这么来的。
func TestReplyAuditSkipsPurposeForHumans(t *testing.T) {
	provider := &sequenceLLMProvider{auditReplies: []string{loopAuditVerdict(true, "斗嘴")}}
	r := densityTestRuntime(BotConfig{}, provider)
	now := time.Now()
	recordDenseSends(r, replyDensityDenseLimit+3, now.Add(-time.Minute))
	event := botReplyLoopEvent(r, "human", "20002", 0, now.Add(-30*time.Second), 10*time.Second, "Diana 你有本事就封我")
	if _, err := r.auditReplyBeforeSend(context.Background(), event, "Diana 你有本事就封我", "不封，陪你玩", r.effectiveConfigForEvent(event), false); err != nil {
		t.Fatal(err)
	}
	if payload := requestTextContent(provider.requestsSnapshot()[0]); strings.Contains(payload, `"exchange_density":`) {
		t.Fatalf("对方是真人时不该问目的：%s", payload)
	}
}

// 密度门槛与对方是不是机器人无关，标记账号不再单设一档，走的是同一条门。
func TestReplyDensityDenseLimitIgnoresBotMarker(t *testing.T) {
	now := time.Now()
	event := densityTestEvent("m", "x")
	marked := densityTestRuntime(BotConfig{MarkedBotIDs: []string{"20002"}}, nil)
	plain := densityTestRuntime(BotConfig{}, nil)
	for _, tc := range []struct {
		name string
		r    *Runtime
	}{{"marked", marked}, {"plain", plain}} {
		recordDenseSends(tc.r, replyDensityDenseLimit-1, now.Add(-time.Minute))
		if _, dense := tc.r.replyDensityForAudit(event, now); dense {
			t.Fatalf("%s：只回过一条就带上了密度", tc.name)
		}
		recordDenseSends(tc.r, replyDensityDenseLimit, now)
		if _, dense := tc.r.replyDensityForAudit(event, now); !dense {
			t.Fatalf("%s：回到两条就该带密度", tc.name)
		}
	}
}

// 回得很密、审核判无目的，也不再按账号「降欲望」：被 @ 的下一条照常进回复，
// 事件里不会再出现 ignored_reply_damping。空转只计数，够阈值走暂停。
func TestPurposelessVerdictDoesNotBlockNextReply(t *testing.T) {
	provider := &sequenceLLMProvider{auditReplies: []string{loopAuditVerdict(true, "漫无目的地互相接戏")}}
	r := densityTestRuntime(BotConfig{}, provider)
	now := time.Now()
	recordDenseSends(r, replyDensityDenseLimit+3, now.Add(-5*time.Second))
	event := botReplyLoopEvent(r, "dense", "20002", 0, now.Add(-20*time.Second), 10*time.Second, "Diana 镜子里的是谁")
	if _, err := r.auditReplyBeforeSend(context.Background(), event, "Diana 镜子里的是谁", "是你自己呀", r.effectiveConfigForEvent(event), false); err != nil {
		t.Fatal(err)
	}
	next := densityTestEvent("follow", "Diana 你有本事就封我")
	next.ToMe = true
	_, _, handled, outcome := r.prepareMessageEvent(context.Background(), next)
	if !handled || outcome != "replied" {
		t.Fatalf("handled=%v outcome=%q，被 @ 的消息应照常回复", handled, outcome)
	}
}

// 下棋这类有明确任务的高频来回：哪怕对面是 AI，一轮轮审核都不计数，永远不暂停。
func TestPurposefulDenseExchangeIsNeverPaused(t *testing.T) {
	var verdicts []string
	for i := 0; i < defaultBotReplyLoopThreshold+2; i++ {
		verdicts = append(verdicts, loopAuditVerdict(false, "对方是 AI，但在按规则报棋步"))
	}
	provider := &sequenceLLMProvider{auditReplies: verdicts}
	r := densityTestRuntime(BotConfig{MarkedBotIDs: []string{"20002"}}, provider)
	now := time.Now()
	recordDenseSends(r, replyDensityDenseLimit+5, now.Add(-time.Minute))
	for i := 0; i < defaultBotReplyLoopThreshold+2; i++ {
		event := botReplyLoopEvent(r, "chess", "20002", i, now.Add(-time.Duration(40-i)*time.Second), 2*time.Second, "Diana 黑落 H8")
		if _, err := r.auditReplyBeforeSend(context.Background(), event, "Diana 黑落 H8", "白落 I9", r.effectiveConfigForEvent(event), false); err != nil {
			t.Fatalf("第 %d 轮被拦：%v", i+1, err)
		}
	}
	if _, blocked := r.activeReplySuppression(densityTestEvent("x", "Diana"), time.Now()); blocked {
		t.Fatal("有明确任务的来回被暂停了")
	}
}

// 主人、关掉循环检测时不计密度；主人解除响应限制时清零；窗口外的回复不算密。
func TestReplyDensityScope(t *testing.T) {
	now := time.Now()

	owner := densityTestRuntime(BotConfig{OwnerID: "20002"}, nil)
	recordDenseSends(owner, replyDensityDenseLimit*2, now.Add(-time.Minute))
	if _, dense := owner.replyDensityForAudit(densityTestEvent("m", "x"), now); dense {
		t.Fatal("主人不计回复密度")
	}

	disabled := false
	off := densityTestRuntime(BotConfig{BotReplyLoopDetectionEnabled: &disabled}, nil)
	recordDenseSends(off, replyDensityDenseLimit*2, now.Add(-time.Minute))
	if _, dense := off.replyDensityForAudit(densityTestEvent("m", "x"), now); dense {
		t.Fatal("关掉机器人循环检测后不该判目的")
	}

	r := densityTestRuntime(BotConfig{}, nil)
	recordDenseSends(r, replyDensityDenseLimit, now.Add(-time.Minute))
	r.resetBotReplyLoopUser("20002")
	if _, dense := r.replyDensityForAudit(densityTestEvent("m", "x"), now); dense {
		t.Fatal("解除后应清零")
	}

	stale := densityTestRuntime(BotConfig{}, nil)
	recordDenseSends(stale, replyDensityDenseLimit*2, now.Add(-replyDensityWindow-time.Minute))
	if _, dense := stale.replyDensityForAudit(densityTestEvent("m", "x"), now); dense {
		t.Fatal("窗口外的回复不该算密")
	}
}

func meaninglessAuditVerdict(meaningless bool, reason string) string {
	return fmt.Sprintf(`{"send_confidence":0.9,"account_safe":true,"count_refusal":false,"reply_loop_meaningless":%v,"reply_loop_purposeless":false,"reply_loop_confidence":0.97,"reply_loop_reason":%q}`, meaningless, reason)
}
