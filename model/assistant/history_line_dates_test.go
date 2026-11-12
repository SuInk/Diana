package assistant

import (
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// 第一行、换天那一行写全日期，同一天的其余行只写时分秒；跨群历史和别的消息不动。
func TestCompactHistoryLineDatesWritesDateOnFirstLineAndDayChange(t *testing.T) {
	input := []llm.Message{
		{Role: llm.RoleSystem, Content: "人设头部"},
		{Role: llm.RoleUser, Content: "[历史 2026-11-11 23:59:10] 甲（im_user_a）: 还没睡"},
		{Role: llm.RoleAssistant, Content: "机器人自己的发言"},
		{Role: llm.RoleUser, Content: "[历史 2026-11-11 23:59:40] 乙（im_user_b）: 快了"},
		{Role: llm.RoleUser, Content: "[历史 2026-11-12 00:00:05] 甲（im_user_a）: 零点了"},
		{Role: llm.RoleUser, Content: "[历史 2026-11-12 00:01:00] 乙（im_user_b）: 晚安"},
		{Role: llm.RoleUser, Content: "[跨群历史 2026-11-12 00:01:30] 丙（im_user_c）: 别的群"},
	}
	got := compactHistoryLineDates(input)
	want := []string{
		"人设头部",
		"[历史 2026-11-11 23:59:10] 甲（im_user_a）: 还没睡",
		"机器人自己的发言",
		"[历史 23:59:40] 乙（im_user_b）: 快了",
		"[历史 2026-11-12 00:00:05] 甲（im_user_a）: 零点了",
		"[历史 00:01:00] 乙（im_user_b）: 晚安",
		"[跨群历史 2026-11-12 00:01:30] 丙（im_user_c）: 别的群",
	}
	for index, message := range got {
		if message.Content != want[index] {
			t.Fatalf("line %d = %q, want %q", index, message.Content, want[index])
		}
	}
	// 不改传进来的那份：条目缓存还握着写全日期的原件。
	if input[3].Content != "[历史 2026-11-11 23:59:40] 乙（im_user_b）: 快了" {
		t.Fatalf("input mutated: %q", input[3].Content)
	}
	// 已经省过的一份再省一遍结果不变。
	again := compactHistoryLineDates(got)
	for index := range got {
		if again[index].Content != got[index].Content {
			t.Fatalf("not idempotent at %d: %q", index, again[index].Content)
		}
	}
}

// 渲染出来的行按机器人时区写日期，省日期只比这个字符串。
func TestHistoryLinePrefixStillWritesFullDateBeforeCompaction(t *testing.T) {
	stamp := time.Date(2026, 11, 11, 10, 59, 25, 0, time.Local).Unix()
	line := historyLinePrefix(MessageEvent{Time: stamp})
	want := "[历史 " + time.Unix(stamp, 0).In(profileLocation("")).Format("2006-01-02 15:04:05") + "] "
	if line != want {
		t.Fatalf("prefix = %q, want %q", line, want)
	}
}

// 续接追加的历史行接在前缀最后一条历史行后面：同一天只写时分秒，换天写全日期。
// 前缀里只写了时分秒、到这一轮完整组装成了第一行写全日期的那条，不能当新消息再追加。
func TestAppendContextRewritesAppendedHistoryDatesAgainstPrefix(t *testing.T) {
	first := appendTestTurn(compactLines(
		"[历史 2026-11-11 23:58:00] 甲: 还没睡",
		"[历史 2026-11-11 23:59:00] 乙: 快了",
	), "", "【当前需要回复的消息】乙: 快了")
	log := appendTestLog(t, first, "早点睡")
	if log.historyDate != "2026-11-11" {
		t.Fatalf("prefix history date = %q", log.historyDate)
	}
	// 这一轮完整组装的窗口从「乙: 快了」开始，它成了第一行、写全了日期。
	second := appendTestTurn(compactLines(
		"[历史 2026-11-11 23:59:00] 乙: 快了",
		"[历史 2026-11-11 23:59:30] 甲: 我也是",
		"[历史 2026-11-12 00:00:10] 乙: 零点了",
		"[历史 2026-11-12 00:00:40] 甲: 晚安",
	), "", "【当前需要回复的消息】甲: 晚安")
	if second[2].Content != "[历史 23:59:30] 甲: 我也是" {
		t.Fatalf("full assembly not compacted: %q", second[2].Content)
	}
	continuation := continueFrom(log, second)
	if continuation == nil {
		t.Fatal("expected a continuation")
	}
	var history []string
	for _, message := range continuation.Append {
		if isHistoryLine(message.Content) {
			history = append(history, message.Content)
		}
	}
	want := []string{
		"[历史 23:59:30] 甲: 我也是",
		"[历史 2026-11-12 00:00:10] 乙: 零点了",
		"[历史 00:00:40] 甲: 晚安",
	}
	if strings.Join(history, "|") != strings.Join(want, "|") {
		t.Fatalf("appended history = %q, want %q", history, want)
	}

	// 下一轮接着续：前缀最后一条已经是 11-12，同一天的新行只写时分秒。
	transcript := append(append([]llm.Message(nil), continuation.Prefix...), continuation.Append...)
	next := newAppendContextLog(&agent.Response{Transcript: transcript, ConversationKey: "k", Continued: true}, log.headKey, estimateMessagesTokens(second), log)
	if next.historyDate != "2026-11-12" {
		t.Fatalf("next prefix history date = %q", next.historyDate)
	}
}

// 预裁剪丢掉开头几行后，留下的第一行要补回日期。
func TestBudgetPretrimRestoresDateOnFirstSurvivingHistoryLine(t *testing.T) {
	messages := compactLines(
		"[历史 2026-11-11 10:00:00] 甲: 一",
		"[历史 2026-11-11 10:01:00] 乙: 二",
		"[历史 2026-11-11 10:02:00] 甲: 三",
	)
	req := llm.GenerateRequest{Messages: []llm.Message{
		{Role: llm.RoleUser, Content: messages[0]},
		{Role: llm.RoleUser, Content: messages[1]},
		{Role: llm.RoleUser, Content: messages[2]},
		{Role: llm.RoleUser, Content: "当前", Priority: llm.MessagePriorityCurrent},
	}}
	out := applyBudgetPretrim(req, map[int]bool{0: true}, nil)
	var got []string
	for _, message := range out.Messages {
		got = append(got, message.Content)
	}
	want := []string{budgetPretrimMarker, "[历史 2026-11-11 10:01:00] 乙: 二", "[历史 10:02:00] 甲: 三", "当前"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("pretrimmed = %q, want %q", got, want)
	}
}

// compactLines 把写全日期的历史行按一份历史省成最终写法。
func compactLines(lines ...string) []string {
	messages := make([]llm.Message, 0, len(lines))
	for _, line := range lines {
		messages = append(messages, llm.Message{Role: llm.RoleUser, Content: line})
	}
	var out []string
	for _, message := range compactHistoryLineDates(messages) {
		out = append(out, message.Content)
	}
	return out
}
