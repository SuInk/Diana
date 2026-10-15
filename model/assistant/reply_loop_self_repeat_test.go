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
	return fmt.Sprintf(`{"send_confidence":0.9,"account_safe":true,"count_refusal":false,"reply_loop_meaningless":false,"reply_loop_purposeless":false,"reply_loop_self_repeat":%v,"reply_loop_confidence":%.2f,"reply_loop_reason":%q}`, selfRepeat, confidence, reason)
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

// 复读自己只丢当前这条：不发这一句，但不牵连这个账号后面的消息。
func TestSelfRepeatDropsOnlyThisReply(t *testing.T) {
	provider := &sequenceLLMProvider{auditReplies: []string{
		selfRepeatVerdict(true, 0.95, "同一句晚安换了措辞又说一遍，没有推进"),
	}}
	r := densityTestRuntime(BotConfig{}, provider)
	now := time.Now()
	recordDenseSends(r, replyDensityDenseLimit-1, now.Add(-time.Minute))
	event := botReplyLoopEvent(r, "again", "20002", 0, now.Add(-30*time.Second), 10*time.Second, "Diana 晚安宝宝喵")
	_, err := r.auditReplyBeforeSend(context.Background(), event, "Diana 晚安宝宝喵", "嗯呐，满格那页见，睡吧喵。", r.effectiveConfigForEvent(event), false)
	if !errors.Is(err, errReplySelfRepeatDropped) {
		t.Fatalf("判到复读就该丢掉这一条，err=%v", err)
	}
	if payload := requestTextContent(provider.requestsSnapshot()[0]); strings.Contains(payload, `"exchange_density":`) {
		t.Fatalf("这一轮本来就不该带密度：%s", payload)
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
