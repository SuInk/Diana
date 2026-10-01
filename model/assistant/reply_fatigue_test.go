// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// fatigueGateProvider 只回答触发阶段那一问，其余调用一律给空 JSON。
type fatigueGateProvider struct {
	mu      sync.Mutex
	novelty string
	purpose string
	err     error
	calls   int
	last    llm.GenerateRequest
}

func (p *fatigueGateProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	for _, message := range req.Messages {
		if strings.Contains(message.Content, "在原地打转") {
			p.mu.Lock()
			p.calls++
			p.last = req
			p.mu.Unlock()
			if p.err != nil {
				return nil, p.err
			}
			novelty := p.novelty
			if novelty == "" {
				novelty = "0.1"
			}
			return &llm.GenerateResponse{Text: `{"exchange_novelty":` + novelty + `,"exchange_purpose":` + p.purpose + `}`}, nil
		}
	}
	return &llm.GenerateResponse{Text: "{}"}, nil
}

func (p *fatigueGateProvider) gateCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func chargeFatigue(r *Runtime, event MessageEvent, amount float64, at time.Time) {
	r.recordReplyFatigueSend(event, replyFatigueCharge{Scored: true, Amount: amount}, at)
}

// 篇幅加权是这套规则分得开人和机器人的原因：长篇无目的接梗满额累加，
// 「嘉然摸摸」这种短句几乎不累，有目的的一轮不累。
func TestReplyFatigueAmountWeighsLength(t *testing.T) {
	long := "锦旗必须直接绣「赛博防爆气囊·拦聊先锋」八个金字，就是那半块草莓蛋糕的罚金还是照扣"
	if got := replyFatigueAmount(long, 0, 0); math.Abs(got-replyFatigueCost) > 1e-9 {
		t.Fatalf("长篇无新意无目的应满额 %.2f，得到 %.3f", replyFatigueCost, got)
	}
	if got := replyFatigueAmount("[CQ:at,qq=42] 嘉然摸摸", 0, 0); got > 0.1 {
		t.Fatalf("短句闲聊不该怎么累，得到 %.3f", got)
	}
	if got := replyFatigueAmount(long, 0.1, 0.9); got > 0.07 {
		t.Fatalf("有目的的一轮不该累，得到 %.3f", got)
	}
}

// 快慢两档同时累加、各自消退：快档 10 分钟退掉，慢档几小时后还在，
// 断断续续刷屏的人不会隔一会儿就回到满血。
func TestReplyFatigueDecaysOverTime(t *testing.T) {
	r := densityTestRuntime(BotConfig{}, nil)
	event := densityTestEvent("m1", "x")
	now := time.Now()
	chargeFatigue(r, event, 0.6, now)
	chargeFatigue(r, event, 0.6, now)
	want := 1.2 + replyFatigueSlowWeight*1.2
	if level := r.replyFatigueLevel(event, now); math.Abs(level-want) > 1e-9 {
		t.Fatalf("两轮满额应为 快1.2 + 0.2×慢1.2 = %.2f，得到 %.3f", want, level)
	}
	later := now.Add(replyFatigueDecay)
	want = 1.2/math.E + replyFatigueSlowWeight*1.2*math.Exp(-float64(replyFatigueDecay)/float64(replyFatigueSlowDecay))
	if level := r.replyFatigueLevel(event, later); math.Abs(level-want) > 1e-6 {
		t.Fatalf("过 10 分钟快档应衰减到 1/e、慢档几乎不动，想要 %.3f，得到 %.3f", want, level)
	}
	// 慢档一小时后还剩八成多：快档早就退光了，它还挂着。
	fast, slow := decayReplyFatigue(r.replyFatigue.byKey[botReplyLoopKey(event, event.UserID)], now.Add(time.Hour))
	if fast > 0.01 || slow < 1.0 {
		t.Fatalf("一小时后快档应退光、慢档应还在：快 %.3f 慢 %.3f", fast, slow)
	}
}

// 疲劳没攒满时触发阶段不调模型，平时零额外开销。
func TestReplyFatigueGateSkipsModelBelowLimit(t *testing.T) {
	provider := &fatigueGateProvider{purpose: "0.1"}
	r := densityTestRuntime(BotConfig{}, provider)
	event := densityTestEvent("m1", "Diana 再接一句")
	chargeFatigue(r, event, 0.8, time.Now()) // 总疲劳 0.8 + 0.2×0.8 = 0.96
	if blocked, _ := r.replyFatigueBlocks(context.Background(), event, "Diana 再接一句"); blocked {
		t.Fatal("疲劳没攒满不该拦")
	}
	if provider.gateCalls() != 0 {
		t.Fatalf("疲劳没攒满不该调模型，调了 %d 次", provider.gateCalls())
	}
}

