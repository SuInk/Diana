package assistant

import (
	"math"
	"testing"
	"time"
)

func int64Ptr(v int64) *int64 { return &v }

// 公式和拟合脚本算出来的概率要一致，换权重时这条会提醒顺手核对。
func TestEstimateChatInProbabilityMatchesFit(t *testing.T) {
	got := estimateChatInProbability(chatInLikelihoodInput{
		Mention: true, Question: true, SelfRatio: 0.25, RecentCount: 8,
		SecondsSinceBot: 90, Pending: 3, Engaged: true, Length: 12,
	})
	if math.Abs(got-0.4952) > 0.001 {
		t.Fatalf("probability = %.4f，拟合脚本给的是 0.4952", got)
	}
	// @ 了别人、只有图片时概率要明显更低。
	low := estimateChatInProbability(chatInLikelihoodInput{AtOther: true, Placeholder: true, SecondsSinceBot: 3600, RecentCount: 1, Pending: 1})
	if low > 0.02 {
		t.Fatalf("@ 别人的纯图片消息概率 %.3f，太高", low)
	}
}

func TestChatInLikelihoodFromPayload(t *testing.T) {
	payload := proactiveReplyPayload{
		CurrentText: "diana 你怎么看？",
		BotAliases:  []string{"Diana"},
		RecentMessages: []proactiveReplyHistoryItem{
			{IsBot: true, AgeSeconds: int64Ptr(30), Addressing: messageAddressing{ReplyUserID: "10001"}},
			{UserID: "10001", AgeSeconds: int64Ptr(50)},
			{UserID: "10002", AgeSeconds: int64Ptr(400)},
		},
		LastBotAddressedCurrentSender: true,
	}
	in := chatInLikelihoodFromPayload(payload, "10001")
	if !in.Mention || !in.Question || in.AtOther || in.Placeholder {
		t.Fatalf("文字特征不对：%+v", in)
	}
	if !in.Engaged || !in.Follow || !in.RightAfter {
		t.Fatalf("机器人刚回过这个人，往来特征不对：%+v", in)
	}
	// 5 分钟窗口里有机器人一条、群友一条，加上当前这条一共三条。
	if in.RecentCount != 3 || math.Abs(in.SelfRatio-1.0/3) > 1e-9 || in.SecondsSinceBot != 30 || in.Pending != 1 {
		t.Fatalf("近期统计不对：%+v", in)
	}
	empty := chatInLikelihoodFromPayload(proactiveReplyPayload{Addressing: messageAddressing{MentionsOther: true}}, "10001")
	if !empty.Placeholder || !empty.AtOther || empty.SecondsSinceBot != chatInGateNoBotSeconds {
		t.Fatalf("没有上下文时的缺省值不对：%+v", empty)
	}
}

func TestChatInGateThresholdFollowsBudget(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	gate := &chatInGate{}
	// 每轮相隔超过 40 秒，各记一次需求。
	for i := 0; i < 30; i++ {
		gate.recordDemand(float64(i%10)/10, now.Add(time.Duration(i)*time.Minute))
		gate.roundOpen = time.Time{}
	}
	at := now.Add(30 * time.Minute)
	if d := gate.evaluate(0.05, 1, at); !d.Allowed || d.Threshold != 0 {
		t.Fatalf("比例 1 不该拦：%+v", d)
	}
	loose := gate.evaluate(0.8, 0.5, at)
	if !loose.Allowed {
		t.Fatalf("还没插过话，0.8 的概率应当放行：%+v", loose)
	}
	// 实际插话数超过目标后阈值收紧，同样的概率不再放行。
	for i := 0; i < 20; i++ {
		gate.replies = append(gate.replies, at.Add(-time.Duration(i)*time.Second))
	}
	tight := gate.evaluate(0.8, 0.5, at)
	if tight.Allowed || tight.Threshold <= loose.Threshold {
		t.Fatalf("插多了阈值没有收紧：before=%+v after=%+v", loose, tight)
	}
	// 一小时以前的需求和插话都过期。
	later := at.Add(2 * time.Hour)
	if d := gate.evaluate(0.5, 0.5, later); d.Expected != 0 || d.Actual != 0 {
		t.Fatalf("窗口外的记录没清掉：%+v", d)
	}
}

func TestChatInGateRoundsCountOnce(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	gate := &chatInGate{}
	gate.recordDemand(0.2, now)
	gate.recordDemand(0.6, now.Add(10*time.Second))
	if len(gate.scores) != 1 || gate.scores[0].probability != 0.6 {
		t.Fatalf("同一轮内只该计一次、取最新概率：%+v", gate.scores)
	}
	gate.recordDemand(0.3, now.Add(time.Minute))
	if len(gate.scores) != 2 {
		t.Fatalf("超过 40 秒应开新一轮：%+v", gate.scores)
	}
}

func TestChatInGateStaticThresholdInterpolates(t *testing.T) {
	if got := chatInGateThreshold(nil, 0, 0.25); math.Abs(got-(0.653+0.565)/2) > 1e-9 {
		t.Fatalf("静态表插值 = %.4f", got)
	}
	if chatInGateThreshold(nil, 0, 0) != 1 || chatInGateThreshold(nil, 0, 1) != 0 {
		t.Fatal("比例 0 / 1 的边界不对")
	}
}

func TestChatInGateCheckRecordsRepliesPerSession(t *testing.T) {
	r := &Runtime{}
	event := MessageEvent{Platform: "onebot-v11", SelfID: "20001", GroupID: "30001", UserID: "10001", MessageType: "group"}
	other := event
	other.GroupID = "30002"
	now := time.Unix(1_800_000_000, 0)
	payload := proactiveReplyPayload{CurrentText: "有人知道这个吗？"}
	if d := r.chatInGateCheck(event, payload, "off", now); d.Allowed {
		t.Fatalf("off 档不该放行：%+v", d)
	}
	if d := r.chatInGateCheck(event, payload, "always", now.Add(time.Minute)); !d.Allowed {
		t.Fatalf("always 档应放行：%+v", d)
	}
	r.recordChatInGateReply(event, now.Add(time.Minute))
	r.recordChatInGateReply(other, now.Add(time.Minute))
	if got := len(r.chatInGates[chatInCooldownKey(event)].replies); got != 1 {
		t.Fatalf("本群插话数 = %d", got)
	}
	if r.chatInGates[chatInCooldownKey(other)] != nil {
		t.Fatal("没评分过的群不该凭空建门控")
	}
}
