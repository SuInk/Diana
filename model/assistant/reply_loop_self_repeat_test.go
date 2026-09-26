package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func selfRepeatVerdict(selfRepeat bool, confidence float64, reason string) string {
	return fmt.Sprintf(`{"send_confidence":0.9,"account_safe":true,"count_refusal":false,"reply_loop_automated_ai":false,"reply_loop_meaningless":false,"reply_loop_purposeless":false,"reply_loop_self_repeat":%v,"reply_loop_confidence":%.2f,"reply_loop_reason":%q}`, selfRepeat, confidence, reason)
}

// 复读自己单独成立：对方的话像真人、每条都有内容、密度也不异常，另外三项全落空，
// 只有「机器人自己把同一句晚安换着说了七遍」这一项判得出来。但它只在对方是机器人时
// 计数——对方是真人时复读是机器人自己的毛病，只丢那一条，不累计到暂停对方。
func TestSelfRepeatCountsOnItsOwn(t *testing.T) {
	for _, tc := range []struct {
		name     string
		decision botReplyLoopAIDecision
		want     bool
	}{
		{"self_repeat_toward_bot", botReplyLoopAIDecision{SelfRepeat: true, Confidence: 0.95, selfRepeatCounts: true}, true},
		{"self_repeat_toward_human", botReplyLoopAIDecision{SelfRepeat: true, Confidence: 0.95}, false},
		{"self_repeat_low_confidence", botReplyLoopAIDecision{SelfRepeat: true, Confidence: 0.75, selfRepeatCounts: true}, false},
		{"nothing_flagged", botReplyLoopAIDecision{Confidence: 0.99}, false},
		// 对方是 AI 依旧只记录不计数：两台 AI 正经下棋不该被停。
		{"automated_ai_alone", botReplyLoopAIDecision{AutomatedAIReply: true, Confidence: 0.99, selfRepeatCounts: true}, false},
		{"ai_and_self_repeat", botReplyLoopAIDecision{AutomatedAIReply: true, SelfRepeat: true, Confidence: 0.95, selfRepeatCounts: true}, true},
		// 没内容、没目的说的是整串来回，对真人照样计数。
		{"purposeless_toward_human", botReplyLoopAIDecision{PurposelessLoop: true, Confidence: 0.95}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.decision.counts(); got != tc.want {
				t.Fatalf("counts() = %v，want %v", got, tc.want)
			}
		})
	}
}

// 复读自己只丢当前这条：不发这一句，但不牵连这个账号后面的消息——降欲望是按账号
// 收口的，开了以后对方不点名就说不上话，新内容会跟着被连坐。
func TestSelfRepeatDropsOnlyThisReply(t *testing.T) {
	provider := &sequenceLLMProvider{auditReplies: []string{
		selfRepeatVerdict(true, 0.95, "同一句晚安换了措辞又说一遍，没有推进"),
	}}
	r := dampingTestRuntime(BotConfig{}, provider)
	now := time.Now()
	recordDampingSends(r, replyDampingDenseLimit-1, now.Add(-time.Minute))
	event := botReplyLoopEvent(r, "again", "20002", 0, now.Add(-30*time.Second), 10*time.Second, "Diana 晚安宝宝喵")
	_, err := r.auditReplyBeforeSend(context.Background(), event, "Diana 晚安宝宝喵", "嗯呐，满格那页见，睡吧喵。", r.effectiveConfigForEvent(event), false)
	if !errors.Is(err, errReplySelfRepeatDropped) {
		t.Fatalf("判到复读就该丢掉这一条，err=%v", err)
	}
	if payload := requestTextContent(provider.requestsSnapshot()[0]); strings.Contains(payload, `"exchange_density":`) {
		t.Fatalf("这一轮本来就不该带密度：%s", payload)
	}
	// 关键：没有开降欲望，对方下一条照常走到生成。
	if verdict := r.replyDampingJudge(dampingTestEvent("u", "接着说"), "接着说", false, now); verdict.Skip {
		t.Fatalf("复读只丢一条，不该把后面的消息一起挡掉：%+v", verdict)
	}
}

// 「没内容」仍然开降欲望：它说的是这一整串来回的状态，按账号收口说得通。
func TestMeaninglessStillDamps(t *testing.T) {
	provider := &sequenceLLMProvider{auditReplies: []string{
		meaninglessAuditVerdict(true, "纯附和，已经重复好几轮"),
	}}
	r := dampingTestRuntime(BotConfig{}, provider)
	now := time.Now()
	recordDampingSends(r, replyDampingDenseLimit-1, now.Add(-time.Minute))
	event := botReplyLoopEvent(r, "empty", "20002", 0, now.Add(-30*time.Second), 10*time.Second, "Diana 嗯")
	if _, err := r.auditReplyBeforeSend(context.Background(), event, "Diana 嗯", "嗯呐", r.effectiveConfigForEvent(event), false); err != nil {
		t.Fatal(err)
	}
	if verdict := r.replyDampingJudge(dampingTestEvent("u", "接着说"), "接着说", false, now); !verdict.Skip {
		t.Fatalf("判到没内容仍然要降欲望：%+v", verdict)
	}
}

