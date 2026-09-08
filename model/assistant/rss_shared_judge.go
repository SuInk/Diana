package assistant

import (
	"context"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// Hash the complete configured route, including credentials and fallbacks.
// An opaque factory or dynamically supplied OAuth credential cannot prove
// equivalent authorization, so those paths deliberately skip result reuse.
func (r *Runtime) rssJudgeIdentity(ctx context.Context, event MessageEvent) string {
	r.mu.RLock()
	store, registry, factory, epoch := r.llmStore, r.llmRegistry, r.llmCfgFactory, r.llmReuseEpoch
	global := r.cfg
	r.mu.RUnlock()
	if store == nil {
		return ""
	}
	profiles := store.Profiles()
	for _, profile := range profiles.Profiles {
		if profile.Config.OAuthProvider != "" {
			return ""
		}
	}
	if registry == nil {
		if provider, ok := store.(LLMProviderRegistryStore); ok {
			registry, _ = provider.ProviderRegistry()
		}
	}
	if registry == nil && factory == nil {
		return ""
	}
	var document any
	if registry != nil {
		document = registry.Document()
	}
	cfg := r.effectiveConfigForEvent(event)
	override, _ := replyRuleLLMProfileID(ctx)
	return sharedResultKey([]any{"rss-model-route-v1", profiles, document, epoch, override, global.ModelRoles, cfg.ModelRoles, global.MaxContextTokens, cfg.MaxContextTokens, llmIdentityMaskingEnabled(global), llmIdentityMaskingEnabled(cfg)})
}

func (r *Runtime) reuseRSSJudgment(ctx context.Context, source MessageEvent, messages []llm.Message, load func(context.Context) (rssJudgeDecision, error)) (rssJudgeDecision, error) {
	identity := r.rssJudgeIdentity(ctx, source)
	key := ""
	if identity != "" {
		plugin, settings, enabled := r.pluginWithSettingsForEvent(rssWatchPluginID, source)
		if rss, ok := plugin.(*RSSWatchPlugin); ok && enabled && rss.client.Jar == nil {
			key = sharedResultKey([]any{"rss-judge-v1", identity, settings, messages})
		}
	}
	budget := r.effectiveConfigForEvent(source).RequestTimeout
	return r.rssJudgments.load(ctx, key, 10*time.Minute, budget, func(rssJudgeDecision) bool { return identity != "" && identity == r.rssJudgeIdentity(ctx, source) }, load)
}
