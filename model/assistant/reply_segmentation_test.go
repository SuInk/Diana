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
	chunks := splitChatReply(reply, chatReplyChunkSize)
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
		if chunks := splitChatReply(item.reply, chatReplyChunkSize); len(chunks) != 1 {
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
