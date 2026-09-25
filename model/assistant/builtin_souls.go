// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"embed"
	"strings"
)

// 内置人设就是几份 SOUL.md，编译进二进制，开箱即用。
//
// 以前内置人设只存在前端（builtin-personas.ts），后端的兜底正文另抄一份，两边靠
// 测试逐字节对齐。现在只有这一处：前端从 /personas 拿到它们，和用户自己存的人设
// 同一张列表，只多一个 builtin 标记表示不能删改。

//go:embed souls/*.md
var builtinSoulFiles embed.FS

// builtinSoulOrder 决定人设库里内置条目的顺序：默认排第一，是新机器人开箱时用的那份。
var builtinSoulOrder = []struct {
	id   string
	file string
}{
	{"builtin:default", "default.md"},
	{"builtin:jiaran", "jiaran.md"},
	{"builtin:human", "human.md"},
	{"builtin:catgirl", "catgirl.md"},
	{"builtin:assistant", "assistant.md"},
	{"builtin:girlfriend", "girlfriend.md"},
	{"builtin:boyfriend", "boyfriend.md"},
}

// defaultSystemPrompt 是没配置任何人设时的兜底 SOUL.md。
var defaultSystemPrompt = mustBuiltinSoul("default.md")

func mustBuiltinSoul(file string) string {
	data, err := builtinSoulFiles.ReadFile("souls/" + file)
	if err != nil {
		panic("assistant: missing builtin soul " + file)
	}
	return strings.TrimSpace(string(data))
}

// BuiltinPersonas 返回内置人设的副本，调用方可以随意改。
func BuiltinPersonas() []Persona {
	personas := make([]Persona, 0, len(builtinSoulOrder))
	for _, item := range builtinSoulOrder {
		text := mustBuiltinSoul(item.file)
		personas = append(personas, Persona{
			ID:           item.id,
			Name:         SoulTitle(text),
			SystemPrompt: text,
			Builtin:      true,
		})
	}
	return personas
}

// IsBuiltinPersonaID 报告这个 ID 是不是内置人设。内置人设只读：改了下次升级
// 就会被新版本的文件盖掉，不如让用户另存一份自己的。
func IsBuiltinPersonaID(id string) bool {
	id = strings.TrimSpace(id)
	for _, item := range builtinSoulOrder {
		if item.id == id {
			return true
		}
	}
	return false
}

// SoulTitle 取 SOUL.md 第一行一级标题当名字；没有标题就返回空串。
func SoulTitle(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
		return ""
	}
	return ""
}
