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

func TestChatReplyDoesNotTreatOccasionalLabelsAsStructuredBlock(t *testing.T) {
	reply := "单颗在 2 到 5 元之间比较合理喵\n像刚才看到的活动价，折合单颗大约 2.3 元，性价比就非常划算喵\n网购活动或者量贩平价款：2 到 3 元/个最合适喵\n品牌日常价或线下烘焙店：3 到 5 元/个很正常喵\n如果是高级手作或者精品烘焙，6 到 9 元/个也有喵"
	chunks := splitChatReply(reply, chatSplitLimits{})
	if len(chunks) != 5 {
		t.Fatalf("普通解释中偶尔出现标签行时仍应自然分条：%#v", chunks)
	}
}

// 行首的提及是投递信息，不是「标签：内容」。它里面那个冒号不能和正文里另一行的
// 冒号凑成两条结构化行，否则整段会被误判成清单，模型给出的换行也就不再分条。
func TestChatReplyMentionDoesNotTurnProseIntoStructuredBlock(t *testing.T) {
	replies := []string{
		"[diana-at:10002] 你说得对，刚才我没核实就顺着“设备名称不能改”接话了，确实是在传播假消息喵\n" +
			"这张图已经直接说明设备名称可以改，甚至你都改成“垃圾华为”了喵\n" +
			"所以正确说法是：HarmonyOS 这里的设备名称能改，不能拿它当作小尾巴或蓝牙设备名是否可改的证据喵",
		"[CQ:at,qq=10002] 第一层结论\n第二层解释\n所以正确说法是：第三层结论",
	}
	for _, reply := range replies {
		chunks := splitChatReply(reply, chatSplitLimits{})
		if len(chunks) != 3 {
			t.Fatalf("带提及的三行普通回复没有自然分条：%#v", chunks)
		}
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
		{"提及后的逐项打分", "[diana-at:10002] 画面：8 分\n剧情：6 分"},
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

// 一句话就是一条消息：句子内部的逗号顿号不分条，末尾那个句号也不算边界。
func TestChatReplyKeepsSingleSentenceRepliesWhole(t *testing.T) {
	for _, reply := range []string{
		"辛苦了，早点睡吧。",
		"先 lsof -i:8080 看看是谁占着，一般是上次没退干净的进程",
	} {
		if chunks := splitChatReply(reply, chatSplitLimits{}); len(chunks) != 1 {
			t.Fatalf("单句被拆开了：%#v", chunks)
		}
	}
}

// 按句号分条曾经有个 60 字的起步门槛，短行整条留着。它拦下来的是一批四五十字、
// 两三句话的回复——恰恰是最该分开发的长度。
func TestChatReplySplitsShortMultiSentenceReplies(t *testing.T) {
	chunks := splitChatReply("端口被占了。先 lsof -i:8080 看看是谁占着，一般是上次没退干净的进程。", chatSplitLimits{})
	if len(chunks) != 2 {
		t.Fatalf("两句话应该分成两条：%#v", chunks)
	}
	if chunks[0] != "端口被占了" {
		t.Fatalf("分条位置不对：%#v", chunks)
	}
}

// 成块的内容（清单、步骤、代码、引用的诗文）整块发，折了行的半句话跟着上一行走。
func TestChatReplyKeepsStructuredBlocksWhole(t *testing.T) {
	block := "第一步：先看 dmesg。\n第二步：再看 journalctl。\n第三步：最后查内存。\n第四步：都不行就重启。"
	if got := splitReplyLines(block); len(got) != 1 {
		t.Fatalf("成块的内容被拆开了：%#v", got)
	}
}

// 条数上限管的是整条回复：按换行分出来超过上限，就把相邻的短段并到上限之内。
//
// 这条用例原先断言的是「超上限退回整条发」，理由是不做「超出的并进最后一条」——
// 那会让最后一条拖着个尾巴。防的方向对，做法太狠：上限 5、模型写 6 段，得到一坨。
// 现在改成并相邻最短的那对，最后一条不会拖尾巴，长段也保持独立，那条理由仍然成立。
func TestChatReplyMergesShortLinesWhenTheyExceedCap(t *testing.T) {
	lines := make([]string, 0, 8)
	for i := 0; i < 8; i++ {
		lines = append(lines, "第几句话")
	}
	many := strings.Join(lines, "\n")
	if chunks := splitChatReply(many, chatSplitLimits{}); len(chunks) != replyMaxChatBubbles {
		t.Fatalf("八行超过上限，应该并到 %d 条：%#v", replyMaxChatBubbles, chunks)
	}
	// 正好到上限就照分，一行不并。
	five := strings.Join(lines[:5], "\n")
	if chunks := splitChatReply(five, chatSplitLimits{}); len(chunks) != 5 {
		t.Fatalf("五行正好到上限，应该分成五条：%#v", chunks)
	}
	// 合并之后模型显式写的标记仍然照做，而且不会被并进相邻块。
	chunks := splitChatReply(many+notificationSplitMarker+"补一句", chatSplitLimits{})
	if chunks[len(chunks)-1] != "补一句" {
		t.Fatalf("标记后面那句被并进了前一块：%#v", chunks)
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
	// 上限调到 2 之后，三行就超了，并成两条而不是整条发。
	if chunks := splitChatReply("第一句\n第二句\n第三句", limits); len(chunks) != 2 {
		t.Fatalf("上限 2 时三行应该并成两条：%#v", chunks)
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

// TestTrailingBracketToneStillSplits 盯住猫娘那个语气词和分条逻辑的冲突。
//
// 提示词已经不教「句尾一个孤零零的『（』」了（审核器会把它当截断），但老配置的
// 句尾候选里还留着，模型自己也会写。分条这边把行尾的开括号当成「话没说完」，会把
// 带「（」的那句粘到下一句上——两次独立发言挤进同一个气泡。线上原话就是这个形状。
func TestTrailingBracketToneStillSplits(t *testing.T) {
	reply := "被你这么一问，我确实悄悄给自己打勾了喵（\n又是糖又是温水的，你这夸法甜得我要化掉了喵"
	got := splitChatReply(reply, chatSplitLimitsFrom(DefaultBotConfig().WithDefaults()))
	if len(got) != 2 {
		t.Fatalf("带「（」的两句被并成了 %d 条：%q", len(got), got)
	}
	if !strings.HasSuffix(got[0], "（") {
		t.Fatalf("语气词「（」没留在第一条末尾：%q", got[0])
	}

	// 半角的一样。
	half := "我好像说漏嘴了(\n算了当我没说"
	if got := splitChatReply(half, chatSplitLimitsFrom(DefaultBotConfig().WithDefaults())); len(got) != 2 {
		t.Fatalf("半角括号语气词被并成了 %d 条：%q", len(got), got)
	}

	// 反过来：真的括号插入语在开括号后断了行，后文有闭括号，那就还是同一句话，
	// 不能因为要救语气词把它也拆开。
	real := "这个接口有个坑（\n文档里没写）后面会补上"
	if got := splitChatReply(real, chatSplitLimitsFrom(DefaultBotConfig().WithDefaults())); len(got) != 1 {
		t.Fatalf("真的括号插入语被拆成了 %d 条：%q", len(got), got)
	}
}

// TestOverLimitRepliesMergeInsteadOfCollapsing 超过条数上限时并短段，而不是整条发。
//
// 原来的规矩是「要么分好，要么别分」：分不进上限就退回只认标记，等于一坨发出去。
// 上限设 5、模型写了 6 段，得到的是一整条三百字，比 6 条更难读——用户设「最多 5 条」
// 的本意是别刷屏，不是别分条。
func TestOverLimitRepliesMergeInsteadOfCollapsing(t *testing.T) {
	long := strings.Repeat("这是一段挺长的话", 6)
	reply := strings.Join([]string{long + "一", long + "二", "短的甲", "短的乙", long + "三", long + "四"}, "\n")
	limits := chatSplitLimits{ChunkSize: 400, MaxBubbles: 5}

	got := splitChatReply(reply, limits)
	if len(got) != 5 {
		t.Fatalf("切成了 %d 条，想要 5 条：%q", len(got), got)
	}
	// 均分的目标是「最长那条尽量短」。段本身长短悬殊时做不到几条一样长——那是数据
	// 的性质，不是算法没做好——所以断言的是最优性：暴力枚举所有切法，没有更好的。
	lines := strings.Split(reply, "\n")
	if got, want := longestChunkRunes(got), bruteForceMinimalLongest(lines, 5); got != want {
		t.Fatalf("最长那条是 %d 字，最优解是 %d 字", got, want)
	}
	// 顺序不能动：那是话的顺序。
	if !strings.HasPrefix(got[0], "这是一段挺长的话") || !strings.HasSuffix(got[len(got)-1], "四") {
		t.Fatalf("段的顺序被打乱了：%q", got)
	}
}

func longestChunkRunes(chunks []string) int {
	longest := 0
	for _, chunk := range chunks {
		if length := len([]rune(chunk)); length > longest {
			longest = length
		}
	}
	return longest
}

// bruteForceMinimalLongest 枚举所有把 lines 连续切成 count 段的方式，返回其中
// 「最长一段」的最小值。段数小的时候够用，专门用来验证 DP 没有偷工。
func bruteForceMinimalLongest(lines []string, count int) int {
	best := -1
	var walk func(start, remaining, longest int)
	walk = func(start, remaining, longest int) {
		if remaining == 1 {
			// 剩下的全归最后一段，中间的换行也要算进长度。
			length := len([]rune(strings.Join(lines[start:], "\n")))
			if length > longest {
				longest = length
			}
			if best < 0 || longest < best {
				best = longest
			}
			return
		}
		for end := start + 1; end <= len(lines)-remaining+1; end++ {
			length := len([]rune(strings.Join(lines[start:end], "\n")))
			next := longest
			if length > next {
				next = length
			}
			walk(end, remaining-1, next)
		}
	}
	walk(0, count, 0)
	return best
}

// TestBalanceSegmentsMinimisesTheLongestBubble 均分的目标是「最长那条尽量短」，
// 而不是「每条段数一样多」：段本身长短不一，按段数均分照样会分出一条巨长的。
func TestBalanceSegmentsMinimisesTheLongestBubble(t *testing.T) {
	lines := []string{"一二三四五六七八九十", "甲", "乙", "丙", "丁"}
	got := balanceSegments(lines, 2)
	if len(got) != 2 {
		t.Fatalf("分成了 %d 条：%q", len(got), got)
	}
	// 按段数均分会切成 2+3（10 字 vs 4 字）；按长度均分应当把长的那段单独留一条。
	if got[0] != "一二三四五六七八九十" {
		t.Fatalf("长段没有单独成条：%q", got)
	}
	if got[1] != "甲\n乙\n丙\n丁" {
		t.Fatalf("剩下的没有并成一条：%q", got)
	}
}

// TestBubbleQuotaFavoursTheCrowdedBlock 名额先保证每块一条，剩下的给最挤的那块。
func TestBubbleQuotaFavoursTheCrowdedBlock(t *testing.T) {
	crowded := []string{"很长的一段话很长的一段话", "很长的一段话很长的一段话", "很长的一段话很长的一段话"}
	small := []string{"短"}
	quotas := allocateBubbleQuota([][]string{crowded, small}, 4)
	if quotas[1] != 1 {
		t.Fatalf("只有一段的块拿了 %d 个名额，应当封顶在 1", quotas[1])
	}
	if quotas[0] != 3 {
		t.Fatalf("拥挤的块只拿到 %d 个名额，剩余名额应当都给它", quotas[0])
	}
}

// TestMergeNeverCrossesSplitMarker 合并不跨越 <dianabr>：那是模型明说要断开的地方。
// 标记块本身就多于上限时按标记发，允许超——那是模型要的条数，不是运行时猜的。
func TestMergeNeverCrossesSplitMarker(t *testing.T) {
	reply := strings.Join([]string{"第一句", "第二句", "第三句", "第四句", "第五句", "第六句"}, notificationSplitMarker)
	limits := chatSplitLimits{ChunkSize: 400, MaxBubbles: 3}

	got := splitChatReply(reply, limits)
	if len(got) != 6 {
		t.Fatalf("模型显式分的 6 条被合并成了 %d 条：%q", len(got), got)
	}

	// 块内可以合并，块之间不行：两个标记块各三行，上限 4 时应当各自并成两段。
	block := "甲一\n甲二\n甲三" + notificationSplitMarker + "乙一\n乙二\n乙三"
	merged := splitChatReply(block, chatSplitLimits{ChunkSize: 400, MaxBubbles: 4})
	if len(merged) != 4 {
		t.Fatalf("块内合并结果是 %d 条：%q", len(merged), merged)
	}
	for _, chunk := range merged {
		if strings.Contains(chunk, "甲") && strings.Contains(chunk, "乙") {
			t.Fatalf("合并跨过了 <dianabr>：%q", chunk)
		}
	}
}

// TestUnderLimitRepliesKeepEveryLine 没超上限的照旧逐行分，合并这条路不该有副作用。
func TestUnderLimitRepliesKeepEveryLine(t *testing.T) {
	reply := "第一段\n第二段\n第三段"
	got := splitChatReply(reply, chatSplitLimits{ChunkSize: 400, MaxBubbles: 5})
	if len(got) != 3 {
		t.Fatalf("切成了 %d 条，想要 3 条：%q", len(got), got)
	}
}

// 合并转发已经把所有节点收进一张卡片，不需要再按普通气泡的条数上限合并。模型写了
// 几个自然段，卡片里就保留几个节点。
func TestForwardReplyKeepsNaturalLinesBeyondBubbleLimit(t *testing.T) {
	reply := "第一段\n第二段\n第三段\n第四段\n第五段\n第六段"
	limits := chatSplitLimits{ChunkSize: 400, MaxBubbles: 2}

	got := splitForwardReply(reply, limits)
	if len(got) != 6 {
		t.Fatalf("合并转发把 6 行压成了 %d 个节点：%q", len(got), got)
	}
	for index, want := range strings.Split(reply, "\n") {
		if got[index] != want {
			t.Fatalf("节点 %d = %q，want %q", index, got[index], want)
		}
	}
}

func TestForwardReplyRespectsNaturalSplitSwitch(t *testing.T) {
	off := chatSplitLimits{ChunkSize: 400, MaxBubbles: 2, MarkerOnly: true}
	if got := splitForwardReply("第一段\n第二段\n第三段", off); len(got) != 1 {
		t.Fatalf("关闭自然分条后换行生成了 %d 个节点：%q", len(got), got)
	}
	if got := splitForwardReply("第一段"+notificationSplitMarker+"第二段", off); len(got) != 2 {
		t.Fatalf("关闭自然分条后显式标记没有生成两个节点：%q", got)
	}
}

// 分条和合并转发此前只有机器人级：GroupConfig 里根本没有这几个字段，群组页也没有
// 对应的输入框。但群和群的说话节奏不一样，一个技术群里长回复整条读更省事，一个闲
// 聊群里同样长度得拆开发才不像播报。
func TestGroupLevelSplitAndForwardOverridesReachEffectiveConfig(t *testing.T) {
	base := BotConfig{
		ResponseMode:               ResponseModeStandard,
		ReplyMaxBubbles:            5,
		DirectReplyChunkSize:       400,
		ForwardReplyThreshold:      900,
		ForwardReplyChunkThreshold: 5,
	}.WithDefaults()
	runtime := NewRuntime(base, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{
		"chatty": {
			GroupID:                    "chatty",
			NaturalReplySplitEnabled:   boolPointer(false),
			ReplyMaxBubbles:            2,
			DirectReplyChunkSize:       120,
			ForwardReplyThreshold:      300,
			ForwardReplyChunkThreshold: 3,
		},
	}})

	cfg := runtime.effectiveConfigForEvent(MessageEvent{Kind: EventKindGroup, GroupID: "chatty"})
	if boolValue(cfg.NaturalReplySplitEnabled, true) {
		t.Fatal("群级自然分条开关没生效")
	}
	if cfg.ReplyMaxBubbles != 2 || cfg.DirectReplyChunkSize != 120 {
		t.Fatalf("群级分条阈值没生效：bubbles=%d chunk=%d", cfg.ReplyMaxBubbles, cfg.DirectReplyChunkSize)
	}
	if cfg.ForwardReplyThreshold != 300 || cfg.ForwardReplyChunkThreshold != 3 {
		t.Fatalf("群级合并转发阈值没生效：len=%d chunks=%d", cfg.ForwardReplyThreshold, cfg.ForwardReplyChunkThreshold)
	}
	// 配置字段好看不算数，得真的传到分条那一层。
	limits := chatSplitLimitsFrom(cfg)
	if !limits.MarkerOnly || limits.MaxBubbles != 2 || limits.ChunkSize != 120 {
		t.Fatalf("chatSplitLimits = %#v", limits)
	}
	if parts := splitChatReply("第一句。\n第二句。\n第三句。", limits); len(parts) != 1 {
		t.Fatalf("关掉自然分条后仍然分成了 %d 条：%#v", len(parts), parts)
	}

	// 没有单独配置的群跟随机器人。
	other := runtime.effectiveConfigForEvent(MessageEvent{Kind: EventKindGroup, GroupID: "other"})
	if !boolValue(other.NaturalReplySplitEnabled, true) || other.ReplyMaxBubbles != 5 || other.DirectReplyChunkSize != 400 {
		t.Fatalf("未配置的群没有跟随机器人：%#v", chatSplitLimitsFrom(other))
	}
	if other.ForwardReplyThreshold != 900 || other.ForwardReplyChunkThreshold != 5 {
		t.Fatalf("未配置的群合并转发没有跟随机器人：len=%d chunks=%d", other.ForwardReplyThreshold, other.ForwardReplyChunkThreshold)
	}
}

func TestGroupNaturalSplitInheritanceSurvivesBotChanges(t *testing.T) {
	base := BotConfig{NaturalReplySplitEnabled: boolPointer(true)}.WithDefaults()
	store := &stubGroupConfigStore{configs: map[string]GroupConfig{
		"inherited": (GroupConfig{GroupID: "inherited"}).WithDefaults("inherited", base),
		"default":   DefaultGroupConfig("default", base),
		"on":        {GroupID: "on", NaturalReplySplitEnabled: boolPointer(true)},
		"off":       {GroupID: "off", NaturalReplySplitEnabled: boolPointer(false)},
	}}
	runtime := NewRuntime(base, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetGroupConfigStore(store)
	for _, enabled := range []bool{false, true, false} {
		cfg := base
		cfg.NaturalReplySplitEnabled = boolPointer(enabled)
		if err := runtime.UpdateConfigInPlace(cfg); err != nil {
			t.Fatal(err)
		}
		for _, groupID := range []string{"inherited", "default", "on", "off", "unconfigured"} {
			event := MessageEvent{Kind: EventKindGroup, GroupID: groupID}
			effective := runtime.effectiveConfigForEvent(event)
			want := enabled
			if groupID == "on" {
				want = true
			} else if groupID == "off" {
				want = false
			}
			if got := boolValue(effective.NaturalReplySplitEnabled, true); got != want {
				t.Errorf("bot=%v group=%s: natural split=%v, want %v", enabled, groupID, got, want)
			}
			chunks := splitEventChatReply("First thought\nSecond thought", effective, event)
			wantChunks := 1
			if want {
				wantChunks = 2
			}
			if len(chunks) != wantChunks {
				t.Errorf("bot=%v group=%s: chunks=%q, want %d", enabled, groupID, chunks, wantChunks)
			}
		}
	}
}

func TestGroupNaturalSplitInheritsEventProfile(t *testing.T) {
	base := BotConfig{ID: "first", NaturalReplySplitEnabled: boolPointer(true)}.WithDefaults()
	other := base
	other.ID = "second"
	other.NaturalReplySplitEnabled = boolPointer(false)
	runtime := NewRuntime(base, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{
		"shared": (GroupConfig{GroupID: "shared"}).WithDefaults("shared", base),
	}})
	for _, enabled := range []bool{false, true, false} {
		other.NaturalReplySplitEnabled = boolPointer(enabled)
		runtime.SetProfiles(ProfileSet{ActiveID: base.ID, Profiles: []BotConfig{base, other}})
		for _, profile := range []BotConfig{base, other} {
			cfg := runtime.effectiveConfigForEvent(MessageEvent{
				ProfileID: profile.ID,
				Kind:      EventKindGroup,
				GroupID:   "shared",
			})
			if got := boolValue(cfg.NaturalReplySplitEnabled, true); got != *profile.NaturalReplySplitEnabled {
				t.Errorf("profile=%s: natural split=%v, want %v", profile.ID, got, *profile.NaturalReplySplitEnabled)
			}
		}
	}
}

// 分号是句内的并列分隔，不是句末。按它分条会把后半句单独扔成一条消息，读起来是
// 话说了一半。
func TestSemicolonIsNotASentenceBoundary(t *testing.T) {
	limits := chatSplitLimits{ChunkSize: 400, MaxBubbles: 5}
	reply := "这个报错有两种可能：一种是端口真的被别的进程占着，换个端口就能起来；另一种是上一次的实例没有退干净，还挂在那里等超时，得先把它清掉才行"
	parts := splitChatReply(reply, limits)
	if len(parts) != 1 {
		t.Fatalf("分号被当成句末切开了：%#v", parts)
	}

	// 句号照常分条，这条改动没把整层关掉。
	period := "端口被别的进程占着，换一个就能起来。先看看到底是谁占的，再决定要不要杀掉它，别上来就 kill 一个自己都不认识的 pid"
	if got := splitChatReply(period, limits); len(got) != 2 {
		t.Fatalf("句号分条 = %#v", got)
	}
}

// 长度兜底非切不可的时候，分号仍然是个体面的落点——比从词中间拦腰切开强。
func TestSemicolonStillWorksAsALengthFallbackCut(t *testing.T) {
	head := strings.Repeat("前半句的内容", 8)
	tail := strings.Repeat("后半句的内容", 8)
	parts := chunkTextByLength(head+"；"+tail, 50)
	if len(parts) < 2 {
		t.Fatalf("没有切开：%#v", parts)
	}
	if !strings.HasSuffix(parts[0], "；") {
		t.Fatalf("第一段没有断在分号上：%q", parts[0])
	}
}