// 攒满后：没新东西也没目的的不回；有新东西的闲聊、提问照回；判断失败放行；主人永远不拦。
func TestReplyFatigueGateAtLimit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		novelty string
		purpose string
		err     error
		owner   bool
		blocked bool
	}{
		{name: "车轱辘话", novelty: "0.1", purpose: "0.1", blocked: true},
		{name: "有新东西的闲聊", novelty: "0.8", purpose: "0.1"},
		{name: "在提问", novelty: "0.2", purpose: "0.9"},
		{name: "判断失败", err: errors.New("boom")},
		{name: "主人", purpose: "0.1", owner: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &fatigueGateProvider{novelty: tc.novelty, purpose: tc.purpose, err: tc.err}
			cfg := BotConfig{}
			if tc.owner {
				cfg.OwnerID = "20002"
			}
			r := densityTestRuntime(cfg, provider)
			event := densityTestEvent("m1", "Diana 锦旗绣八个金字")
			chargeFatigue(r, event, 1.2, time.Now())
			blocked, reason := r.replyFatigueBlocks(context.Background(), event, "Diana 锦旗绣八个金字")
			if blocked != tc.blocked {
				t.Fatalf("blocked=%v，想要 %v（%s）", blocked, tc.blocked, reason)
			}
			if blocked && !strings.Contains(reason, "疲劳") {
				t.Fatalf("拦下的原因要说清是疲劳：%q", reason)
			}
		})
	}
}

// @ 本机的消息也走这道闸：疲劳攒满、这句又没目的，触发阶段就不回，不生成回复。
func TestReplyFatigueBlocksDirectMentionAtTrigger(t *testing.T) {
	provider := &fatigueGateProvider{purpose: "0.05"}
	r := densityTestRuntime(BotConfig{}, provider)
	event := densityTestEvent("follow", "Diana 法警连证物袋都备好了")
	event.ToMe = true
	chargeFatigue(r, event, 1.5, time.Now())
	_, _, handled, outcome := r.prepareMessageEvent(context.Background(), event)
	if handled || outcome != "ignored_reply_fatigue" {
		t.Fatalf("handled=%v outcome=%q，想要 ignored_reply_fatigue", handled, outcome)
	}
}

// 判断攒着：最近几轮审核打的目的分攒得够高，疲劳满了也照回，不再单独问。
// 一直在追问、纠正、请你做事的人，不会因为某一句被单独判低就被晾着。
func TestReplyFatigueEngagedPartnerSkipsGate(t *testing.T) {
	provider := &fatigueGateProvider{purpose: "0.1"}
	r := densityTestRuntime(BotConfig{}, provider)
	event := densityTestEvent("m1", "Diana 要不你再问我吧，先把你的答案带上")
	now := time.Now()
	for i := 0; i < 3; i++ {
		r.recordReplyFatigueSend(event, replyFatigueCharge{Scored: true, Amount: 0.5, Purpose: 0.85}, now)
	}
	level, engage := r.replyFatigueSnapshot(event, now)
	if level < replyFatigueLimit || engage < replyFatigueEngageGate {
		t.Fatalf("疲劳 %.2f 应已攒满、目的 %.2f 应攒够", level, engage)
	}
	if blocked, reason := r.replyFatigueBlocks(context.Background(), event, "Diana 要不你再问我吧，先把你的答案带上"); blocked {
		t.Fatalf("攒着的目的够高不该拦：%s", reason)
	}
	if provider.gateCalls() != 0 {
		t.Fatalf("攒着的目的够高不该再调模型，调了 %d 次", provider.gateCalls())
	}

	// 攒着的目的随时间消退：隔了一个多小时，又回到逐句判断。
	if _, engage := r.replyFatigueSnapshot(event, now.Add(time.Hour)); engage >= replyFatigueEngageGate {
		t.Fatalf("一小时后攒着的目的应已消退，得到 %.2f", engage)
	}
}

// 接梗的来回目的分低，攒不起来，疲劳满了照样逐句判断。
func TestReplyFatigueBanterDoesNotAccumulatePurpose(t *testing.T) {
	provider := &fatigueGateProvider{purpose: "0.1"}
	r := densityTestRuntime(BotConfig{}, provider)
	event := densityTestEvent("m1", "Diana 锦旗绣八个金字")
	now := time.Now()
	for i := 0; i < 3; i++ {
		r.recordReplyFatigueSend(event, replyFatigueCharge{Scored: true, Amount: 0.5, Purpose: 0.2}, now)
	}
	if blocked, _ := r.replyFatigueBlocks(context.Background(), event, "Diana 锦旗绣八个金字"); !blocked {
		t.Fatal("接梗攒不起目的，车轱辘话应当拦下")
	}
}

