package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func floatPtr(v float64) *float64 { return &v }

// 可回答不再是单独一道闸：它拦的两类消息分别由相关度和闲聊的评分标准接住。
// 旧配置里存着的 answerability_level 读进来也不再起作用，相关度或闲聊达标就放行。
func TestAnswerabilityNoLongerGatesReplies(t *testing.T) {
	var p ParticipationPreferences
	if err := json.Unmarshal([]byte(`{"relevance_level":"medium","chat_level":"medium","answerability_level":"minimal"}`), &p); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		rel, chat  float64
		cool, want bool
	}{
		{0.8, 0.1, true, true}, {0.1, 0.8, true, true}, {0.1, 0.8, false, false}, {0.49, 0.49, true, false},
	} {
		if got, _ := p.ratingsAllow(testRatings(tc.rel, tc.chat), tc.cool); got != tc.want {
			t.Fatalf("%+v got %v", tc, got)
		}
	}
	// 提示词里不能再让模型评可回答，也不能再写它是程序判断的一部分。
	prompt := p.prompt()
	if strings.Contains(prompt, "answerability") || strings.Contains(prompt, "可回答") {
		t.Fatalf("提示词里还有可回答：%s", prompt)
	}
	// 它原来拦的两类消息要分别出现在相关度和闲聊的标准里。
	for _, want := range []string{"问某个具体群友本人才知道的事", "原样复读别人刚说过的话", "对一个无法核实的说法补充听起来内行、其实没有依据的理由"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("评分标准缺少原可回答的判据：%s", want)
		}
	}
}
func testRatings(r, c float64) participationRatings {
	return participationRatings{participationRating{&r, "相关度原因"}, participationRating{&c, "闲聊原因"}}
}
func TestParticipationRatingsORAndOff(t *testing.T) {
	for _, tc := range []struct {
		a, b       string
		r, c       float64
		cool, want bool
	}{
		{"medium", "medium", 0.8, 0.1, true, true}, {"medium", "medium", 0.1, 0.8, true, true},
		{"medium", "medium", 0.49, 0.49, true, false}, {"off", "medium", 1, 0.1, true, false},
		{"medium", "off", 0.1, 1, true, false}, {"off", "off", 1, 1, true, false},
		{"always", "off", 0.01, 0.01, false, true}, {"off", "always", 0.01, 0.01, true, true},
		{"off", "always", 0.01, 0.01, false, false}, {"always", "always", 0, 0, true, false},
		{"high", "medium", 0.3, 0, true, true}, {"low", "off", 0.69, 1, true, false},
	} {
		p := ParticipationPreferences{RelevanceLevel: tc.a, ChatLevel: tc.b}
		got, _ := p.ratingsAllow(testRatings(tc.r, tc.c), tc.cool)
		if got != tc.want {
			t.Fatalf("%+v got %v", tc, got)
		}
	}
}
func TestParticipationRatingsProtocol(t *testing.T) {
	good := `{"relevance":{"score":0.25,"reason":"未直接对机器人说话"},"chat_in":{"score":0.80,"reason":"自然接梗"}}`
	if _, err := parseParticipationRatings(good); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`true`, `0.8`, `{"score":0.8,"reason":"x"}`, strings.Replace(good, "0.25", "1.25", 1), strings.Replace(good, "0.25", `"0.25"`, 1)} {
		if _, err := parseParticipationRatings(raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

// 线上 14 天里 139/1780 条评分因为这些形状被整条丢弃，全部按可解析处理。
func TestParticipationRatingsLenientParsing(t *testing.T) {
	body := `"relevance":{"score":0.31,"reason":"群友在延续话题"},"chat_in":{"score":0.82,"reason":"顺着当前玩笑接一句很合适"}`
	good := "{" + body + "}"
	for _, tc := range []struct {
		name, raw string
		want      float64
	}{
		{"clean", good, 0.82},
		{"trailing_devanagari", good + "િ", 0.82},
		{"trailing_brace_and_words", good + "} krwar", 0.82},
		{"trailing_prose", good + "\n以上是我的评分。", 0.82},
		{"leading_prose", "好的，评分如下：\n" + good, 0.82},
		{"code_fence", "```json\n" + good + "\n```", 0.82},
		{"code_fence_bare", "```\n" + good + "\n```", 0.82},
		{"truncated_outer_brace", "{" + body, 0.82},
		// 中转把最后一个右花括号顶成了乱码：对象少一个闭合，尾巴上多出几个字符。
		{"junk_replaces_outer_brace", "{" + body + "\u0ac7\u0aa3", 0.82},
		{"junk_replaces_outer_brace_cjk", "{" + body + "】【。", 0.82},
		// 零宽字符夹在最后两个右花括号之间：对象是配平的，但 JSON 解析当场报错。
		{"zero_width_between_braces", "{" + body + "\u200c}", 0.82},
		{"zero_width_inside_object", "{" + body[:len(body)-1] + "\u200c" + body[len(body)-1:] + "}", 0.82},
		{"truncated_outer_brace_with_fence", "```json\n{" + body, 0.82},
		{"unknown_fields", `{"should_reply":true,"category":"chat_in",` + body + `,"note":{"a":1}}`, 0.82},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ratings, err := parseParticipationRatings(tc.raw)
			if err != nil {
				t.Fatalf("rejected %q: %v", tc.raw, err)
			}
			if *ratings.ChatIn.Score != tc.want || *ratings.Relevance.Score != 0.31 {
				t.Fatalf("ratings=%+v", ratings)
			}
			if ratings.ChatIn.Reason != "顺着当前玩笑接一句很合适" {
				t.Fatalf("reason=%q", ratings.ChatIn.Reason)
			}
		})
	}
	for _, tc := range []struct{ name, raw string }{
		{"empty", ""},
		{"no_object", "抱歉，我无法评分。"},
		{"truncated_mid_string", `{"relevance":{"score":0.31,"reason":"群友在延续`},
		{"truncated_before_chat_in", `{"relevance":{"score":0.31,"reason":"群友"}`},
		{"missing_reason", `{"relevance":{"score":0.31,"reason":" "},"chat_in":{"score":0.8,"reason":"接梗"}}`},
		{"score_out_of_range", `{"relevance":{"score":1.31,"reason":"高"},"answerability":{"score":0.6,"reason":"可以"},"chat_in":{"score":0.8,"reason":"接梗"}}`},
		{"first_object_is_not_ratings", `{"note":"thinking"} ` + good},
	} {
		t.Run("reject_"+tc.name, func(t *testing.T) {
			if _, err := parseParticipationRatings(tc.raw); err == nil {
				t.Fatalf("accepted %q", tc.raw)
			}
		})
	}
}

