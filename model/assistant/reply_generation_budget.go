package assistant

import (
	"fmt"

	"github.com/SuInk/diana/model/llm"
)

func replyGenerationBudgetPrompt(limit int) string {
	if limit <= 0 {
		return ""
	}
	return fmt.Sprintf("回复长度预算：每条消息正文分别不超过 %d 个 Unicode 字符，汉字、标点、空白和正文排版计入，内部控制标记和非文本消息段不计。允许分条时，先按完整段落、句子或主要章节组织，不限制多条正文的字符总和。只有某一条仍太长时才精简那一条，保留核心结论、必要步骤和风险，不机械切碎列表、表格或代码。用户明确要求单条时，把整份回复控制在一条的上限内。首次生成就遵守预算；不要截断内容，也不要为了用满预算添话。模型的输出 token 预算和发送频率控制仍然有效。", limit)
}

func withReplyGenerationBudget(messages []llm.Message, limit int, platforms ...string) []llm.Message {
	prompt := replyGenerationBudgetPrompt(limit)
	if len(platforms) > 0 && NormalizePlatformID(platforms[0]) == PlatformTelegram {
		prompt = joinPromptSections(prompt, fmt.Sprintf("Telegram 不使用合并转发，也不自动拼接多条消息。每条消息渲染后还须不超过 %d 个 UTF-16 码元，非 BMP 字符通常占两个码元。用户明确要求单条时，先精简到一条的容量以内，不截断句子或代码。", telegramTextLimit))
	}
	if prompt == "" {
		return messages
	}
	// Keep the stable prompt/history prefix and the final user message intact.
	// Copy the slice so reusable caller messages never acquire a stale budget.
	index := len(messages)
	if index > 0 && messages[index-1].Role == llm.RoleUser {
		index--
	}
	out := make([]llm.Message, len(messages)+1)
	copy(out, messages[:index])
	out[index] = llm.Message{Role: llm.RoleSystem, Content: prompt, Priority: llm.MessagePrioritySystem, AtomicText: true}
	copy(out[index+1:], messages[index:])
	return out
}