// 增量为 0 的一轮（对方只发了图、或这一轮满分）也要把目的攒进去。
func TestReplyFatigueRecordsPurposeWithoutAmount(t *testing.T) {
	r := densityTestRuntime(BotConfig{}, nil)
	event := densityTestEvent("m1", "[图片]")
	now := time.Now()
	r.recordReplyFatigueSend(event, replyFatigueCharge{Scored: true, Amount: 0, Purpose: 0.9}, now)
	if _, engage := r.replyFatigueSnapshot(event, now); math.Abs(engage-replyFatigueEngageWeight*0.9) > 1e-9 {
		t.Fatalf("目的应攒到 %.2f，得到 %.3f", replyFatigueEngageWeight*0.9, engage)
	}
}

// 门控和意图识别看同一份按时间排的对话：机器人拆成几条发的回复都在，先后看得出来。
func TestReplyFatigueGateSeesOrderedTranscript(t *testing.T) {
	provider := &fatigueGateProvider{purpose: "0.1"}
	r := densityTestRuntime(BotConfig{}, provider)
	event := densityTestEvent("m9", "Diana 要不你再问我吧")
	event.SenderName = "群友"
	base := time.Now().Unix()
	event.Time = base
	history := []MessageEvent{
		{Kind: EventKindGroup, GroupID: "123456", UserID: "42", MessageID: "b1", Time: base - 50, RawMessage: "那我出题：火在架构里是什么"},
		{Kind: EventKindGroup, GroupID: "123456", UserID: "20002", SenderName: "群友", MessageID: "u1", Time: base - 40, RawMessage: "你说的都不在点子上"},
		{Kind: EventKindGroup, GroupID: "123456", UserID: "42", MessageID: "b2", Time: base - 30, RawMessage: "确实是我没想清楚"},
		{Kind: EventKindGroup, GroupID: "123456", UserID: "42", MessageID: "b3", Time: base - 29, RawMessage: "你这种先把边界敲死的思路才对"},
	}
	for i := range history {
		history[i].Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": history[i].RawMessage}}}
	}
	event.replyHistory, event.replyHistoryLoaded = history, true
	r.recordReplyFatigueSend(event, replyFatigueCharge{Scored: true, Amount: 1.2}, time.Now())

	if blocked, _ := r.replyFatigueBlocks(context.Background(), event, "Diana 要不你再问我吧"); !blocked {
		t.Fatal("测试前提：目的 0.1 应当拦下")
	}
	provider.mu.Lock()
	content := requestTextContent(provider.last)
	provider.mu.Unlock()
	order := []string{"那我出题", "你说的都不在点子上", "确实是我没想清楚", "你这种先把边界敲死的思路才对", "【当前消息】"}
	// 系统提示词里也提到【当前消息】，从对话稿开头往后找。
	start := strings.Index(content, "对话按时间从早到晚：")
	if start < 0 {
		t.Fatalf("门控应收到对话稿：\n%s", content)
	}
	content = content[start:]
	last := -1
	for _, want := range order {
		at := strings.Index(content, want)
		if at <= last {
			t.Fatalf("对话稿应按时间排出 %q：\n%s", want, content)
		}
		last = at
	}
}

// memoryReplyFatigueStore 是测试用的落盘存储。
type memoryReplyFatigueStore struct {
	mu      sync.Mutex
	records []ReplyFatigueRecord
	saves   int
}

func (s *memoryReplyFatigueStore) LoadReplyFatigue(context.Context) ([]ReplyFatigueRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ReplyFatigueRecord(nil), s.records...), nil
}

func (s *memoryReplyFatigueStore) SaveReplyFatigue(_ context.Context, records []ReplyFatigueRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append([]ReplyFatigueRecord(nil), records...)
	s.saves++
	return nil
}

