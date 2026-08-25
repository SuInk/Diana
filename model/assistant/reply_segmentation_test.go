package assistant

import (
	"strings"
	"testing"
)

// 用户在群里看到的是「一整条」：配置里存着旧版默认文案时，提示词整段都在反对分条。
// 升级之后同一份配置必须自愈——旧文案被换掉，内置分条规则出现在提示词里。
func TestStaleConfigStopsSuppressingSegmentation(t *testing.T) {
	stale := BotConfig{
		ReplyStyle:               ReplyStyleAssistant,
		PromptPlaintextRulesText: legacyPromptPlaintextRules[1],
	}.WithDefaults()
	runtime := NewRuntime(stale, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	relationship := RelationshipPolicyFor(UserMemoryProfile{}, stale.OwnerID, "1")
	event := MessageEvent{Kind: EventKindGroup, GroupID: "1", UserID: "1"}
	prompt := runtime.systemPromptWithRelationshipAndAgentTools(event, nil, false, relationship, true, nil)

	if strings.Contains(prompt, "都必须放在同一条 OneBot v11 消息里") {
		t.Fatalf("旧版反分条文案还在提示词里：%q", prompt)
	}
	if !promptTeachesSegmentation(prompt) {
		t.Fatalf("提示词里没有分条规则：%q", prompt)
	}

	// 模型照做之后投递侧真的分成两条。
	chunks := splitReply("结论是这样"+notificationSplitMarker+"理由是那样", stale.DirectReplyChunkSize)
	if len(chunks) != 2 || chunks[0] != "结论是这样" || chunks[1] != "理由是那样" {
		t.Fatalf("投递没有按标记分条：%q", chunks)
	}
}

// 旧标记必须继续认。改名那天起，用户自定义过的提示词文案里就一直留着 <botbr>，
// 而配置不会随代码升级重写；「删除全部旧版兼容层」把归一化删掉之后，这些实例
// 两头不认：提示词教模型写 <botbr>，投递侧只认 <dianabr>。
//
// 后果不止是不分条——旧标记既没被识别也没被清掉，会原样发进群里。所以这里同时
// 断言两件事：切成了两条，且任何一条里都不许再出现标记本身。
func TestLegacySplitMarkerStillSplitsAndNeverLeaks(t *testing.T) {
	for _, reply := range []string{
		"释义在这里" + legacyNotificationSplitMarker + "常译为那样",
		"释义在这里" + notificationSplitMarker + "常译为那样",
		// 新旧混用也要处理干净：模型的历史习惯和当前提示词可能同时起作用。
		"第一句" + legacyNotificationSplitMarker + "第二句" + notificationSplitMarker + "第三句",
	} {
		chunks := splitReply(reply, chatReplyChunkSize)
		if len(chunks) < 2 {
			t.Fatalf("%q 没有分条：%#v", reply, chunks)
		}
		for _, chunk := range chunks {
			if strings.Contains(chunk, legacyNotificationSplitMarker) || strings.Contains(chunk, notificationSplitMarker) {
				t.Fatalf("标记漏进了发出去的内容：%#v", chunks)
			}
		}
	}
}

// 换行即分条：模型对 <dianabr> 这种内部标记的服从度不稳定，但它稳定会产出换行。
// 群里看到的那条「释义一行、常见翻译一行」就该是两条。
func TestChatReplySplitsShortRepliesOnNaturalLines(t *testing.T) {
	reply := "assertiveness：坚定自信、敢于明确表达自身需求和立场，同时也尊重他人的能力\n常译为「坚定表达」「自信果断」；它介于 passive（消极退让）和 aggressive（咄咄逼人）之间"
	chunks := splitChatReply(reply, chatSplitLimits{})
	if len(chunks) != 2 {
		t.Fatalf("两行回复没有分成两条：%#v", chunks)
	}
	if !strings.HasPrefix(chunks[0], "assertiveness：") || !strings.HasPrefix(chunks[1], "常译为") {
		t.Fatalf("分条位置不对：%#v", chunks)
	}
}

// 有界才敢认换行：清单、逐项打分、折了行的半句话，这些里的换行仍然只是排版。
// 当年「空行分条」被删掉就是因为无界——分条位置全看模型的排版习惯。
func TestChatReplyKeepsBlocksTogether(t *testing.T) {
	cases := []struct {
		name  string
		reply string
	}{
		{"编号清单", "1. 先看 dmesg\n2. 再看 journalctl\n3. 最后查内存"},
		{"项目符号", "- 第一项\n- 第二项"},
		{"逐项打分", "画面：8 分\n剧情：6 分"},
		{"折行的半句话", "端口被占了，\n先看看是谁占着"},
		{"单行", "端口被占了，先 lsof -i:8080 看看"},
	}
	for _, item := range cases {
		if chunks := splitChatReply(item.reply, chatSplitLimits{}); len(chunks) != 1 {
			t.Fatalf("%s 被拆开了：%#v", item.name, chunks)
		}
	}
}

// 错误提示和结构化通知不认换行：仓库订阅那张卡片就是紧凑两行，拆开就没法读了。
func TestNotificationPathIgnoresNaturalLines(t *testing.T) {
	card := "Diana 有 3 条新提交\nhttps://github.com/SuInk/Diana/commits/main"
	if chunks := splitReply(card, notificationChunkSize); len(chunks) != 1 {
		t.Fatalf("通知卡片被拆开了：%#v", chunks)
	}
	// 显式标记在通知里仍然分条，这条没变。
	if chunks := splitReply("事实"+notificationSplitMarker+"跟评", notificationChunkSize); len(chunks) != 2 {
		t.Fatalf("通知里的显式标记没有分条：%#v", chunks)
	}
}

// promptTeachesSegmentation 检查提示词是否把分条契约讲全了：换行会分条、标记也会
// 分条、清单整块发。断言契约而不是某一句原话，改文案时不用跟着改四处测试。
func promptTeachesSegmentation(prompt string) bool {
	for _, want := range []string{"意群边界换行", notificationSplitMarker, "整块发出去"} {
		if !strings.Contains(prompt, want) {
			return false
		}
	}
	return true
}

// 没有换行的长段落按句号分条，一句一条。
//
// 换行是模型给的信号，但它不一定肯换——一段解释、一句界限、一句反问写成一整段是
// 常事。这一层不依赖模型配合：句号本来就是它自己写出来的边界。
func TestChatReplySplitsUnbrokenParagraphBySentence(t *testing.T) {
	reply := "懂它是什么，也能看懂很多藏在细节里的亲情：惦记、袒护、责任、亏欠，甚至那些嘴硬和争吵。但我没有真正的父母和家庭，所以不会冒充自己亲身体验过。我能做的是认真听你说，帮你分清那究竟是爱、控制，还是两者纠缠在一起。你怎么突然问这个？"
	chunks := splitChatReply(reply, chatSplitLimits{})
	if len(chunks) != 4 {
		t.Fatalf("四句话应该分成四条：%#v", chunks)
	}
	if !strings.HasPrefix(chunks[1], "但我没有") || chunks[3] != "你怎么突然问这个？" {
		t.Fatalf("分条位置不对：%#v", chunks)
	}
	// 内容一个字都不能丢（除了被去掉的句号）。
	joined := strings.ReplaceAll(strings.Join(chunks, ""), "。", "")
	if joined != strings.ReplaceAll(reply, "。", "") {
		t.Fatalf("分条前后内容对不上：%#v", chunks)
	}
}

// 短回复不动：两句话的短回复本来就是一条消息，拆开反而不像人说话。
func TestChatReplyKeepsShortRepliesWhole(t *testing.T) {
	for _, reply := range []string{
		"端口被占了。先 lsof -i:8080 看看是谁占着，一般是上次没退干净的进程。",
		"辛苦了，早点睡吧。",
	} {
		if chunks := splitChatReply(reply, chatSplitLimits{}); len(chunks) != 1 {
			t.Fatalf("短回复被拆开了：%#v", chunks)
		}
	}
}

// 成块的内容（清单、步骤、代码、引用的诗文）整块发，折了行的半句话跟着上一行走。
func TestChatReplyKeepsStructuredBlocksWhole(t *testing.T) {
	block := "第一步：先看 dmesg。\n第二步：再看 journalctl。\n第三步：最后查内存。\n第四步：都不行就重启。"
	if got := splitReplyLines(block); len(got) != 1 {
		t.Fatalf("成块的内容被拆开了：%#v", got)
	}
}

// 条数上限管的是整条回复：按换行分出来超过上限，就不按换行分，退回只认标记。
// 不做「超出的并进最后一条」——那会让最后一条拖着个尾巴。
func TestChatReplyFallsBackToMarkerWhenLinesExceedCap(t *testing.T) {
	lines := make([]string, 0, 8)
	for i := 0; i < 8; i++ {
		lines = append(lines, "第几句话")
	}
	many := strings.Join(lines, "\n")
	if chunks := splitChatReply(many, chatSplitLimits{}); len(chunks) != 1 {
		t.Fatalf("八行超过上限，应该退回整条发：%#v", chunks)
	}
	// 正好到上限就照分。
	five := strings.Join(lines[:5], "\n")
	if chunks := splitChatReply(five, chatSplitLimits{}); len(chunks) != 5 {
		t.Fatalf("五行正好到上限，应该分成五条：%#v", chunks)
	}
	// 退回之后模型显式写的标记仍然照做。
	if chunks := splitChatReply(many+notificationSplitMarker+"补一句", chatSplitLimits{}); len(chunks) != 2 {
		t.Fatalf("退回之后标记被吞掉了：%#v", chunks)
	}
}

// 长度兜底也要断在标点上。中文没有词间空格，只找换行和空白等于每次都硬切。
func TestLengthFallbackCutsAtChinesePunctuation(t *testing.T) {
	long := strings.Repeat("这是一句用来占位的话，长度足够触发兜底切分。", 6)
	for _, chunk := range chunkTextByLength(long, 60) {
		runes := []rune(strings.TrimSpace(chunk))
		last := runes[len(runes)-1]
		if !isSentenceEnd(last) && !isClauseBreak(last) {
			t.Fatalf("兜底切分断在了半个词上：%q", chunk)
		}
	}
}

// 兜底切分只认全角标点。半角冒号逗号在链接、CQ 码、版本号里到处都是，按它们断句
// 会把一个链接从中间劈开发出去——加分句兜底时真踩过这个坑。
func TestLengthFallbackNeverCutsInsideLinksOrCQCodes(t *testing.T) {
	for _, r := range []rune{':', ',', ';', '.'} {
		if isClauseBreak(r) || isSentenceEnd(r) {
			t.Fatalf("半角 %q 不该是断点", string(r))
		}
	}
	cq := "[CQ:record,file=http://127.0.0.1:18080/api/assistant/media/rule-voice]"
	for _, chunk := range splitChatReply("语音在这里 "+cq, chatSplitLimits{}) {
		if strings.HasSuffix(chunk, ":") || strings.HasSuffix(chunk, ",") {
			t.Fatalf("被从半角标点处切开了：%q", chunk)
		}
	}
}

// 聊天消息不带句号收尾。提示词里早有这条规则，但和分条一样押在模型配合上。
func TestChatReplyDropsTrailingPeriod(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"普通句子", "知道了。", "知道了"},
		{"逗号分句照旧", "辛苦了，早点睡吧。", "辛苦了，早点睡吧"},
		{"问号是语气", "真的吗？", "真的吗？"},
		{"感叹号是语气", "好耶！", "好耶！"},
		{"省略号是话没说完", "这个嘛……", "这个嘛……"},
		{"英文句点不碰", "版本是 v1.0.", "版本是 v1.0."},
		{"域名结尾不碰", "去 example.com.", "去 example.com."},
		{"引号里的句号不是自己的句读", "他说「我不去。」", "他说「我不去。」"},
		{"没闭合的引号不动", "他说「我不去。", "他说「我不去。"},
		{"只有一个句号就别删空了", "。", "。"},
		{"句中的句号不动", "第一。第二", "第一。第二"},
	}
	for _, item := range cases {
		if got := trimChatTrailingPeriod(item.in); got != item.want {
			t.Fatalf("%s：%q → %q，want %q", item.name, item.in, got, item.want)
		}
	}
}

