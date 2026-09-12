// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package platformskill

import _ "embed"

//go:embed SKILL.md
var markdown string

// Markdown returns the canonical built-in platform interface skill instructions.
func Markdown() string {
	return markdown
}
