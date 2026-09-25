// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"errors"
	"strings"
)

// 人设文件就是一份 SOUL.md：整份原文就是正文，名字取第一行一级标题。
//
// 之前的人设文件是一份 YAML，正文之外还带品格结构体、自称、语气词、全部内置提示词
// 和判据。现在人设只回答「她是谁」，提示词和判据是机器人自己的设置，不跟着人设走，
// 文件也就没必要再是一份结构化配置。

// PersonaDocument 是解析出来的一个人设文件。
type PersonaDocument struct {
	Personas []Persona `json:"personas"`
}

// ErrPersonaDocumentEmpty 表示文件是空的。
var ErrPersonaDocumentEmpty = errors.New("persona document is empty")

// ParsePersonaMarkdown 把一份 SOUL.md 解析成一套人设。名字取第一行一级标题，没有
// 标题就用 fallbackName（通常是去掉扩展名的文件名）。
func ParsePersonaMarkdown(raw []byte, fallbackName string) (PersonaDocument, error) {
	text := strings.TrimSpace(strings.ReplaceAll(string(raw), "\r\n", "\n"))
	if text == "" {
		return PersonaDocument{}, ErrPersonaDocumentEmpty
	}
	name := SoulTitle(text)
	if name == "" {
		name = strings.TrimSpace(fallbackName)
	}
	persona := Persona{Name: name, SystemPrompt: text}.Normalized()
	return PersonaDocument{Personas: []Persona{persona}}, nil
}