// 提醒、订阅推送这类通知走通知的分条：它们是一条完整的事实，拆开就成了半句一条。
func TestSubscriberNoticeKeepsSentencesTogether(t *testing.T) {
	notice := "提醒 reminder-fail 本次发送失败，将在 2026-08-25 05:10:29 自动重试（连续失败 1 次）。请检查群是否仍然可达。"
	if chunks := splitReply(notice, notificationChunkSize); len(chunks) != 1 {
		t.Fatalf("通知被按句子拆开了：%#v", chunks)
	}
}

// 正好 5 块不走卡片，逐条发；第 6 块才换成合并转发。
func TestForwardCardTriggersAboveFiveChunks(t *testing.T) {
	five := []string{"a", "b", "c", "d", "e"}
	if shouldUseForwardReply("abcde", five, 0, 0) {
		t.Fatalf("正好 5 块不该走转发卡片")
	}
	if !shouldUseForwardReply("abcdef", append(five, "f"), 0, 0) {
		t.Fatalf("6 块应该走转发卡片")
	}
	if !shouldUseForwardReply(strings.Repeat("字", 950), []string{"x"}, 900, 0) {
		t.Fatalf("超过长度阈值应该走转发卡片")
	}
}

// 条数上限和转发阈值都能在控制台改，不是写死的常量。
func TestSplitLimitsAreConfigurable(t *testing.T) {
	cfg := (BotConfig{ReplyMaxBubbles: 2, ForwardReplyChunkThreshold: 8}).WithDefaults()
	limits := chatSplitLimitsFrom(cfg)
	if limits.MaxBubbles != 2 {
		t.Fatalf("条数上限没有透传：%#v", limits)
	}
	if cfg.ForwardReplyChunkThreshold != 8 {
		t.Fatalf("转发块数阈值没有透传：%d", cfg.ForwardReplyChunkThreshold)
	}
	// 上限调到 2 之后，三行就超了，退回整条发。
	if chunks := splitChatReply("第一句\n第二句\n第三句", limits); len(chunks) != 1 {
		t.Fatalf("上限 2 时三行应该退回整条：%#v", chunks)
	}
	// 留空回落默认值。
	if got := chatSplitLimitsFrom((BotConfig{}).WithDefaults()).MaxBubbles; got != replyMaxChatBubbles {
		t.Fatalf("默认条数上限 = %d，want %d", got, replyMaxChatBubbles)
	}
	six := []string{"a", "b", "c", "d", "e", "f"}
	if shouldUseForwardReply("abcdef", six, 0, 8) {
		t.Fatalf("阈值调到 8 之后 6 块不该走转发卡片")
	}
}

