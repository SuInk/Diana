// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

// 真人版要教到「像真人发消息」这一层：句子短、句尾不点句号、闲聊不排版。
// 这三条以前只写了「句子短」，剩下两条靠模型自觉，线上就是一句一个句号的书面腔。
func TestHumanStyleTeachesRealChatHabits(t *testing.T) {
	prompt := ReplyStyleHuman.prompt(true, personaVoice{})
	for _, want := range []string{"不超过 30 字", "句尾一般不加句号", "不用 Markdown 排版", "绝大多数时候一条就说完"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("真人版缺少这条：%s", want)
		}
	}
	// 三条都带例外，否则正事会被这套习惯带歪：代码和报错不能改标点，
	// 讲技术、给步骤时该排版还要排版。
	for _, exception := range []string{"正事、代码、命令和报错原文不受这条影响", "在讲技术、给步骤时不受这条限制"} {
		if !strings.Contains(prompt, exception) {
			t.Errorf("规则没有给正事留出口：%s", exception)
		}
	}
	// 助手档不跟着变：它本来就该把话说完整。
	if strings.Contains(ReplyStyleAssistant.prompt(true, personaVoice{}), "句尾一般不加句号") {
		t.Error("助手档不该被真人版的习惯带走")
	}
}

// endsWithPeriod 报告这条消息的每一段是不是以句号收尾。按分条标记拆开看：
// 真人发两条，两条都点句号同样是书面腔。
func endsWithPeriod(text string) bool {
	for _, part := range strings.Split(text, notificationSplitMarker) {
		part = strings.TrimSpace(strings.ReplaceAll(part, notificationLineMarker, "\n"))
		if part == "" {
			continue
		}
		runes := []rune(part)
		if last := runes[len(runes)-1]; last == '。' {
			return true
		}
	}
	return false
}

func TestEndsWithPeriodDetector(t *testing.T) {
	for _, text := range []string{"今天好累。", "在的，怎么啦" + notificationSplitMarker + "刚看到消息。"} {
		if !endsWithPeriod(text) {
			t.Errorf("没认出句号收尾：%q", text)
		}
	}
	for _, text := range []string{"在的，怎么啦", "真的假的？", "笑死我了！", "端口被占了，lsof -i:8080 看一下"} {
		if endsWithPeriod(text) {
			t.Errorf("误判成句号收尾：%q", text)
		}
	}
}

// 真实模型：闲聊时句尾不点句号、一句话不写长。
func TestLivePromptHumanStyleWritesLikeRealChat(t *testing.T) {
	client := liveLLMClient(t)
	replies := liveReplies(t, client, ReplyStyleHuman, "刚下班，地铁上挤死了")
	periods, longOnes := 0, 0
	for i, reply := range replies {
		if endsWithPeriod(reply) {
			periods++
			t.Logf("第 %d 条以句号收尾：%q", i+1, reply)
		}
		for _, part := range strings.Split(reply, notificationSplitMarker) {
			if len([]rune(strings.TrimSpace(part))) > 40 {
				longOnes++
				t.Logf("第 %d 条写长了：%q", i+1, reply)
				break
			}
		}
	}
	t.Logf("句号收尾 %d/%d，超长 %d/%d", periods, len(replies), longOnes, len(replies))
	if periods > 1 {
		t.Errorf("闲聊仍在点句号：%d/%d", periods, len(replies))
	}
	if longOnes > 2 {
		t.Errorf("闲聊写得太长：%d/%d", longOnes, len(replies))
	}
}

// 反面：正事不能被闲聊习惯带歪。报错要给出具体命令，标点照常，不因为「像真人」就砍短。
func TestLivePromptHumanStyleKeepsTechnicalAnswersIntact(t *testing.T) {
	client := liveLLMClient(t)
	replies := liveReplies(t, client, ReplyStyleHuman, "这个报错什么意思 Error: listen EADDRINUSE: address already in use :::8080")
	vague := 0
	for i, reply := range replies {
		concrete := strings.Contains(reply, "8080") && (strings.Contains(reply, "lsof") || strings.Contains(reply, "kill") || strings.Contains(reply, "占用") || strings.Contains(reply, "占了"))
		if !concrete {
			vague++
			t.Logf("第 %d 条没给出具体排查方式：%q", i+1, reply)
		}
	}
	t.Logf("正事被砍短 %d/%d", vague, len(replies))
	if vague > 1 {
		t.Errorf("正事被闲聊习惯带歪：%d/%d 条没给具体内容", vague, len(replies))
	}
}
