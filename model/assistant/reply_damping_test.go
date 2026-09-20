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

func dampingTestRuntime(cfg BotConfig, provider LLMProvider) *Runtime {
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

func dampingTestEvent(messageID, text string) MessageEvent {
	return MessageEvent{
		Kind: EventKindGroup, GroupID: "123456", UserID: "20002", SelfID: "42", MessageID: messageID,
		RawMessage: text, Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
	}
}

// recordDampingSends 记 count 次已发出的回复，最后一次在 last，之前每次间隔 10 秒。
func recordDampingSends(r *Runtime, count int, last time.Time) {
	for i := 0; i < count; i++ {
		at := last.Add(-time.Duration(count-1-i) * 10 * time.Second)
		r.recordReplyDampingSend(dampingTestEvent(fmt.Sprintf("sent-%d", i), "Diana 在吗"), at)
	}
}

func loopAuditVerdict(purposeless bool, reason string) string {
	return fmt.Sprintf(`{"send_confidence":0.9,"account_safe":true,"count_refusal":false,"reply_loop_automated_ai":true,"reply_loop_meaningless":false,"reply_loop_purposeless":%v,"reply_loop_confidence":0.97,"reply_loop_reason":%q}`, purposeless, reason)
}

// 只回过一条时审核不带密度、不判目的；模型就算填了无目的也不作数、不降欲望。
// 一来一回两次之后才谈得上「一连串来回」，再早就没有东西可判。
func TestReplyAuditOnlyJudgesPurposeWhenDense(t *testing.T) {
	provider := &sequenceLLMProvider{auditReplies: []string{loopAuditVerdict(true, "续写剧情")}}
	r := dampingTestRuntime(BotConfig{}, provider)
	now := time.Now()
	recordDampingSends(r, replyDampingDenseLimit-1, now.Add(-time.Minute))
	event := botReplyLoopEvent(r, "sparse", "20002", 0, now.Add(-30*time.Second), 10*time.Second, "Diana 接着演")
	if _, err := r.auditReplyBeforeSend(context.Background(), event, "Diana 接着演", "好呀", r.effectiveConfigForEvent(event), false); err != nil {
		t.Fatal(err)
	}
	if payload := requestTextContent(provider.requestsSnapshot()[0]); strings.Contains(payload, `"exchange_density":`) {
		t.Fatalf("不密时审核载荷不该带密度：%s", payload)
	}
	if verdict := r.replyDampingJudge(dampingTestEvent("next", "随口一句"), "随口一句", true, now); verdict.Skip {
		t.Fatalf("不密时不该降欲望：%+v", verdict)
	}
}

// 回到第二条就问目的：审核本来就要跑这一次，密度只是同一份载荷里多一个字段，
// 让它先转够十轮再问等于白放前面那些。
func TestReplyAuditAsksPurposeAtSecondReply(t *testing.T) {
	if replyDampingDenseLimit != 2 {
		t.Fatalf("这道门定的是两条，现在是 %d", replyDampingDenseLimit)
	}
	provider := &sequenceLLMProvider{auditReplies: []string{loopAuditVerdict(true, "反复寒暄，没有要完成的事")}}
	r := dampingTestRuntime(BotConfig{}, provider)
	now := time.Now()
	recordDampingSends(r, replyDampingDenseLimit, now.Add(-time.Minute))
	event := botReplyLoopEvent(r, "early", "20002", 0, now.Add(-30*time.Second), 10*time.Second, "Diana 晚安")
	if _, err := r.auditReplyBeforeSend(context.Background(), event, "Diana 晚安", "晚安喵", r.effectiveConfigForEvent(event), false); err != nil {
		t.Fatal(err)
	}
	payload := requestTextContent(provider.requestsSnapshot()[0])
	if !strings.Contains(payload, fmt.Sprintf(`"bot_replies_to_sender":%d`, replyDampingDenseLimit)) {
		t.Fatalf("回到 %d 条就该把密度递给审核：%s", replyDampingDenseLimit, payload)
	}
	if verdict := r.replyDampingJudge(dampingTestEvent("u", "接着说"), "接着说", false, now); !verdict.Skip {
		t.Fatalf("无目的后没点名的话应放掉：%+v", verdict)
	}
}

// 判据与对方是不是机器人无关，标记账号不再单设一档，走的是同一条门。
func TestReplyDampingDenseLimitIgnoresBotMarker(t *testing.T) {
	now := time.Now()
	event := dampingTestEvent("m", "x")
	marked := dampingTestRuntime(BotConfig{MarkedBotIDs: []string{"20002"}}, nil)
	plain := dampingTestRuntime(BotConfig{}, nil)
	for _, tc := range []struct {
		name string
		r    *Runtime
	}{{"marked", marked}, {"plain", plain}} {
		recordDampingSends(tc.r, replyDampingDenseLimit-1, now.Add(-time.Minute))
		if _, dense := tc.r.replyDensityForAudit(event, now); dense {
			t.Fatalf("%s：只回过一条就带上了密度", tc.name)
		}
		recordDampingSends(tc.r, replyDampingDenseLimit, now)
		if _, dense := tc.r.replyDensityForAudit(event, now); !dense {
			t.Fatalf("%s：回到两条就该带密度", tc.name)
		}
	}
}

// 回得很密、审核判无目的：开始降欲望——不主动接、没点名的不接、点名的按冷却放行；
// 之后再判到有目的就立刻解除。
func TestReplyDampingFollowsPurposeVerdict(t *testing.T) {
	provider := &sequenceLLMProvider{auditReplies: []string{
		loopAuditVerdict(true, "漫无目的地互相接戏"),
		loopAuditVerdict(false, "在报五子棋棋步"),
	}}
	r := dampingTestRuntime(BotConfig{}, provider)
	now := time.Now()
	last := now.Add(-5 * time.Second)
	recordDampingSends(r, replyDampingDenseLimit, last)

	event := botReplyLoopEvent(r, "dense", "20002", 0, now.Add(-20*time.Second), 10*time.Second, "Diana 镜子里的是谁")
	if _, err := r.auditReplyBeforeSend(context.Background(), event, "Diana 镜子里的是谁", "是你自己呀", r.effectiveConfigForEvent(event), false); err != nil {
		t.Fatal(err)
	}
	payload := requestTextContent(provider.requestsSnapshot()[0])
	if !strings.Contains(payload, `"exchange_density":`) || !strings.Contains(payload, fmt.Sprintf(`"bot_replies_to_sender":%d`, replyDampingDenseLimit)) {
		t.Fatalf("密的时候审核载荷要带密度：%s", payload)
	}

	if verdict := r.replyDampingJudge(dampingTestEvent("p", "随口一句"), "随口一句", true, now); !verdict.Skip || !strings.Contains(verdict.Reason, "主动接") {
		t.Fatalf("无目的后主动接话应放掉：%+v", verdict)
	}
	if verdict := r.replyDampingJudge(dampingTestEvent("u", "接着说"), "接着说", false, now); !verdict.Skip || !strings.Contains(verdict.Reason, "只接") {
		t.Fatalf("无目的后没点名的话应放掉：%+v", verdict)
	}
	if verdict := r.replyDampingJudge(dampingTestEvent("n1", "Diana 你看"), "Diana 你看", false, last.Add(10*time.Second)); !verdict.Skip || !strings.Contains(verdict.Reason, "冷却") {
		t.Fatalf("冷却内的点名消息应放掉：%+v", verdict)
	}
	if verdict := r.replyDampingJudge(dampingTestEvent("n2", "Diana 你看"), "Diana 你看", false, last.Add(replyDampingCooldownStep+time.Second)); verdict.Skip {
		t.Fatalf("冷却过后的点名消息应放行：%+v", verdict)
	}

	chess := botReplyLoopEvent(r, "chess", "20002", 1, now.Add(-10*time.Second), 5*time.Second, "Diana 黑 I8，该你了")
	if _, err := r.auditReplyBeforeSend(context.Background(), chess, "Diana 黑 I8，该你了", "白 J9", r.effectiveConfigForEvent(chess), false); err != nil {
		t.Fatal(err)
	}
	if verdict := r.replyDampingJudge(dampingTestEvent("after-chess", "接着说"), "接着说", false, now); verdict.Skip {
		t.Fatalf("判到有目的后应立刻解除降欲望：%+v", verdict)
	}
}

// 下棋这类有明确任务的高频来回：哪怕对面是 AI，一轮轮审核都不计数，永远不暂停。
func TestPurposefulDenseExchangeIsNeverPaused(t *testing.T) {
	var verdicts []string
	for i := 0; i < botReplyLoopThreshold+2; i++ {
		verdicts = append(verdicts, loopAuditVerdict(false, "对方是 AI，但在按规则报棋步"))
	}
	provider := &sequenceLLMProvider{auditReplies: verdicts}
	r := dampingTestRuntime(BotConfig{}, provider)
	now := time.Now()
	recordDampingSends(r, replyDampingDenseLimit+5, now.Add(-time.Minute))
	for i := 0; i < botReplyLoopThreshold+2; i++ {
		event := botReplyLoopEvent(r, "chess", "20002", i, now.Add(-time.Duration(40-i)*time.Second), 2*time.Second, "Diana 黑落 H8")
		if _, err := r.auditReplyBeforeSend(context.Background(), event, "Diana 黑落 H8", "白落 I9", r.effectiveConfigForEvent(event), false); err != nil {
			t.Fatalf("第 %d 轮被拦：%v", i+1, err)
		}
	}
	if _, blocked := r.activeReplySuppression(dampingTestEvent("x", "Diana"), time.Now()); blocked {
		t.Fatal("有明确任务的来回被暂停了")
	}
}

// 冷却随回复次数变长。
func TestReplyDampingCooldownGrowsWithReplies(t *testing.T) {
	r := dampingTestRuntime(BotConfig{}, nil)
	now := time.Now()
	last := now.Add(-2 * time.Minute)
	recordDampingSends(r, replyDampingDenseLimit+2, last)
	r.markReplyPurpose(dampingTestEvent("mark", "x"), true, last)
	if verdict := r.replyDampingJudge(dampingTestEvent("m1", "Diana 你看"), "Diana 你看", false, last.Add(3*replyDampingCooldownStep-time.Second)); !verdict.Skip {
		t.Fatalf("多回两条后冷却应是三档：%+v", verdict)
	}
	if verdict := r.replyDampingJudge(dampingTestEvent("m2", "Diana 你看"), "Diana 你看", false, last.Add(3*replyDampingCooldownStep+time.Second)); verdict.Skip {
		t.Fatalf("三档冷却过后应放行：%+v", verdict)
	}
}

// 主人、关掉循环检测时完全不管；主人解除响应限制时清零。
func TestReplyDampingScope(t *testing.T) {
	now := time.Now()

	owner := dampingTestRuntime(BotConfig{OwnerID: "20002"}, nil)
	recordDampingSends(owner, replyDampingDenseLimit*2, now.Add(-time.Minute))
	owner.markReplyPurpose(dampingTestEvent("mark", "x"), true, now)
	if _, dense := owner.replyDensityForAudit(dampingTestEvent("m", "x"), now); dense {
		t.Fatal("主人不参与回复欲望衰减")
	}
	if verdict := owner.replyDampingJudge(dampingTestEvent("m", "接着说"), "接着说", false, now); verdict.Skip {
		t.Fatalf("主人不受回复欲望衰减影响：%+v", verdict)
	}

	disabled := false
	off := dampingTestRuntime(BotConfig{BotReplyLoopDetectionEnabled: &disabled}, nil)
	recordDampingSends(off, replyDampingDenseLimit*2, now.Add(-time.Minute))
	if _, dense := off.replyDensityForAudit(dampingTestEvent("m", "x"), now); dense {
		t.Fatal("关掉机器人循环检测后不该判目的")
	}

	r := dampingTestRuntime(BotConfig{}, nil)
	recordDampingSends(r, replyDampingDenseLimit, now.Add(-time.Minute))
	r.markReplyPurpose(dampingTestEvent("mark", "x"), true, now)
	r.resetBotReplyLoopUser("20002")
	if verdict := r.replyDampingJudge(dampingTestEvent("m", "接着说"), "接着说", false, now); verdict.Skip {
		t.Fatalf("解除后应清零：%+v", verdict)
	}
}

// 窗口外的回复不算密；降欲望过了保留期自动失效。
func TestReplyDampingExpires(t *testing.T) {
	r := dampingTestRuntime(BotConfig{}, nil)
	now := time.Now()
	recordDampingSends(r, replyDampingDenseLimit*2, now.Add(-replyDampingWindow-time.Minute))
	if _, dense := r.replyDensityForAudit(dampingTestEvent("m", "x"), now); dense {
		t.Fatal("窗口外的回复不该算密")
	}
	r.markReplyPurpose(dampingTestEvent("mark", "x"), true, now.Add(-replyDampingPurposelessRetention-time.Minute))
	if verdict := r.replyDampingJudge(dampingTestEvent("m", "接着说"), "接着说", false, now); verdict.Skip {
		t.Fatalf("过了保留期应自动解除：%+v", verdict)
	}
}

// 走完整的回复判断：降欲望期间没点名的接话记为 ignored_reply_damping，不进回复。
func TestPrepareMessageEventAppliesReplyDamping(t *testing.T) {
	r := dampingTestRuntime(BotConfig{}, nil)
	now := time.Now()
	recordDampingSends(r, replyDampingDenseLimit, now.Add(-time.Minute))
	r.markReplyPurpose(dampingTestEvent("mark", "x"), true, now)
	event := dampingTestEvent("follow", "你刚才说的那个呢")
	event.ToMe = true
	_, _, handled, outcome := r.prepareMessageEvent(context.Background(), event)
	if handled || outcome != "ignored_reply_damping" {
		t.Fatalf("handled=%v outcome=%q, want ignored_reply_damping", handled, outcome)
	}
	recent := r.Status().RecentEvents
	if len(recent) == 0 || !strings.Contains(recent[0].Reason, "没有明确目的") {
		t.Fatalf("事件原因应说明回复欲望衰减：%#v", recent)
	}
}

func meaninglessAuditVerdict(meaningless bool, reason string) string {
	return fmt.Sprintf(`{"send_confidence":0.9,"account_safe":true,"count_refusal":false,"reply_loop_automated_ai":false,"reply_loop_meaningless":%v,"reply_loop_purposeless":false,"reply_loop_confidence":0.97,"reply_loop_reason":%q}`, meaningless, reason)
}

// 「没内容」的空转不必等密度：这一来一回本身已经在转，密度只决定它转得多快。
// 对方是不是机器人同样不影响——counts() 只看有没有内容、有没有目的。
func TestMeaninglessLoopDampsWithoutDensity(t *testing.T) {
	provider := &sequenceLLMProvider{auditReplies: []string{meaninglessAuditVerdict(true, "双方都只在应付，已经重复好几轮")}}
	r := dampingTestRuntime(BotConfig{}, provider)
	now := time.Now()
	// 刻意停在问目的那道门以下：这一轮审核拿不到密度证据。
	recordDampingSends(r, replyDampingDenseLimit-1, now.Add(-time.Minute))
	event := botReplyLoopEvent(r, "empty", "20002", 0, now.Add(-30*time.Second), 10*time.Second, "Diana 嗯")
	if _, err := r.auditReplyBeforeSend(context.Background(), event, "Diana 嗯", "嗯呐", r.effectiveConfigForEvent(event), false); err != nil {
		t.Fatal(err)
	}
	if payload := requestTextContent(provider.requestsSnapshot()[0]); strings.Contains(payload, `"exchange_density":`) {
		t.Fatalf("这一轮本来就不该带密度：%s", payload)
	}
	if verdict := r.replyDampingJudge(dampingTestEvent("u", "接着说"), "接着说", false, now); !verdict.Skip {
		t.Fatalf("判到没内容就该降欲望，没点名的话应放掉：%+v", verdict)
	}
}

// 没问过目的就没有「有目的」这个结论，一条普通回复不能把刚判出来的空转一笔勾销。
func TestMeaninglessDampingNotClearedWithoutDensity(t *testing.T) {
	provider := &sequenceLLMProvider{auditReplies: []string{
		meaninglessAuditVerdict(true, "纯附和，已经重复好几轮"),
		meaninglessAuditVerdict(false, "这条有内容"),
	}}
	r := dampingTestRuntime(BotConfig{}, provider)
	now := time.Now()
	recordDampingSends(r, replyDampingDenseLimit-1, now.Add(-time.Minute))
	first := botReplyLoopEvent(r, "empty", "20002", 0, now.Add(-40*time.Second), 10*time.Second, "Diana 嗯")
	if _, err := r.auditReplyBeforeSend(context.Background(), first, "Diana 嗯", "嗯呐", r.effectiveConfigForEvent(first), false); err != nil {
		t.Fatal(err)
	}
	second := botReplyLoopEvent(r, "words", "20002", 1, now.Add(-20*time.Second), 10*time.Second, "Diana 明天几点的车")
	if _, err := r.auditReplyBeforeSend(context.Background(), second, "Diana 明天几点的车", "九点十分那班", r.effectiveConfigForEvent(second), false); err != nil {
		t.Fatal(err)
	}
	if verdict := r.replyDampingJudge(dampingTestEvent("u", "接着说"), "接着说", false, now); !verdict.Skip {
		t.Fatalf("没问过目的就不该解除降欲望：%+v", verdict)
	}
}
