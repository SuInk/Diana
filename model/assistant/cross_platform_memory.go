// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"sort"
	"strings"
)

func (r *Runtime) crossPlatformMemoryPrefixes(event MessageEvent, cfg BotConfig) []string {
	if event.Kind != EventKindGroup || !boolValue(cfg.LongTermMemoryEnabled, true) || !boolValue(cfg.CrossPlatformMemoryEnabled, false) {
		return nil
	}
	// Without isolated namespaces there is no reliable provenance for old
	// memories. Do not guess which platform an unnamespaced group belongs to.
	id := strings.TrimSpace(event.ProfileID)
	if id == "" || strings.TrimSpace(event.ContextNamespace) != id {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	var prefixes []string
	for sourceID, source := range r.profileConfigs {
		if sourceID == "" || sourceID == id || !source.Enabled ||
			NormalizePlatformID(source.Platform) == NormalizePlatformID(event.Platform) ||
			!boolValue(source.LongTermMemoryEnabled, true) || !boolValue(source.CrossPlatformMemoryEnabled, false) {
			continue
		}
		prefixes = append(prefixes, sourceID+":group:")
	}
	sort.Strings(prefixes)
	return prefixes
}