// 审核提示词必须把判据说成「看意思不看字」，并明确把连续答同一个技术问题排除在外：
// 线上回放过的误报全是这一类——刷机答疑、计费讲解，用词高度重合但每条都在给新信息。
func TestSelfRepeatPromptSeparatesTopicContinuity(t *testing.T) {
	prompt := replyQualityPromptForConfig(BotConfig{})
	for _, want := range []string{
		"reply_loop_self_repeat",
		"只在带了 recent_bot_replies 时判",
		"字面不重复也可以是 true,判的是意思不是字",
		"连续回答同一个技术问题、逐条讲解、补充细节都是 false",
		"拿不准一律 false",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("审核提示词缺少 %q", want)
		}
	}
}

// 旧提示词没有这个字段时按 false 解析，升级期间整条审核结论不作废。
func TestSelfRepeatDefaultsFalseOnLegacyPayload(t *testing.T) {
	decision, ok := parseProactiveReplyQualityDecision(`{"send_confidence":0.9,"reply_loop_meaningless":false,"reply_loop_purposeless":false,"reply_loop_confidence":0.95}`)
	if !ok {
		t.Fatal("旧提示词的回答应当仍可解析")
	}
	if decision.ReplyLoopSelfRepeat {
		t.Fatal("缺字段时应当按 false 处理")
	}
}

// 停下来时写进事件的理由要说清是哪一种空转：复读自己被写成「没有明确目的」，
// 会让下一个排查的人照着错的方向找——这次的根因就是被 trigger_kind 误导浪费的。
func TestReplyDampingReasonNamesTheActualCause(t *testing.T) {
	for _, tc := range []struct {
		name     string
		decision botReplyLoopAIDecision
		want     string
	}{
		{"meaningless", botReplyLoopAIDecision{MeaninglessLoop: true}, replyDampingCauseMeaningless},
		{"purposeless", botReplyLoopAIDecision{PurposelessLoop: true}, replyDampingCausePurposeless},
		// 同时命中时挑更具体的那个。
		{"meaningless_beats_purposeless", botReplyLoopAIDecision{MeaninglessLoop: true, PurposelessLoop: true}, replyDampingCauseMeaningless},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := replyDampingCause(tc.decision); got != tc.want {
				t.Fatalf("理由 = %q，want %q", got, tc.want)
			}
		})
	}

	provider := &sequenceLLMProvider{auditReplies: []string{meaninglessAuditVerdict(true, "纯附和，已经重复好几轮")}}
	r := dampingTestRuntime(BotConfig{}, provider)
	now := time.Now()
	recordDampingSends(r, replyDampingDenseLimit-1, now.Add(-time.Minute))
	event := botReplyLoopEvent(r, "empty", "20002", 0, now.Add(-30*time.Second), 10*time.Second, "Diana 嗯")
	if _, err := r.auditReplyBeforeSend(context.Background(), event, "Diana 嗯", "嗯呐", r.effectiveConfigForEvent(event), false); err != nil {
		t.Fatal(err)
	}
	verdict := r.replyDampingJudge(dampingTestEvent("u", "接着说"), "接着说", false, now)
	if !verdict.Skip {
		t.Fatalf("应当放掉：%+v", verdict)
	}
	if !strings.Contains(verdict.Reason, replyDampingCauseMeaningless) {
		t.Fatalf("理由该说是没有实质内容，实际是：%s", verdict.Reason)
	}
}

// 降欲望按判定时间到期，不随对方继续发消息而续期。曾经改成「每放掉一条就续上」，
// 结果是这个账号只要不点名就永远说不上话，新内容跟着被连坐；真正要一直挡住的复读
// 现在逐条判、逐条丢，不靠这一层兜。
func TestReplyDampingExpiresOnItsOwnSchedule(t *testing.T) {
	r := dampingTestRuntime(BotConfig{}, nil)
	start := time.Now()
	recordDampingSends(r, replyDampingDenseLimit, start)
	r.markReplyPurpose(dampingTestEvent("mark", "x"), true, replyDampingCauseMeaningless, start)

	within := start.Add(replyDampingPurposelessRetention / 2)
	if verdict := r.replyDampingJudge(dampingTestEvent("mid", "接着说"), "接着说", false, within); !verdict.Skip {
		t.Fatalf("保留期内应当放掉：%+v", verdict)
	}
	// 对方一直在说也不续期：到点就解除。
	after := start.Add(replyDampingPurposelessRetention + time.Minute)
	if verdict := r.replyDampingJudge(dampingTestEvent("late", "接着说"), "接着说", false, after); verdict.Skip {
		t.Fatalf("保留期过了就该解除：%+v", verdict)
	}
}

