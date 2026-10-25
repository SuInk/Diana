// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"log"
	"slices"
	"strings"
)

var reasoningEffortOrder = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}

func knownReasoningEfforts(values []string) []string {
	var out []string
	for _, effort := range reasoningEffortOrder {
		if slices.Contains(values, effort) {
			out = append(out, effort)
		}
	}
	return out
}

// ReasoningEfforts uses provider-specific metadata first. For private gateways,
// prefer the canonical publisher, otherwise require catalog entries to agree.
// Unknown/ambiguous models remain unrestricted; never infer limits from a prefix.
func (c *ModelsDevCatalog) ReasoningEfforts(cfg ProviderConfig, model string) []string {
	catalog, apis := c.data()
	for _, provider := range modelsDevProviderIDs(cfg, apis) {
		if info, ok := catalog[provider][model]; ok && len(info.ReasoningEfforts) > 0 {
			return slices.Clone(info.ReasoningEfforts)
		}
	}
	c.reasoningOnce.Do(func() {
		c.reasoningIndex = map[string][]string{}
		c.canonicalReasoningIndex = map[string][]string{}
		for provider, models := range catalog {
			for id, info := range models {
				if len(info.ReasoningEfforts) == 0 {
					continue
				}
				canonicalProvider, canonicalID, _ := strings.Cut(info.CanonicalModelID, "/")
				if canonicalProvider == provider && canonicalID == id {
					c.canonicalReasoningIndex[id] = slices.Clone(info.ReasoningEfforts)
					c.canonicalReasoningIndex[info.CanonicalModelID] = slices.Clone(info.ReasoningEfforts)
				}
				for _, key := range []string{id, bareModelName(id)} {
					previous, exists := c.reasoningIndex[key]
					if !exists {
						c.reasoningIndex[key] = slices.Clone(info.ReasoningEfforts)
					} else if !slices.Equal(previous, info.ReasoningEfforts) {
						c.reasoningIndex[key] = nil
					}
				}
			}
		}
	})
	if efforts := c.canonicalReasoningIndex[model]; len(efforts) > 0 {
		return slices.Clone(efforts)
	}
	if efforts, exists := c.reasoningIndex[model]; exists {
		return slices.Clone(efforts)
	}
	return slices.Clone(c.reasoningIndex[bareModelName(model)])
}

func (cfg ProviderConfig) SupportedReasoningEfforts(model string) []string {
	// These are wire effort values, not Gemini budgets or Anthropic thinking modes.
	if cfg.Provider != ProviderOpenAICompatible || isDeepSeekTarget(cfg, model) {
		return nil
	}
	if efforts := SharedModelsDevCatalog().ReasoningEfforts(cfg, model); len(efforts) > 0 {
		return efforts
	}
	for _, info := range cfg.Models {
		if info.ID == model {
			return knownReasoningEfforts(info.ReasoningEfforts)
		}
	}
	return nil
}

// ResolveReasoningEffort preserves the saved preference and clamps only this
// request, after selecting its actual model (including failover candidates).
func (cfg ProviderConfig) ResolveReasoningEffort(model, requested string) string {
	index := slices.Index(reasoningEffortOrder, requested)
	if index < 0 { // Empty means upstream default; invalid values remain validation errors.
		return requested
	}
	supported := cfg.SupportedReasoningEfforts(model)
	if len(supported) == 0 || slices.Contains(supported, requested) {
		return requested
	}
	effective := supported[len(supported)-1]
	for _, effort := range reasoningEffortOrder[index:] {
		if slices.Contains(supported, effort) {
			effective = effort
			break
		}
	}
	log.Printf("llm: reasoning effort adjusted model=%q requested=%q effective=%q", model, requested, effective)
	return effective
}

// EnrichReasoningModels also decorates old saved lists on read, without requiring
// a new /models request or changing user configuration or context limits.
func EnrichReasoningModels(cfg ProviderConfig, models []ModelInfo) []ModelInfo {
	out := slices.Clone(models)
	for i := range out {
		out[i].ReasoningEfforts = cfg.SupportedReasoningEfforts(out[i].ID)
	}
	return out
}
