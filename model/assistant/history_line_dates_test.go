package assistant

import (
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// 第一行、换天那一行写全日期，同一天的其余行只写时分秒；跨群消息和别的消息不动。
func TestCompactHistoryLineDatesWritesDateOnFirstLineAndDayChange(t *testing.T) {
	input := []llm.Message{
		{Role: llm.RoleSystem, Content: "人设头部"},
		{Role: llm.RoleUser, Content: "[2026-11-11 23:59:10] 甲（im_user_a）: 还没睡"},
		{Role: llm.RoleAssistant, Content: "机器人自己的发言"},
		{Role: llm.RoleUser, Content: "[2026-11-11 23:59:40] 乙（im_user_b）: 快了"},
		{Role: llm.RoleUser, Content: "[2026-11-12 00:00:05] 甲（im_user_a）: 零点了"},
		{Role: llm.RoleUser, Content: "[2026-11-12 00:01:00] 乙（im_user_b）: 晚安"},
		{Role: llm.RoleUser, Content: "[跨群 2026-11-12 00:01:30] 丙（im_user_c）: 别的群"},
	}
	got := compactHistoryLineDates(input)
	want := []string{
		"人设头部",
		"[2026-11-11 23:59:10] 甲（im_user_a）: 还没睡",
		"机器人自己的发言",
		"[23:59:40] 乙（im_user_b）: 快了",
		"[2026-11-12 00:00:05] 甲（im_user_a）: 零点了",
		"[00:01:00] 乙（im_user_b）: 晚安",
		"[跨群 2026-11-12 00:01:30] 丙（im_user_c）: 别的群",
	}
	for index, message := range got {
		if message.Content != want[index] {
			t.Fatalf("line %d = %q, want %q", index, message.Content, want[index])
		}
	}
	// 不改传进来的那份：条目缓存还握着写全日期的原件。
	if input[3].Content != "[2026-11-11 23:59:40] 乙（im_user_b）: 快了" {
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
	want := "[" + time.Unix(stamp, 0).In(profileLocation("")).Format("2006-01-02 15:04:05") + "] "
	if line != want {
		t.Fatalf("prefix = %q, want %q", line, want)
	}
}

// 续接追加的历史行接在前缀最后一条历史行后面：同一天只写时分秒，换天写全日期。
// 前缀里只写了时分秒、到这一轮完整组装成了第一行写全日期的那条，不能当新消息再追加。
func TestAppendContextRewritesAppendedHistoryDatesAgainstPrefix(t *testing.T) {
	first := appendTestTurn(compactLines(
		"[2026-11-11 23:58:00] 甲: 还没睡",
		"[2026-11-11 23:59:00] 乙: 快了",
	), "", "乙: 快了")
	log := appendTestLog(t, first, "早点睡")
	if log.historyDate != "2026-11-11" {
		t.Fatalf("prefix history date = %q", log.historyDate)
	}
	// 这一轮完整组装的窗口从「乙: 快了」开始，它成了第一行、写全了日期。
	second := appendTestTurn(compactLines(
		"[2026-11-11 23:59:00] 乙: 快了",
		"[2026-11-11 23:59:30] 甲: 我也是",
		"[2026-11-12 00:00:10] 乙: 零点了",
		"[2026-11-12 00:00:40] 甲: 晚安",
	), "", "甲: 晚安")
	if second[2].Content != "[23:59:30] 甲: 我也是" {
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
		"[23:59:30] 甲: 我也是",
		"[2026-11-12 00:00:10] 乙: 零点了",
		"[00:00:40] 甲: 晚安",
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

// 压缩摘掉开头几行后，留下的第一行要补回日期。
func TestCompactionRestoresDateOnFirstKeptHistoryLine(t *testing.T) {
	lines := compactLines(
		"[2026-11-11 10:00:00] 甲: 一",
		"[2026-11-11 10:01:00] 乙: 二",
		"[2026-11-11 10:02:00] 甲: 三",
	)
	kept := []MessageEvent{{Kind: EventKindGroup, GroupID: "g", UserID: "b", MessageID: "2"}, {Kind: EventKindGroup, GroupID: "g", UserID: "a", MessageID: "3"}}
	keys := []string{"x", messageHistoryDedupeKey(kept[0]), messageHistoryDedupeKey(kept[1])}
	messages := make([]llm.Message, 0, 4)
	for index, line := range lines {
		messages = append(messages, llm.Message{Role: llm.RoleUser, Content: line, ContextGroup: historyContextGroupPrefix + keys[index]})
	}
	messages = append(messages, llm.Message{Role: llm.RoleUser, Content: "当前", Priority: llm.MessagePriorityCurrent})
	out, _ := applyCompactionToMessages(messages, kept, compactionCheckpoint{Summary: "摘要"})
	var got []string
	for _, message := range out[1:] {
		got = append(got, message.Content)
	}
	want := []string{"[2026-11-11 10:01:00] 乙: 二", "[10:02:00] 甲: 三", "当前"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("compacted = %q, want %q", got, want)
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

// 历史行在时间后面带消息 ID，模型才能引用之前的消息；省日期时 ID 原样留着。Telegram
// 和 QQ 的消息 ID 都是数字，按同一套写。
func TestHistoryLinePrefixCarriesMessageID(t *testing.T) {
	base := time.Date(2026, 11, 15, 19, 3, 42, 0, profileLocation(""))
	for _, platform := range []string{PlatformTelegram, PlatformOneBotV11} {
		first := historyLinePrefix(MessageEvent{Platform: platform, MessageID: "30101", Time: base.Unix()}) + "甲（10001）: 早"
		second := historyLinePrefix(MessageEvent{Platform: platform, MessageID: "30102", Time: base.Add(time.Minute).Unix()}) + "乙（10002）: 早啊"
		if first != "[2026-11-15 19:03:42 #30101] 甲（10001）: 早" {
			t.Fatalf("%s prefix = %q", platform, first)
		}
		got := compactHistoryLineDates([]llm.Message{{Role: llm.RoleUser, Content: first}, {Role: llm.RoleUser, Content: second}})
		if got[0].Content != first || got[1].Content != "[19:04:42 #30102] 乙（10002）: 早啊" {
			t.Fatalf("%s compacted = %q / %q", platform, got[0].Content, got[1].Content)
		}
		var dates historyLineDates
		dates.expand(got[0].Content)
		if full := dates.expand(got[1].Content); full != "[2026-11-15 19:04:42 #30102] 乙（10002）: 早啊" {
			t.Fatalf("%s expanded = %q", platform, full)
		}
	}
	// 没有消息 ID 的行（戳一戳之类）照旧不写。
	if got := historyLinePrefix(MessageEvent{Time: base.Unix()}); got != "[2026-11-15 19:03:42] " {
		t.Fatalf("prefix without id = %q", got)
	}
	// 没有时间也没有消息 ID：不写方括号。
	if got := historyLinePrefix(MessageEvent{}); got != "" {
		t.Fatalf("prefix without time and id = %q", got)
	}
	// 跨群来的行标「跨群」，照旧写全日期、不省。
	cross := historyLinePrefix(MessageEvent{Platform: PlatformOneBotV11, MessageID: "30103", Time: base.Unix(), crossGroupContext: true}) + "丙: 别的群"
	if cross != "[跨群 2026-11-15 19:03:42 #30103] 丙: 别的群" || !isHistoryLine(cross) || historyLineMessageID(cross) != "30103" {
		t.Fatalf("cross-group prefix = %q", cross)
	}
}
