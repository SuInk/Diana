package assistant

import (
	"strconv"

	"github.com/SuInk/diana/model/llm"
)

const (
	promptReplyBudget         = "回复长度预算：每条消息正文分别不超过 {limit} 个 Unicode 字符，汉字、标点、空白和正文排版计入，内部控制标记和非文本消息段不计。允许分条时，先按完整段落、句子或主要章节组织，不限制多条正文的字符总和。只有某一条仍太长时才精简那一条，保留核心结论、必要步骤和风险，不机械切碎列表、表格或代码。用户明确要求单条时，把整份回复控制在一条的上限内。首次生成就遵守预算；不要截断内容，也不要为了用满预算添话。模型的输出 token 预算和发送频率控制仍然有效。"
	promptReplyBudgetTelegram = "Telegram 不使用合并转发，也不自动拼接多条消息。每条消息渲染后还须不超过 {limit} 个 UTF-16 码元，非 BMP 字符通常占两个码元。用户明确要求单条时，先精简到一条的容量以内，不截断句子或代码。"
)

var (
	promptReplyBudgetSpec = styleSpec("budget", "回复长度预算", "设置了单条回复字数上限时，作为独立 system 消息放在当前消息前面。",
		promptReplyBudget, PromptVar{Name: "limit", Description: "单条消息的字数上限"})
	promptReplyBudgetTelegramSpec = styleSpec("budget.telegram", "Telegram 长度限制", "Telegram 平台上接在回复长度预算后面，说明平台自身的单条上限。",
		promptReplyBudgetTelegram, PromptVar{Name: "limit", Description: "Telegram 单条消息的 UTF-16 码元上限"})
)

// replyBudgetPromptMaxLimit 以上的单条上限不念给模型听。生产上常见的 35000 是防失控
// 的硬上限，超了由 normalizeReply 截断；把它写进紧挨当前消息的那段 system，模型读到
// 的是「每条有三万五千字的额度、多条总和不限」，前面再怎么要求简短都被这句压过去。
// 上限紧到真会改变写法时（比如限制在几百字内）才需要提前告诉它。
const replyBudgetPromptMaxLimit = 2000

func replyGenerationBudgetPrompt(limit int, configs ...BotConfig) string {
	if limit <= 0 || limit > replyBudgetPromptMaxLimit {
		return ""
	}
	return promptOverridesOf(configs).render(promptReplyBudgetSpec, map[string]string{"limit": strconv.Itoa(limit)})
}

// withReplyGenerationBudgetForConfig 按机器人配置插入长度预算，文案读配置里的覆盖值。
func withReplyGenerationBudgetForConfig(messages []llm.Message, cfg BotConfig) []llm.Message {
	return insertReplyGenerationBudget(messages, cfg.MaxReplyChars, cfg.Platform, cfg.PromptOverrides)
}

func withReplyGenerationBudget(messages []llm.Message, limit int, platforms ...string) []llm.Message {
	platform := ""
	if len(platforms) > 0 {
		platform = platforms[0]
	}
	return insertReplyGenerationBudget(messages, limit, platform, nil)
}

func insertReplyGenerationBudget(messages []llm.Message, limit int, platform string, overrides PromptOverrides) []llm.Message {
	cfg := BotConfig{PromptOverrides: overrides}
	prompt := replyGenerationBudgetPrompt(limit, cfg)
	if NormalizePlatformID(platform) == PlatformTelegram {
		prompt = joinPromptSections(prompt, cfg.promptf(promptReplyBudgetTelegramSpec, map[string]string{"limit": strconv.Itoa(telegramTextLimit)}))
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