func TestParticipationBotShareBlocksChatIn(t *testing.T) {
	for _, tc := range []struct {
		name       string
		bot, total int
		level      string
		want       bool
	}{
		{"quiet_bot", 4, 20, "medium", false},
		{"at_threshold", 5, 20, "medium", true},
		{"just_below_threshold", 4, 17, "medium", false},
		{"dominating_small_group", 12, 20, "low", true},
		{"always_never_blocked", 18, 20, "always", false},
		{"no_history", 0, 0, "medium", false},
		{"bot_silent", 0, 20, "medium", false},
		// 安静群里一来一回的占比天然很高，机器人只说了两句就不算刷屏。
		{"quiet_back_and_forth", 2, 4, "medium", false},
		{"three_in_a_row", 3, 4, "medium", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := participationBotShareBlocks(tc.bot, tc.total, tc.level); got != tc.want {
				t.Fatalf("bot=%d total=%d level=%s got=%v want=%v", tc.bot, tc.total, tc.level, got, tc.want)
			}
		})
	}
	messages := make([]proactiveReplyHistoryItem, 0, 40)
	for i := 0; i < 40; i++ {
		messages = append(messages, proactiveReplyHistoryItem{IsBot: i < 10})
	}
	if bot, total := proactiveReplyBotShare(messages, participationShareWindow, 0); bot != 10 || total != 30 {
		t.Fatalf("window bot=%d total=%d", bot, total)
	}
	if bot, total := proactiveReplyBotShare(messages[:3], participationShareWindow, 0); bot != 3 || total != 3 {
		t.Fatalf("short history bot=%d total=%d", bot, total)
	}
	if bot, total := proactiveReplyBotShare(nil, participationShareWindow, 0); bot != 0 || total != 0 {
		t.Fatalf("empty history bot=%d total=%d", bot, total)
	}
	// 时间跨度之外的旧消息不参与统计：几小时前机器人说过多少条，不代表此刻在刷屏。
	aged := make([]proactiveReplyHistoryItem, 0, 12)
	for i := 0; i < 12; i++ {
		age := int64(i) * 120
		aged = append(aged, proactiveReplyHistoryItem{IsBot: i%2 == 0, AgeSeconds: &age})
	}
	bot, total := proactiveReplyBotShare(aged, participationShareWindow, participationShareSpanSeconds)
	if bot != 3 || total != 6 {
		t.Fatalf("span-limited bot=%d total=%d", bot, total)
	}
	if !participationBotShareBlocks(bot, total, "medium") {
		t.Fatal("half of the last ten minutes is the bot and must pause the chat branch")
	}
	// 缺 age_seconds 的条目按刚发生处理，不会因为缺字段被悄悄漏掉。
	if bot, total := proactiveReplyBotShare([]proactiveReplyHistoryItem{{IsBot: true}}, participationShareWindow, participationShareSpanSeconds); bot != 1 || total != 1 {
		t.Fatalf("missing age bot=%d total=%d", bot, total)
	}
}