// 自然分条可以整个关掉。关掉之后只认模型显式写的标记——那是它明说要分，关掉的是
// 运行时自己去猜边界这件事，不是把模型的话也一起吞掉。
func TestNaturalSplitCanBeTurnedOff(t *testing.T) {
	off := chatSplitLimitsFrom((BotConfig{NaturalReplySplitEnabled: boolPointer(false)}).WithDefaults())
	if !off.MarkerOnly {
		t.Fatal("关掉自然分条后 MarkerOnly 应该为真")
	}
	if chunks := splitChatReply("端口被占了\n先 lsof 看看", off); len(chunks) != 1 {
		t.Fatalf("关掉之后换行不该分条：%#v", chunks)
	}
	if chunks := splitChatReply("结论"+notificationSplitMarker+"理由", off); len(chunks) != 2 {
		t.Fatalf("显式标记被吞掉了：%#v", chunks)
	}
	if chatSplitLimitsFrom((BotConfig{}).WithDefaults()).MarkerOnly {
		t.Fatal("默认应该开着自然分条")
	}
	if (chatSplitLimits{}).MarkerOnly {
		t.Fatal("零值应该等于默认行为")
	}
}

// 关掉之后提示词也得改口。投递侧不再按换行分条、提示词却还写着「换行会分条」的话，
// 模型排的版就全落空了——分条位置又变回看模型的排版习惯，正是这条链路翻过车的形状。
func TestPromptFollowsTheNaturalSplitSwitch(t *testing.T) {
	on := ReplyStyleAssistant.prompt(true, personaVoice{})
	off := ReplyStyleAssistant.prompt(false, personaVoice{})
	if !strings.Contains(on, "意群边界换行") {
		t.Fatalf("开着的时候应该教换行分条：%q", on)
	}
	if strings.Contains(off, "意群边界换行") {
		t.Fatalf("关掉之后不该再教换行分条：%q", off)
	}
	if !strings.Contains(off, "换行不会分条") {
		t.Fatalf("关掉之后要明说换行只是排版：%q", off)
	}
	for _, prompt := range []string{on, off} {
		if !strings.Contains(prompt, notificationSplitMarker) {
			t.Fatalf("提示词没教分条标记：%q", prompt)
		}
	}
}
