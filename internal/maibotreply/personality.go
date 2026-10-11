// SPDX-License-Identifier: GPL-3.0-only
// Adapted from MaiBot at Revision; see NOTICE.md.
package maibotreply

import "strings"

// DefaultReplyStyle is PersonalityConfig.reply_style from upstream
// src/config/official_configs.py, unchanged.
const DefaultReplyStyle = "你的风格平淡简短，可以参考贴吧的回复风格。不滥用比喻或者生硬句子。视情况省略主语或者进行倒装，风格较为随意。"

const DefaultPersonality = "是一个大二女大学生，现在正在上网和群友聊天。善于用人类的角度思考问题，聊天偏日常。"
const DefaultBehaviorStyle = "是大二女大学生，现在正在上网和群友聊天。善于用人类的角度思考问题，聊天偏日常。不会没话题硬找话题，"

// PersonalityPrompt ports _build_personality_prompt. The host supplies its
// configured character. An empty identity uses a
// neutral chat context instead of upstream's assertion that the bot is human.
// Experimental emotion traits remain disabled, as in the upstream default.
func PersonalityPrompt(name string, aliases []string, personality string) string {
	aliasText := ""
	if len(aliases) > 0 {
		aliasText = "，也有人叫你" + strings.Join(aliases, ",")
	}
	personality = strings.TrimSpace(personality)
	if personality == "" {
		personality = "正在和大家聊天。"
	}
	return "你的名字是" + name + aliasText + "。\n" + personality
}