// 附和、捧场、顺口接一句本身是正常闲聊（2026-09-16 主人明确的口径），评分不能因为
// 没带新信息就把它压低。当初线上 6% 的插话以「确实/没错」开头，真正的问题不是附和，
// 是顺手给一个无法核实的判断补了一段听起来内行的理由——那是编造，仍然要压低。
func TestChatInTreatsAgreementAsChatButNotFabrication(t *testing.T) {
	prompt := ParticipationPreferences{Desire: 50}.prompt()
	for _, want := range []string{
		"附和、捧场、表达共鸣、顺口接一句本身就是正常闲聊",
		"不因为没带新信息就压低",
		"原样复读别人刚说过的话，不超过 0.10",
		"对一个无法核实的说法补充听起来内行、其实没有依据的理由",
		"那是在编",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("闲聊评分口径缺少 %q", want)
		}
	}
	if strings.Contains(prompt, "只能附和对方的主观判断") {
		t.Fatal("附和又被当成低分了")
	}
	// 插话回复阶段必须和评分口径一致：评分放行了附和，写回复时不能再被要求「不要附和」。
	if strings.Contains(chatInReplyPrompt, "不要附和") || !strings.Contains(chatInReplyPrompt, "附和、捧场、顺口接一句都可以") {
		t.Fatal("插话回复约束和评分口径对不上")
	}
	for _, want := range []string{"0.10 两人私聊", "0.90 群里明确抛出邀请"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("闲聊刻度缺失 %q", want)
		}
	}
}

// 「讲不出依据就压低」当初写在可回答里，而可回答是所有分支共用的门槛，写死之后
// 接梗和角色扮演一起被判死：玩笑本来就没有依据可讲。可回答并进闲聊之后这条豁免照样要留。回放 59 条线上评分时放行率从 66%
// 掉到 10%，丢的正是群里在演课堂角色扮演、接机器人自己抛的梗、拿触发行为调侃机器人这
// 几类。附和一个无法核实的事实判断是机器人撑不住的断言，接一个正在进行的玩笑没有断言，
// 只问机器人手里有没有一句新词。提示词必须把这两件事分开。
func TestChatInExemptsBanterFromEvidenceTest(t *testing.T) {
	prompt := ParticipationPreferences{Desire: 50}.prompt()
	for _, want := range []string{
		// 依据标准的适用范围写成断言类型，不是「所有消息」。
		"「没有依据就压低」只管对事实、原因、产品、人物和事件的断言",
		"群里在玩梗、在演正进行的角色扮演、或在拿机器人打趣时没有这种断言",
		// 玩笑照梗和调侃的锚点给分，不被依据标准带着一起塌。
		"照梗与调侃的锚点给",
		// 原样复读仍然留在低分区。
		"只能原样复读就不超过 0.10",
		// 玩笑包装下的事实断言不能借豁免绕开依据标准。
		"玩笑里顺带抛出的事实说法仍按依据算",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("banter carve-out missing %q", want)
		}
	}
	// 豁免必须排在附和锚点之后，读起来才是「上面那条依据标准的例外」。
	if strings.Index(prompt, "原样复读别人刚说过的话") > strings.Index(prompt, "「没有依据就压低」只管") {
		t.Fatal("banter carve-out must follow the sycophancy anchors it exempts")
	}
}

