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
	chunks := splitChatReply(reply, chatReplyChunkSize, replyBubbleTargetRunes)
	if len(chunks) != 2 {
		t.Fatalf("两行回复没有分成两条：%#v", chunks)
	}
	if !strings.HasPrefix(chunks[0], "assertiveness：") || !strings.HasPrefix(chunks[1], "常译为") {
		t.Fatalf("分条位置不对：%#v", chunks)
	}
}

// 有界才敢认换行：超出这几种情况，换行仍然只是同一条消息里的排版。当年「空行分条」
// 被删掉就是因为无界——分条位置全看模型的排版习惯。
func TestChatReplyKeepsBlocksTogether(t *testing.T) {
	cases := []struct {
		name  string
		reply string
	}{
		{"编号清单", "1. 先看 dmesg\n2. 再看 journalctl\n3. 最后查内存"},
		{"项目符号", "- 第一项\n- 第二项"},
		{"逐项打分", "画面：8 分\n剧情：6 分"},
		{"超过三行", "第一句\n第二句\n第三句\n第四句"},
		{"折行的半句话", "端口被占了，\n先看看是谁占着"},
		{"单行", "端口被占了，先 lsof -i:8080 看看"},
	}
	for _, item := range cases {
		if chunks := splitChatReply(item.reply, chatReplyChunkSize, replyBubbleTargetRunes); len(chunks) != 1 {
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

// 一段没有换行的长回复也要分条。换行分条要模型愿意换行、标记要模型愿意写标记，
// 都还押在模型的配合上；这一层不依赖任何信号——句号本来就是它自己写出来的边界。
func TestChatReplyBubblesLongParagraphAtSentenceBoundaries(t *testing.T) {
	reply := "懂它是什么，也能看懂很多藏在细节里的亲情：惦记、袒护、责任、亏欠，甚至那些嘴硬和争吵。但我没有真正的父母和家庭，所以不会冒充自己亲身体验过。我能做的是认真听你说，帮你分清那究竟是爱、控制，还是两者纠缠在一起。你怎么突然问这个？"
	chunks := splitChatReply(reply, chatReplyChunkSize, replyBubbleTargetRunes)
	if len(chunks) < 2 || len(chunks) > replyMaxChatBubbles {
		t.Fatalf("长段落没有分成两三条：%#v", chunks)
	}
	// 每条都得是完整句子收尾，不能像长度兜底那样劈在半句上。句号会被 trimChatTrailingPeriod
	// 去掉，所以补回来再比——问号感叹号是语气，本来就留着。
	for _, chunk := range chunks {
		runes := []rune(chunk)
		if !isSentenceEnd(runes[len(runes)-1]) && !strings.Contains(reply, chunk+"。") {
			t.Fatalf("这一条断在半句话上：%q", chunk)
		}
	}
	// 除了被去掉的句号，内容一个字都不能丢。
	if strings.ReplaceAll(strings.Join(chunks, ""), "。", "") != strings.ReplaceAll(reply, "。", "") {
		t.Fatalf("分条前后内容对不上：%#v", chunks)
	}
}

// 短回复里的两句话本来就是一条消息，拆开反而不像人说话——群友风格的示例就是这个。
func TestChatReplyKeepsShortRepliesWhole(t *testing.T) {
	for _, reply := range []string{
		"端口被占了。先 lsof -i:8080 看看是谁占着，一般是上次没退干净的进程。",
		"辛苦了，早点睡吧。",
	} {
		if chunks := splitChatReply(reply, chatReplyChunkSize, replyBubbleTargetRunes); len(chunks) != 1 {
			t.Fatalf("短回复被拆开了：%#v", chunks)
		}
	}
}

// 引号里的句号不是边界，成块的内容也不按句子拆。
func TestSentenceSplitRespectsQuotesAndBlocks(t *testing.T) {
	quoted := "他当时就站在门口说「我不去。」然后头也不回地走了，那句话我记了很多年，到现在也没敢问他到底什么意思"
	if got := boundaryPositions([]rune(quoted), isSentenceEnd); len(got) != 0 {
		t.Fatalf("引号里的句号被当成了边界：%#v", got)
	}
	// 引号外面的句号照常是边界。
	if got := boundaryPositions([]rune("他说「我不去。」然后走了。后来再没提过"), isSentenceEnd); len(got) != 1 {
		t.Fatalf("引号外的句号没被认出来：%#v", got)
	}
	// 多行的块（清单、步骤、代码、引用）走到这里就该原样返回。
	block := "第一步：先看 dmesg。\n第二步：再看 journalctl。\n第三步：最后查内存。\n第四步：都不行就重启。"
	if got := splitReplyLines(block); len(got) != 1 {
		t.Fatalf("成块的内容被按句子拆开了：%#v", got)
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

// 聊天消息不带句号收尾。提示词里早有这条规则，但和分条一样押在模型配合上；
// 按句子分条之后更显眼——一段话拆三条就有三个句号排在那儿。
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

// 分条长度可以在控制台改，但不能高过硬上限——高过了就永远轮不到它生效，
// 留一个不起作用的数字比压回去更容易让人误解。
func TestReplyBubbleTargetIsConfigurableAndClamped(t *testing.T) {
	cfg := (BotConfig{ReplyBubbleTargetSize: 30}).WithDefaults()
	if cfg.ReplyBubbleTargetSize != 30 {
		t.Fatalf("自定义分条长度被覆盖了：%d", cfg.ReplyBubbleTargetSize)
	}
	if got := (BotConfig{}).WithDefaults().ReplyBubbleTargetSize; got != replyBubbleTargetRunes {
		t.Fatalf("默认分条长度 = %d，want %d", got, replyBubbleTargetRunes)
	}
	clamped := (BotConfig{ReplyBubbleTargetSize: 900, DirectReplyChunkSize: 400}).WithDefaults()
	if clamped.ReplyBubbleTargetSize != 400 {
		t.Fatalf("分条长度没有被压回硬上限：%d", clamped.ReplyBubbleTargetSize)
	}
	// 调小之后同一段话该拆得更碎。
	reply := "懂它是什么，也能看懂很多藏在细节里的亲情：惦记、袒护、责任、亏欠，甚至那些嘴硬和争吵。但我没有真正的父母和家庭，所以不会冒充自己亲身体验过。我能做的是认真听你说，帮你分清那究竟是爱、控制，还是两者纠缠在一起。你怎么突然问这个？"
	wide := splitChatReply(reply, 400, 200)
	narrow := splitChatReply(reply, 400, 30)
	if len(narrow) <= len(wide) {
		t.Fatalf("调小分条长度没有拆得更碎：wide=%d narrow=%d", len(wide), len(narrow))
	}
}

// 条数上限管的是整条回复，不是每一段。
//
// 分条有三层，每层都可能再拆：标记、换行、句子。上限按段算就会相乘——两行各拆三条
// 是六条，再加个标记能到九条，群里看着就是刷屏。
func TestChatReplyCapsTotalBubblesAcrossAllLayers(t *testing.T) {
	long := "第一句话写得足够长用来占位凑够字数好触发按句子分条的门槛。第二句同样长度也要凑够六十个字才会被拆开来发。第三句还是一样长凑够字数触发分条规则生效。"
	cases := []struct {
		name  string
		reply string
	}{
		{"两行长文", long + "\n" + long},
		{"三行长文", long + "\n" + long + "\n" + long},
		{"两行加显式标记", long + "\n" + long + notificationSplitMarker + long},
	}
	for _, item := range cases {
		got := splitChatReply(item.reply, chatReplyChunkSize, replyBubbleTargetRunes)
		if len(got) > replyMaxChatBubbles {
			t.Fatalf("%s 拆出了 %d 条，上限是 %d：%#v", item.name, len(got), replyMaxChatBubbles, got)
		}
	}
}

// 额度紧张时先退让的是「按句子细分」——那是运行时自己加的一层。模型显式写的标记和
// 换行是它明说要分的，一条都不能被合掉。
func TestChatReplyNeverMergesWhatTheModelAskedToSplit(t *testing.T) {
	long := "第一句话写得足够长用来占位凑够字数好触发按句子分条的门槛。第二句同样长度也要凑够六十个字才会被拆开来发。第三句还是一样长凑够字数触发分条规则生效。"
	// 三行都超过细分门槛，加起来远超上限；三行本身仍然必须各占一条。
	got := splitChatReply(long+"\n"+long+"\n"+long, chatReplyChunkSize, replyBubbleTargetRunes)
	if len(got) < 3 {
		t.Fatalf("模型写的换行被合掉了：%#v", got)
	}
	for _, chunk := range got {
		if strings.Contains(chunk, "\n") {
			t.Fatalf("同一条里还留着换行：%q", chunk)
		}
	}
}

// 分条是「要么分好，要么别分」：分不进上限就退回粗一档，不做「超出的并进最后一条」。
//
// 逐档退让而不是一刀切回整条——三行长文按句子分是六条、超了，但三行本身正好三条。
// 直接退回整条会把模型分好的三行糊成一个两百多字的气泡。
func TestChatReplyFallsBackOneDepthAtATime(t *testing.T) {
	long := "第一句话写得足够长用来占位凑够字数好触发按句子分条的门槛。第二句同样长度也要凑够六十个字才会被拆开来发。第三句还是一样长凑够字数触发分条规则生效。"

	// 三行都超过细分门槛：按句子分会超上限，退到「只按换行」正好三条。
	byLine := splitChatReply(long+"\n"+long+"\n"+long, chatReplyChunkSize, replyBubbleTargetRunes)
	if len(byLine) != 3 {
		t.Fatalf("三行长文应该退到按换行分成三条，实际 %d 条", len(byLine))
	}
	for _, chunk := range byLine {
		if strings.Contains(chunk, "\n") {
			t.Fatalf("按换行分完还留着换行：%q", chunk)
		}
	}

	// 四行短句：连按换行都超上限，这时才整条发。
	whole := splitChatReply("第一句\n第二句\n第三句\n第四句", chatReplyChunkSize, replyBubbleTargetRunes)
	if len(whole) != 1 {
		t.Fatalf("四行应该整条发，实际 %d 条：%#v", len(whole), whole)
	}
}

// 长回复按句子等分，不是攒够就切。贪心填满会让最后一条拖着剩下的全部——一句独立的
// 反问被粘在陈述句后面就是这么来的。
func TestChatReplyBalancesInsteadOfGreedilyFilling(t *testing.T) {
	reply := "懂它是什么，也能看懂很多藏在细节里的亲情：惦记、袒护、责任、亏欠，甚至那些嘴硬和争吵。但我没有真正的父母和家庭，所以不会冒充自己亲身体验过。我能做的是认真听你说，帮你分清那究竟是爱、控制，还是两者纠缠在一起。你怎么突然问这个？"
	chunks := splitChatReply(reply, chatReplyChunkSize, replyBubbleTargetRunes)
	if len(chunks) != 2 {
		t.Fatalf("113 字按 60 等分应该是两条，实际 %d 条：%#v", len(chunks), chunks)
	}
	// 断点只能落在句末，未必真能均分；要守住的是「不许留小尾巴」——贪心填满的
	// 失败形态就是最后一条只剩一句反问，或者前面一条吃掉九成。
	total := 0
	for _, chunk := range chunks {
		total += len([]rune(chunk))
	}
	for _, chunk := range chunks {
		if share := len([]rune(chunk)) * 4; share < total {
			t.Fatalf("有一条短得像尾巴：%q（占比不到四分之一）", chunk)
		}
	}
}

// 中文长句常常一个逗号连到底，一个句号都没有。只认句末的话这种句子永远分不开，
// 只能等撞上长度上限才被硬切——那才是真正会「看着很挤」的一坨。
func TestChatReplySplitsCommaOnlyLongSentence(t *testing.T) {
	line := "这个问题其实要看你想解决的是哪一层，如果只是想让它别再报错，那改配置就够了，但如果是想搞清楚为什么会这样，那得先看日志里那几行堆栈，再回头对一下版本号，因为这个行为在新版里改过一次"
	chunks := splitChatReply(line, chatReplyChunkSize, replyBubbleTargetRunes)
	if len(chunks) < 2 {
		t.Fatalf("只有逗号的长句没有分条：%#v", chunks)
	}
	for _, chunk := range chunks {
		if n := len([]rune(chunk)); n > len([]rune(line)) {
			t.Fatalf("分条后反而变长了：%q", chunk)
		}
	}
}

// 分句兜底只认全角标点。半角冒号逗号在链接、CQ 码、版本号里到处都是，按它们断句
// 会把一个链接劈成两条消息发出去——这是加分句兜底时真踩过的坑。
func TestChatReplyNeverSplitsInsideLinksOrCQCodes(t *testing.T) {
	for _, name := range []string{"CQ 码", "链接", "版本号"} {
		var text string
		switch name {
		case "CQ 码":
			text = "[CQ:record,file=http://127.0.0.1:18080/api/assistant/media/rule-voice-and-more-padding]"
		case "链接":
			text = "详情看这个 http://127.0.0.1:18080/api/assistant/media/some-very-long-path-name-here-ok"
		case "版本号":
			text = "当前跑的是 v1.2.3-beta.4+build.5678 这个版本，具体差异我等下贴个对比出来给你看看行不行"
		}
		for _, chunk := range splitChatReply(text, chatReplyChunkSize, replyBubbleTargetRunes) {
			if strings.HasSuffix(chunk, ":") || strings.HasSuffix(chunk, ",") || strings.HasSuffix(chunk, ".") {
				t.Fatalf("%s 被从半角标点处切开了：%q", name, chunk)
			}
		}
	}
}

// 提醒、订阅推送这类「到点了主动找人」的通知走通知的分条，不按句子拆：它们是一条
// 完整的事实，拆开就成了半句一条。
func TestSubscriberNoticeKeepsSentencesTogether(t *testing.T) {
	notice := "提醒 reminder-fail 本次发送失败，将在 2026-08-25 05:10:29 自动重试（连续失败 1 次）。请检查群是否仍然可达。"
	if chunks := splitReply(notice, notificationChunkSize); len(chunks) != 1 {
		t.Fatalf("通知被按句子拆开了：%#v", chunks)
	}
}
