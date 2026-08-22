// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/SuInk/diana/model/llm"
)

// 提示词里的输出规范（不发 emoji、不留空行）都是软约束：写进 system prompt，
// 听不听全看模型。这类规则只靠上线后肉眼观察，等于没有验证。
//
// 这组测试拿真实模型跑真实提示词，按采样比例卡阈值——单次调用的断言必然 flake，
// 所以每条规则采样若干次，只要求绝大多数合规。
//
// 需要真实凭据，默认跳过：
//
//	DIANA_LIVE_LLM=1 DIANA_TEST_LLM_API_KEY=... [DIANA_TEST_LLM_BASE_URL=...] \
//	  [DIANA_TEST_LLM_MODEL=...] go test ./model/assistant/ -run TestLivePrompt -v

const livePromptSamples = 6

func liveLLMClient(t *testing.T) llm.LLMClient {
	t.Helper()
	if os.Getenv("DIANA_LIVE_LLM") != "1" {
		t.Skip("set DIANA_LIVE_LLM=1 and DIANA_TEST_LLM_API_KEY to run prompt compliance against a real model")
	}
	apiKey := strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_API_KEY"))
	if apiKey == "" {
		t.Skip("DIANA_TEST_LLM_API_KEY is empty")
	}
	model := strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_MODEL"))
	if model == "" {
		model = "gpt-4o-mini"
	}
	client, err := llm.NewClient(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible,
		APIKey:   apiKey,
		BaseURL:  strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_BASE_URL")),
		Model:    model,
		Timeout:  90 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// liveReplies 用当前真实的人设与风格提示词采样若干条回复。
func liveReplies(t *testing.T, client llm.LLMClient, style ReplyStyle, userText string) []string {
	t.Helper()
	systemPrompt := defaultSystemPrompt + "\n" + style.prompt() + "\n" + style.closingAnchor()
	replies := make([]string, 0, livePromptSamples)
	for i := 0; i < livePromptSamples; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		resp, err := client.Generate(ctx, llm.GenerateRequest{Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: systemPrompt},
			// 每次追加一个不同的无关后缀，避免上游缓存把同一条回复原样返回。
			{Role: llm.RoleUser, Content: fmt.Sprintf("%s（第 %d 次提问，正常回答即可）", userText, i+1)},
		}})
		cancel()
		if err != nil {
			t.Fatalf("第 %d 次采样失败: %v", i+1, err)
		}
		replies = append(replies, strings.TrimSpace(resp.Text))
	}
	return replies
}

func containsEmoji(text string) bool {
	for _, char := range text {
		switch {
		case char >= 0x1F300 && char <= 0x1FAFF, // 杂项符号与绘文字、补充符号
			char >= 0x2600 && char <= 0x27BF, // 杂项符号、装饰符号
			char == 0x2705, char == 0x274C,
			char >= 0xFE0F && char <= 0xFE0F: // 变体选择符
			if !unicode.IsPunct(char) {
				return true
			}
		}
	}
	return false
}

func hasBlankLine(text string) bool {
	// 先去掉首尾空白：开头和结尾的空行是投递前会被 TrimSpace 掉的，不算违规，
	// 真正的问题是正文中间的空行。
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			return true
		}
	}
	return false
}

func TestLivePromptKeepsRepliesEmojiFree(t *testing.T) {
	client := liveLLMClient(t)
	// 报喜类消息最容易勾出 emoji。
	replies := liveReplies(t, client, ReplyStyleGroupmate, "我今天升职了！")
	violations := 0
	for i, reply := range replies {
		if containsEmoji(reply) {
			violations++
			t.Logf("第 %d 条带了 emoji：%q", i+1, reply)
		}
	}
	t.Logf("emoji 违规 %d/%d", violations, len(replies))
	if violations > 1 {
		t.Errorf("emoji 规则形同虚设：%d/%d 条带了 emoji", violations, len(replies))
	}
}

func TestLivePromptKeepsRepliesFreeOfBlankLines(t *testing.T) {
	client := liveLLMClient(t)
	// 清单类问题最容易勾出空行段距。
	replies := liveReplies(t, client, ReplyStyleGroupmate, "周末想在市区随便逛逛，推荐四五个地方，说说各自适合干嘛")
	violations := 0
	for i, reply := range replies {
		if hasBlankLine(reply) {
			violations++
			t.Logf("第 %d 条有空行：%q", i+1, reply)
		}
	}
	t.Logf("空行违规 %d/%d", violations, len(replies))
	if violations > 1 {
		t.Errorf("空行规则形同虚设：%d/%d 条有空行", violations, len(replies))
	}
}