func TestParticipationSevenLevelBoundaries(t *testing.T) {
	for level, threshold := range map[string]float64{"minimal": 0.90, "low": 0.70, "medium": 0.50, "high": 0.30, "extreme": 0.10} {
		if !ratingPasses(threshold, level) || ratingPasses(threshold-0.01, level) {
			t.Fatalf("boundary %s %.2f", level, threshold)
		}
		p := ParticipationPreferences{RelevanceLevel: level, ChatLevel: level}
		r, c := p.ratingLevels()
		if r != level || c != level {
			t.Fatalf("lost %s", level)
		}
	}
	if ratingPasses(1, "off") || !ratingPasses(0, "always") || ratingPasses(1, "invalid") {
		t.Fatal("off/always/invalid mismatch")
	}
}
func TestParticipationRatingsRouting(t *testing.T) {
	provider := &capturingLLMProvider{}
	r := NewRuntime(BotConfig{Participation: &ParticipationPreferences{RelevanceLevel: "medium", ChatLevel: "high", CooldownSeconds: 30}}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "u", MessageID: "m", RawMessage: "接着聊"}
	for _, tc := range []struct {
		rel, chat float64
		want      bool
	}{{0.5, 0.1, true}, {0.1, 0.3, true}, {0.1, 0.29, false}, {0, 0, false}} {
		provider.reply = fmt.Sprintf(`{"relevance":{"score":%.2f,"reason":"相关度"},"answerability":{"score":0.8,"reason":"有意义的回复"},"chat_in":{"score":%.2f,"reason":"闲聊"}}`, tc.rel, tc.chat)
		_, _, _, got := r.routeProactiveReplyBatch(context.Background(), []proactiveReplyCandidate{{Event: event, Text: event.RawMessage}})
		if got != tc.want {
			t.Fatalf("%+v got %v", tc, got)
		}
	}
	r.markChatInReplied(event)
	provider.reply = `{"relevance":{"score":0.9,"reason":"直接接话"},"answerability":{"score":0.8,"reason":"有意义的回复"},"chat_in":{"score":0.1,"reason":"不适合闲聊"}}`
	_, _, _, got := r.routeProactiveReplyBatch(context.Background(), []proactiveReplyCandidate{{Event: event}})
	if !got {
		t.Fatal("chat cooldown blocked relevance branch")
	}
}

// scriptedRouterProvider 按顺序返回预设回复，最后一条会一直重复。
type scriptedRouterProvider struct {
	mu      sync.Mutex
	replies []string
	calls   []llm.GenerateRequest
}

// Generate 记录请求并返回脚本里的下一条回复。
func (p *scriptedRouterProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, req)
	reply := ""
	if len(p.replies) > 0 {
		reply = p.replies[0]
		if len(p.replies) > 1 {
			p.replies = p.replies[1:]
		}
	}
	return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: reply}, nil
}

func (p *scriptedRouterProvider) callSnapshot() []llm.GenerateRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]llm.GenerateRequest(nil), p.calls...)
}

func requestContains(req llm.GenerateRequest, needle string) bool {
	for _, message := range req.Messages {
		if strings.Contains(message.Content, needle) {
			return true
		}
	}
	return false
}

func lastParticipationRatingLog(t *testing.T, logs *captureAppLogs) map[string]any {
	t.Helper()
	for _, entry := range logs.entriesSnapshot() {
		if entry.Action == "diana.proactive_reply_route" && entry.Metadata["ratings"] != nil {
			return entry.Metadata
		}
	}
	t.Fatal("no participation rating log")
	return nil
}

