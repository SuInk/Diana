// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "strings"

// Expand the supported literal at request time; stored personas stay reusable.
// Unknown tokens and inserted names are never recursively evaluated.
func (cfg BotConfig) personaPrompt() string {
	return strings.ReplaceAll(cfg.SystemPrompt, "{{name}}", NormalizeProfileName(cfg.Name))
}
