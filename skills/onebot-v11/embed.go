// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package onebotv11skill

import _ "embed"

//go:embed SKILL.md
var markdown string

// Markdown returns the canonical built-in OneBot v11 skill instructions.
func Markdown() string {
	return markdown
}