func TestParticipationRatingsRetryOnceOnParseFailure(t *testing.T) {
	valid := `{"relevance":{"score":0.80,"reason":"直接问机器人"},"answerability":{"score":0.80,"reason":"能给出有内容的回答"},"chat_in":{"score":0.10,"reason":"不需要闲聊"}}`
	newRuntime := func(replies ...string) (*Runtime, *scriptedRouterProvider, *captureAppLogs, MessageEvent) {
		provider := &scriptedRouterProvider{replies: replies}
		logs := &captureAppLogs{}
		r := NewRuntime(BotConfig{Participation: &ParticipationPreferences{RelevanceLevel: "medium", ChatLevel: "medium", CooldownSeconds: 30}}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
		r.SetAppLogWriter(logs)
		return r, provider, logs, MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "u", MessageID: "m", RawMessage: "Diana 这个怎么弄"}
	}

	r, provider, logs, event := newRuntime("我先想想怎么打分。", valid)
	routed, _, _, allowed := r.routeProactiveReplyBatch(context.Background(), []proactiveReplyCandidate{{Event: event, Text: event.RawMessage}})
	if !allowed {
		t.Fatalf("retry result ignored: %s", routed.routingReason)
	}
	calls := provider.callSnapshot()
	if len(calls) != 2 {
		t.Fatalf("calls=%d want 2", len(calls))
	}
	if requestContains(calls[0], participationRatingsRetryReminder) || !requestContains(calls[1], participationRatingsRetryReminder) {
		t.Fatalf("reminder must appear only on the retry: %+v", calls[1].Messages)
	}
	if !requestContains(calls[1], "Diana 这个怎么弄") || !requestContains(calls[1], "接话评分模块") {
		t.Fatal("retry dropped the original payload")
	}
	if metadata := lastParticipationRatingLog(t, logs); metadata["retried"] != true || metadata["parsed"] != true || metadata["allowed"] != true {
		t.Fatalf("retry log: %+v", metadata)
	}

	r, provider, logs, event = newRuntime("我先想想怎么打分。", "还是想不好。")
	routed, _, _, allowed = r.routeProactiveReplyBatch(context.Background(), []proactiveReplyCandidate{{Event: event, Text: event.RawMessage}})
	if allowed || !strings.Contains(routed.routingReason, "接话评分格式无效") {
		t.Fatalf("second failure must stay silent: allowed=%t reason=%s", allowed, routed.routingReason)
	}
	if calls := provider.callSnapshot(); len(calls) != 2 {
		t.Fatalf("retried more than once: calls=%d", len(calls))
	}
	if metadata := lastParticipationRatingLog(t, logs); metadata["retried"] != true || metadata["parsed"] != false {
		t.Fatalf("failed retry log: %+v", metadata)
	}

	r, provider, logs, event = newRuntime(valid)
	if _, _, _, allowed = r.routeProactiveReplyBatch(context.Background(), []proactiveReplyCandidate{{Event: event, Text: event.RawMessage}}); !allowed {
		t.Fatal("valid rating rejected")
	}
	if calls := provider.callSnapshot(); len(calls) != 1 {
		t.Fatalf("retried a parseable answer: calls=%d", len(calls))
	}
	if metadata := lastParticipationRatingLog(t, logs); metadata["retried"] != false {
		t.Fatalf("unexpected retry flag: %+v", metadata)
	}
}

func TestParticipationRatingsPromptAndConfig(t *testing.T) {
	for _, level := range []string{"off", "low", "medium", "high", "always"} {
		p := ParticipationPreferences{RelevanceLevel: level, ChatLevel: level}
		prompt := p.prompt()
		for _, banned := range []string{"should_reply", "true", "false", "substance", "confidence", "category"} {
			if strings.Contains(prompt, banned) {
				t.Fatalf("prompt contains %s", banned)
			}
		}
		if !strings.Contains(prompt, level) {
			t.Fatal("level missing")
		}
		encoded, _ := json.Marshal(PayloadFromConfig(BotConfig{Participation: &p}))
		var payload ConfigPayload
		if err := json.Unmarshal(encoded, &payload); err != nil {
			t.Fatal(err)
		}
		restored := ConfigFromPayload(payload, BotConfig{}).participationPreferences()
		a, b := restored.ratingLevels()
		if a != level || b != level {
			t.Fatalf("lost levels %s %s", a, b)
		}
	}
}