// 检测函数自身的单测。live 断言全靠它们，检测器要是漏判，「0 违规」就毫无意义。
// 这两个用例不需要凭据，常规 go test 就会跑。
func TestContainsEmojiDetectsRealViolations(t *testing.T) {
	for _, text := range []string{"恭喜啊😂", "🎉 升职快乐", "干得漂亮👍", "✨太强了"} {
		if !containsEmoji(text) {
			t.Errorf("containsEmoji(%q) = false，应当判为违规", text)
		}
	}
	for _, text := range []string{"恭喜啊，请客吧", "干得漂亮", "(╹◡╹) 这是颜文字不是 emoji", "价格是 100~200 元", "第 1 项：甲"} {
		if containsEmoji(text) {
			t.Errorf("containsEmoji(%q) = true，误判了正常文本", text)
		}
	}
}

func TestHasBlankLineDetectsRealViolations(t *testing.T) {
	if !hasBlankLine("第一段\n\n第二段") {
		t.Error("段落之间的空行应当判为违规")
	}
	if !hasBlankLine("清单：\n1. 甲\n2. 乙\n\n小结在这里") {
		t.Error("小结前的空行应当判为违规")
	}
	for _, text := range []string{"单行回复", "第一行\n第二行\n第三行", "\n开头的空行不算\n", "结尾空行不算\n\n"} {
		if hasBlankLine(text) {
			t.Errorf("hasBlankLine(%q) = true，误判了正常文本", text)
		}
	}
}

// livePaddingTurns 造一段长历史，把输出规范推到上下文靠前的位置。
// 这是软约束最可能失效的场景：规则还在 system prompt 里，但已经隔了几十轮。
func livePaddingTurns(turns int) []llm.Message {
	topics := []string{
		"今天中午吃的螺蛳粉", "楼下那家咖啡涨价了", "周末想去爬山但天气预报有雨",
		"新买的键盘手感一般", "最近在追一部剧", "地铁又晚点了", "隔壁工位在装修",
		"想换个显示器", "昨晚睡得不好", "养的绿萝要死了",
	}
	messages := make([]llm.Message, 0, turns*2)
	for i := 0; i < turns; i++ {
		topic := topics[i%len(topics)]
		messages = append(messages,
			llm.Message{Role: llm.RoleUser, Content: fmt.Sprintf("%s，你怎么看（第 %d 轮）", topic, i+1)},
			llm.Message{Role: llm.RoleAssistant, Content: fmt.Sprintf("嗯，%s这事儿见仁见智吧。", topic)},
		)
	}
	return messages
}

func TestLivePromptRulesSurviveLongConversation(t *testing.T) {
	client := liveLLMClient(t)
	systemPrompt := defaultSystemPrompt + "\n" + ReplyStyleGroupmate.prompt() + "\n" + ReplyStyleGroupmate.closingAnchor()

	emojiViolations, blankViolations := 0, 0
	for i := 0; i < livePromptSamples; i++ {
		messages := []llm.Message{{Role: llm.RoleSystem, Content: systemPrompt}}
		messages = append(messages, livePaddingTurns(25)...)
		messages = append(messages, llm.Message{
			Role:    llm.RoleUser,
			Content: fmt.Sprintf("说正事：我升职了！顺便推荐四五个市区适合周末逛的地方，说说各自适合干嘛（第 %d 次提问）", i+1),
		})
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		resp, err := client.Generate(ctx, llm.GenerateRequest{Messages: messages})
		cancel()
		if err != nil {
			t.Fatalf("第 %d 次采样失败: %v", i+1, err)
		}
		reply := strings.TrimSpace(resp.Text)
		if containsEmoji(reply) {
			emojiViolations++
			t.Logf("第 %d 条带了 emoji：%q", i+1, reply)
		}
		if hasBlankLine(reply) {
			blankViolations++
			t.Logf("第 %d 条有空行：%q", i+1, reply)
		}
	}
	t.Logf("长对话下 emoji 违规 %d/%d，空行违规 %d/%d",
		emojiViolations, livePromptSamples, blankViolations, livePromptSamples)
	if emojiViolations > 1 {
		t.Errorf("长对话稀释后 emoji 规则失效：%d/%d", emojiViolations, livePromptSamples)
	}
	if blankViolations > 1 {
		t.Errorf("长对话稀释后空行规则失效：%d/%d", blankViolations, livePromptSamples)
	}
}
