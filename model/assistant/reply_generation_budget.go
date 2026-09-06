package assistant

import (
	"fmt"

	"github.com/SuInk/diana/model/llm"
)

func replyGenerationBudgetPrompt(limit int) string {
	if limit <= 0 {
		return ""
	}
	return fmt.Sprintf("本轮回复字数预算：最终回复正文合计不得超过 %d 个 Unicode 字符，汉字、标点、空白和正文排版计入，内部控制标记和非文本消息段不计。这是整轮总预算，不是每条消息各有一份；单条或分条选择不改变上限。请在首次生成时就控制篇幅，保留核心结论、必要步骤和风险，删除重复和次要细节，不先写超长草稿。句子和代码保持完整，不为凑字数破坏内容。该预算仅约束给用户的最终正文，不限制工具调用与工具结果。", limit)
}

func withReplyGenerationBudget(messages []llm.Message, limit int) []llm.Message {
	prompt := replyGenerationBudgetPrompt(limit)
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
