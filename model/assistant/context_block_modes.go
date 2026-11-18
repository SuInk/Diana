// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "strings"

// 主回复里几块「背景」各自怎么带：总是带（默认，和以前一样）、按需（不带进上下文，
// 头部一句话告诉模型要用时拿哪个工具查）、关闭（不带，也不提示）。按需只给已经有工具
// 能查回同样内容的块；世界书、心情、群风格、群约定、链接策略、来源回溯没有对应工具，
// 只有总是和关闭两档。设置在机器人「上下文」页的「每轮带进上下文的内容」。
//
// 这里只管带不带进请求，不管功能本身：跨群记忆、自述、小窝、世界书、心情、风格学习
// 这些功能开关照旧在原处，功能关了这一块本来就没有内容。

const (
	ContextBlockAlways   = "always"
	ContextBlockOnDemand = "on_demand"
	ContextBlockOff      = "off"
)

const (
	ContextBlockCrossGroup = "cross_group"
	ContextBlockNotebook   = "notebook"
	ContextBlockSelfNote   = "self_note"
	ContextBlockWallet     = "wallet"
	ContextBlockMediaIndex = "media_index"
	ContextBlockWorldBook  = "world_book"
	ContextBlockMood       = "mood"
	ContextBlockGroupStyle = "group_style"
	ContextBlockGroupRules = "group_rules"
	ContextBlockLinkPolicy = "link_policy"
)

// contextBlockOnDemandHints 是能按需的块和按需时告诉模型的查法，按这个顺序写进头部。
var contextBlockOnDemandHints = []struct{ key, hint string }{
	{ContextBlockCrossGroup, "别的群里相关的聊天：chat_history（scope=all_groups）"},
	{ContextBlockNotebook, "笔记本里的词条和群里的叫法：notebook"},
	{ContextBlockSelfNote, "你自己记下的自我认知：self_note"},
	{ContextBlockWallet, "你的零花钱：wallet"},
	{ContextBlockMediaIndex, "更早的图片和文件：history_media"},
}

// contextBlockKeys 是所有能设的块，校验配置用。
var contextBlockKeys = map[string]bool{
	ContextBlockCrossGroup: true, ContextBlockNotebook: true, ContextBlockSelfNote: true, ContextBlockWallet: true,
	ContextBlockMediaIndex: true, ContextBlockWorldBook: true, ContextBlockMood: true, ContextBlockGroupStyle: true,
	ContextBlockGroupRules: true, ContextBlockLinkPolicy: true,
}

func contextBlockOnDemandAllowed(key string) bool {
	for _, item := range contextBlockOnDemandHints {
		if item.key == key {
			return true
		}
	}
	return false
}

// contextBlockMode 返回这一块的档位；没设、设错或这块不支持按需时按总是带。
func contextBlockMode(cfg BotConfig, key string) string {
	switch mode := strings.TrimSpace(cfg.ContextBlockModes[key]); mode {
	case ContextBlockOff:
		return ContextBlockOff
	case ContextBlockOnDemand:
		if contextBlockOnDemandAllowed(key) {
			return ContextBlockOnDemand
		}
	}
	return ContextBlockAlways
}

// contextBlockIncluded 报告这一块要不要带进主回复请求。
func contextBlockIncluded(cfg BotConfig, key string) bool {
	return contextBlockMode(cfg, key) == ContextBlockAlways
}

// normalizeContextBlockModes 去掉不认识的块和不认识的档位，总是带（默认）也不存。
func normalizeContextBlockModes(modes map[string]string) map[string]string {
	var out map[string]string
	for key, mode := range modes {
		key, mode = strings.TrimSpace(key), strings.TrimSpace(mode)
		if !contextBlockKeys[key] || (mode != ContextBlockOff && !(mode == ContextBlockOnDemand && contextBlockOnDemandAllowed(key))) {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[key] = mode
	}
	return out
}

// contextBlockOnDemandPrompt 是头部里那句「这些没带，要用自己查」。只随配置变，放头部
// 不影响缓存；没有按需的块就不写。
func contextBlockOnDemandPrompt(cfg BotConfig) string {
	var lines []string
	for _, item := range contextBlockOnDemandHints {
		if contextBlockMode(cfg, item.key) == ContextBlockOnDemand {
			lines = append(lines, "- "+item.hint)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "【按需查的内容】下面这些不会自动带进上下文。回答需要时先用对应工具查，别凭印象说：\n" + strings.Join(lines, "\n")
}