// 收声提示必须读起来像人随口说的。后台词汇漏一个就打回，宁可不发也不退回模板。
func TestSanitizeReplyPauseHintRejectsSystemVoice(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{"natural", "那我先去忙点别的啦，晚点再聊喵", "那我先去忙点别的啦，晚点再聊喵"},
		{"quoted", "「先歇会儿，一会儿回来喵」", "先歇会儿，一会儿回来喵"},
		{"pause_word", "我先暂停一下喵", ""},
		{"account_word", "已暂停响应此账号", ""},
		{"loop_word", "为避免循环我先不说话了", ""},
		{"mention", "@某人 我先去忙啦", ""},
		{"cq_code", "[CQ:at,qq=20002] 先不聊了", ""},
		{"account_number", "账号 20002 先歇着", ""},
		{"too_long", strings.Repeat("先", 31), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeReplyPauseHint(tc.raw); got != tc.want {
				t.Fatalf("sanitize(%q) = %q，want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// 语义去重拿着对方这次的请求和引用判过「有新内容」，审核里的复读就不能再把这条
// 整条丢掉。线上那次是被点名追问「你把url给我就行」：去重判 keep（0.95），复读
// 却判了 true，追问的人什么也没收到。
func TestSelfRepeatYieldsToConfirmedNewContent(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		provider := &sequenceLLMProvider{auditReplies: []string{
			selfRepeatVerdict(true, 0.95, "又说了一遍拿不到链接"),
		}}
		r := dampingTestRuntime(BotConfig{}, provider)
		now := time.Now()
		event := botReplyLoopEvent(r, "again", "20002", 0, now.Add(-30*time.Second), 10*time.Second, "Diana 你把url给我就行")
		cfg := r.effectiveConfigForEvent(event)
		prepared := r.prepareReplyAudit(context.Background(), event, "Diana 你把url给我就行", "真翻不到它的 space 链接", cfg, false)
		prepared.newContentConfirmed = confirmed
		_, err := r.applyReplyAudit(context.Background(), event, cfg, prepared)
		if confirmed && err != nil {
			t.Fatalf("去重确认有新内容时不该按复读丢掉：%v", err)
		}
		if !confirmed && !errors.Is(err, errReplySelfRepeatDropped) {
			t.Fatalf("没有去重结论时复读照旧丢这一条：%v", err)
		}
	}
}

// 只有高置信的 keep 才算确认有新内容；drop、低置信、改写都不算。
func TestDeduplicateReplyVerdictReportsConfirmedKeep(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{`{"action":"keep","confidence":0.95}`, true},
		{`{"action":"keep","confidence":0.6}`, false},
		{`{"action":"drop","confidence":0.95}`, false},
	} {
		p := &auditOverrideProvider{text: tc.raw}
		r := topicTestRuntime(p)
		event := directedGroupMessage("m", "u", "你把url给我就行")
		g, release, err := r.lockSemanticReply(context.Background(), event)
		if err != nil {
			t.Fatal(err)
		}
		g.remember("你不能调用浏览器吗", "拿不到带 UID 的链接")
		_, kept, err := r.deduplicateReplyVerdict(context.Background(), event, "你把url给我就行", "候选", BotConfig{}, g, false)
		release()
		if err != nil || kept != tc.want {
			t.Fatalf("%s：kept=%v err=%v，want %v", tc.raw, kept, err, tc.want)
		}
	}
}

// 线上被「复读自己」记过数的，大多是真人在正常追问、机器人答得有点重复。复读是机器人
// 自己的毛病：对真人只丢那一条，记满三次也不暂停对方；对已标记的机器人照旧累计，
// 互道晚安停不下来的那种循环仍然拦得住。
func TestSelfRepeatSuppressesOnlyBots(t *testing.T) {
	selfRepeat := proactiveReplyQualityDecision{Confidence: 0.9, ReplyLoopSelfRepeat: true, ReplyLoopConfidence: 0.95, ReplyLoopSelfRepeatConfidence: 0.95, ReplyLoopPurposelessConfidence: 0.95}
	for _, tc := range []struct {
		name       string
		marked     []string
		wantPaused bool
	}{
		{"human", nil, false},
		{"marked_bot", []string{"20002"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := dampingTestRuntime(BotConfig{MarkedBotIDs: tc.marked}, nil)
			now := time.Now()
			var last MessageEvent
			for i := 0; i < botReplyLoopThreshold; i++ {
				last = botReplyLoopEvent(r, tc.name, "20002", i, now.Add(time.Duration(i-botReplyLoopThreshold)*time.Minute), 10*time.Second, "晚安")
				err := r.applyReplyLoopVerdict(context.Background(), last, botReplyLoopCandidate{TriggerKind: "quote"}, selfRepeat, true)
				if tc.wantPaused && i == botReplyLoopThreshold-1 {
					if !errors.Is(err, errReplyLoopDetected) {
						t.Fatalf("第 %d 次复读应当触发暂停，err=%v", i+1, err)
					}
					continue
				}
				if !errors.Is(err, errReplySelfRepeatDropped) {
					t.Fatalf("第 %d 次复读应当只丢这一条，err=%v", i+1, err)
				}
			}
			if _, paused := r.activeReplySuppression(last, time.Now()); paused != tc.wantPaused {
				t.Fatalf("暂停 = %v，want %v", paused, tc.wantPaused)
			}
		})
	}
}
