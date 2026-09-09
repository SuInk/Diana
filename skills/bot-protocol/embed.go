package botprotocol

import _ "embed"

//go:embed SKILL.md
var markdown string

func Markdown() string { return markdown }