// 疲劳落盘：重启以后接着算，不会每次部署都把刷了一天的机器人清零。
func TestReplyFatigueSurvivesRestart(t *testing.T) {
	store := &memoryReplyFatigueStore{}
	r := densityTestRuntime(BotConfig{}, nil)
	event := densityTestEvent("m1", "Diana 锦旗绣八个金字")
	now := time.Now()
	r.recordReplyFatigueSend(event, replyFatigueCharge{Scored: true, Amount: 1.2, Purpose: 0.8}, now)
	r.persistReplyFatigue(context.Background(), store)
	if store.saves != 1 || len(store.records) != 1 {
		t.Fatalf("应写下一条记录：saves=%d records=%#v", store.saves, store.records)
	}
	// 没有变化就不重复写。
	r.persistReplyFatigue(context.Background(), store)
	if store.saves != 1 {
		t.Fatalf("没有变化不该再写，写了 %d 次", store.saves)
	}

	restarted := densityTestRuntime(BotConfig{}, nil)
	records, _ := store.LoadReplyFatigue(context.Background())
	restarted.restoreReplyFatigue(records, now)
	wantLevel, wantEngage := r.replyFatigueSnapshot(event, now)
	level, engage := restarted.replyFatigueSnapshot(event, now)
	if math.Abs(level-wantLevel) > 1e-9 || math.Abs(engage-wantEngage) > 1e-9 {
		t.Fatalf("重启后应接着算：疲劳 %.3f/%.3f，目的 %.3f/%.3f", level, wantLevel, engage, wantEngage)
	}

	// 已经消退干净的不装回来；解除暂停清掉的人也要落盘。
	stale := densityTestRuntime(BotConfig{}, nil)
	stale.restoreReplyFatigue(records, now.Add(48*time.Hour))
	if len(stale.replyFatigue.byKey) != 0 {
		t.Fatalf("消退干净的记录不该装回：%#v", stale.replyFatigue.byKey)
	}
	restarted.resetReplyFatigueUser(event.UserID)
	restarted.persistReplyFatigue(context.Background(), store)
	if len(store.records) != 0 {
		t.Fatalf("清掉的人应从落盘里删掉：%#v", store.records)
	}
}

// 新意和目的由发送前审核顺带打分：只有正常生成的回复才问，发出去以后才累加。
func TestReplyAuditScoresExchangeForFatigue(t *testing.T) {
	provider := &sequenceLLMProvider{auditReplies: []string{
		`{"send_confidence":0.95,"exchange_novelty":0.1,"exchange_purpose":0.0}`,
		`{"send_confidence":0.95}`,
	}}
	r := densityTestRuntime(BotConfig{}, provider)
	event := densityTestEvent("m1", "锦旗必须直接绣「赛博防爆气囊·拦聊先锋」八个金字，那半块草莓蛋糕的罚金还是照扣")
	input := historyPlainText(event)
	cfg := r.effectiveConfigForEvent(event)

	intent, err := r.auditReplyBeforeSend(withReplyFatigueAudit(context.Background()), event, input, "哈哈哈哈草，一码归一码", cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	if payload := requestTextContent(provider.requestsSnapshot()[0]); !strings.Contains(payload, `"exchange_check":true`) {
		t.Fatalf("正常回复的审核应带 exchange_check：%s", payload)
	}
	if !intent.fatigue.Scored || math.Abs(intent.fatigue.Amount-0.54) > 1e-9 {
		t.Fatalf("增量应为 0.6×1×(1−0.1)=0.54，得到 %+v", intent.fatigue)
	}
	if level := r.replyFatigueLevel(event, time.Now()); level != 0 {
		t.Fatalf("还没发出去就不该累加，得到 %.3f", level)
	}
	r.applyReplyControlAfterSend(context.Background(), event, "哈哈哈哈草，一码归一码", intent)
	if level := r.replyFatigueLevel(event, time.Now()); math.Abs(level-0.54*(1+replyFatigueSlowWeight)) > 0.01 {
		t.Fatalf("发出去以后快慢两档各加 0.54，总疲劳应为 %.3f，得到 %.3f", 0.54*(1+replyFatigueSlowWeight), level)
	}

	// 插件直发这类没标记的审核不问、不记。
	intent, err = r.auditReplyBeforeSend(context.Background(), event, input, "解析结果", cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	if payload := requestTextContent(provider.requestsSnapshot()[1]); strings.Contains(payload, `"exchange_check":true`) {
		t.Fatalf("没标记的审核不该问疲劳：%s", payload)
	}
	if intent.fatigue.Scored {
		t.Fatal("没打分就不该记疲劳")
	}
}

// 审核只答了一项时当没打分：宁可多回一句。
func TestReplyAuditIgnoresPartialExchangeScore(t *testing.T) {
	decision, ok := parseProactiveReplyQualityDecision(`{"send_confidence":0.9,"exchange_novelty":0.2}`)
	if !ok || decision.ExchangeScored {
		t.Fatalf("缺 exchange_purpose 应当没打分：%+v", decision)
	}
	decision, ok = parseProactiveReplyQualityDecision(`{"send_confidence":0.9,"exchange_novelty":0.2,"exchange_purpose":1.4}`)
	if !ok || decision.ExchangeScored {
		t.Fatalf("越界的分数应当作废：%+v", decision)
	}
}
