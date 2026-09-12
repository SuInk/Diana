package assistant

import (
	"github.com/SuInk/diana/model/agent"
	botprotocol "github.com/SuInk/diana/skills/bot-protocol"
)

func (r *Runtime) botProtocolBuiltinSkills(event MessageEvent) []agent.SkillMetadata {
	skills := r.platformInterfaceBuiltinSkills(event)
	return append(skills, agent.SkillMetadata{
		Name: "bot-protocol", Description: "Route group queries and platform actions to protocol tools, and Diana reply behavior to diana.bot_config.",
		ShortDescription: "平台群操作与 Diana 回复设置", Path: "builtin://bot-protocol/SKILL.md", Source: "builtin:bot-protocol", Content: botprotocol.Markdown(),
	})
}
