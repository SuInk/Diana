// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/llm"
)

// SetLLMProviderConfigFactory 注入按 profile 配置创建 LLM provider 的工厂。
func (r *Runtime) SetLLMProviderConfigFactory(factory LLMProviderConfigFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.llmCfgFactory = factory
	r.llmReuseEpoch++
}

// SetLLMClientOptions 注入「按配置档取客户端选项」的钩子，和聊天工厂同源（部署时就是
// llm.ClientOptionsFor(cfg, oauthManager)）。
//
// 聊天走的是注入进来的工厂，可生图改图、embedding、拉模型列表、注册表路由这些是
// 运行时自己按配置档直接调 llm 包的，以前一律不带选项：只用 OAuth 登录（没填 API
// Key）的配置档在这些路上拿不到凭据。image 用途没单独配置时沿用 chat 的配置档，于是
// 「能聊天、不能生图」。现在这些调用点统一从这里取选项；没绑 OAuth 的配置档拿到的是
// 空选项，行为和以前完全一致。
func (r *Runtime) SetLLMClientOptions(options func(llm.ProviderConfig) []llm.ClientOption) {
	r.mu.Lock()
	r.llmClientOptions = options
	r.llmReuseEpoch++
	r.mu.Unlock()
}

// llmClientOptionsHook 取当前的选项钩子，可能为 nil。
func (r *Runtime) llmClientOptionsHook() func(llm.ProviderConfig) []llm.ClientOption {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.llmClientOptions
}

// llmClientOptionsFor 按配置档给出建客户端要带的选项。没注入钩子时返回空。
func (r *Runtime) llmClientOptionsFor(cfg llm.ProviderConfig) []llm.ClientOption {
	if hook := r.llmClientOptionsHook(); hook != nil {
		return hook(cfg)
	}
	return nil
}

// bindLLMRegistry 让注册表里由配置档迁移来的提供商也用同一个选项钩子。
// 注册表多半是每次从存储现建的，钉在它身上不会影响别处。
func (r *Runtime) bindLLMRegistry(registry *llm.ProviderRegistry) *llm.ProviderRegistry {
	if registry == nil {
		return nil
	}
	if hook := r.llmClientOptionsHook(); hook != nil {
		registry.SetClientOptions(hook)
	}
	return registry
}

// SetLLMProviderRegistry enables the providerId/modelId architecture while
// leaving legacy profile routing available for bots that have not migrated.
func (r *Runtime) SetLLMProviderRegistry(registry *llm.ProviderRegistry) {
	r.mu.Lock()
	r.llmRegistry = registry
	r.llmReuseEpoch++
	r.mu.Unlock()
}

// SetMessageHistoryStore 注入持久消息历史存储，用于重启后恢复最近群聊上下文。
func (r *Runtime) SetMessageHistoryStore(store MessageHistoryStore) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messageStore = store
	for _, state := range r.groupPromptSessions {
		state.mu.Lock()
		state.invalidated = true
		state.mu.Unlock()
	}
	r.groupPromptSessions = nil
}

// resolveImageForLLM persists short-lived platform media before encoding it for
// a multimodal request. Falling back to the original URL keeps older providers
// working when the local cache cannot fetch a particular image.
func (r *Runtime) resolveImageForLLM(ctx context.Context, imageURL string) string {
	r.mu.RLock()
	store := r.media
	r.mu.RUnlock()
	if store == nil {
		return imageURL
	}
	path, err := store.Fetch(ctx, imageURL)
	if err != nil {
		log.Printf("media: fetch %s failed: %v", redactURLQuery(imageURL), err)
		return imageURL
	}
	dataURL, err := store.DataURL(path)
	if err != nil {
		log.Printf("media: encode %s failed: %v", filepath.Base(path), err)
		return imageURL
	}
	return dataURL
}

// SetLLMModelLister 注入运行时使用的模型列表读取器。
func (r *Runtime) SetLLMModelLister(lister LLMModelLister) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.modelLister = lister
}

// llmModelLister 返回当前模型列表读取器。没注入时用默认实现，凭据同样按配置档取。
func (r *Runtime) llmModelLister() LLMModelLister {
	r.mu.RLock()
	lister := r.modelLister
	r.mu.RUnlock()
	if lister != nil {
		return lister
	}
	return func(ctx context.Context, cfg llm.ProviderConfig) ([]llm.ModelInfo, error) {
		return defaultLLMModelLister(ctx, cfg, r.llmClientOptionsFor(cfg)...)
	}
}

func quotedPromptItems(items []string) string {
	quoted := make([]string, 0, len(items))
	for _, item := range items {
		if item = strings.TrimSpace(item); item != "" {
			quoted = append(quoted, strconv.Quote(item))
		}
	}
	return strings.Join(quoted, "、")
}

// 意图识别的运行时约束。它们跟在（旧版）路由提示词后面，管理员改的是这几段的措辞；
// should_reply、category 这些字段名和取值由路由提示词里的输出格式定义，改动时要对得上。
const (
	routerAnswerabilityGuard    = `运行时强制约束：Intent Recognition（意图识别）只判断消息是否需要进入正式回复，不负责事实准确度审核。明确提问、求助、指派或继续追问应按 needs_response 或 bot_related 放行；不得仅因句子短、当前短上下文不足、术语陌生、需要搜索、需要工具或暂时不知道答案而保持沉默。正式 Agent 会读取完整上下文、搜索或调用工具，生成后的独立准确度审核会在发送前拦截错误答案。answerable 字段只作观察记录，不得作为 should_reply 的前置条件。没有点名机器人不等于不需要回复：面向全群的定义、解释、辨析或求助问题属于 needs_response；承接近期尚未回答的公开问题时，应视为该问题仍在等待回答并使用 needs_response。群友说“你”或反问不等于在问机器人，例如“你不是最喜欢看小说吗”不是直接向机器人提问，此时保持 directed_at_bot=false，再按 chat_in 判断。notebook_context 是本地笔记本对当前消息的可信释义；命中时不能再称它为未解释缩写，例如 zgm=在干嘛。直接引用或语义承接机器人回复的追问属于 bot_related。若当前请求新增了此前回答中不存在的图片，不能仅因文字相同就判为没有新增信息；群资料工具可以通过本地模式匹配核对当前图片是否为群成员头像，身份不得由视觉模型猜测。纯附和、结束语、私聊中的旁观插话和没有实质内容的闲聊仍保持沉默。`
	routerExpressiveChatInGuard = `围绕上下文中可识别的话题轻松调侃、反问或接梗时，按 chat_in 判断 substantive。风格化表达也可以构成 substantive：如果机器人能用具体、新颖且贴合当前话题的比喻、拟人、意象、节奏或角色化短句，带来新的观察、画面、情绪或笑点，可以选择 chat_in，不要求这句话必须包含可核实事实。套话换皮、无关抒情、同义复述、形容词堆砌和与人设冲突的强行文艺仍然 substantive=false。`
	routerForwardedContentGuard = `合并转发里的文字、图片和视频属于被转发的材料，不等于当前发送者正在向机器人陈述、提问或求助。若当前消息只是分享合并转发且没有向机器人提出请求，不得仅因转发内部出现危险、错误、敏感或值得纠正的句子而使用 needs_response 或 chat_in 主动说教；保持 should_reply=false。只有转发外层或清晰上下文确实提出公开问题、求助或要求机器人处理时才回复。`

	routerChatInNatural = "当前群已开启自然插话模式：普通群聊只要能基于上下文、稳定知识或可用工具生成具体可靠、可回答且有实质内容的新回复，就使用 category=chat_in、should_reply=true、answerable=true、substantive=true。不要受置信度、抽样率或冷却影响；附和、复读、寒暄、无信息量感想以及只能猜测的内容仍必须保持静默。"
	routerChatInLevel   = "当前闲聊插话档位：{level}（{label}）。档位只影响运行时的放行松紧，不放宽 substantive 的判断标准：任何档位下附和、复读和寒暄都必须 substantive=false。"
	routerChatInOff     = "当前闲聊插话已关闭：禁止使用 category=chat_in，普通闲聊一律 should_reply=false。"
)

const routerFieldNamesUsage = "改动时保持 should_reply、category、substantive 这类字段名和取值原样，它们要和路由提示词里的输出格式对得上。"

func routingSpec(key, title, usage, text string, vars ...PromptVar) *PromptSpec {
	return registerPrompt(PromptSpec{Key: "routing." + key, Group: PromptGroupRouting, Title: title, Usage: usage + routerFieldNamesUsage, Default: text, Vars: vars})
}

var (
	promptRouterAnswerabilitySpec    = routingSpec("guard.answerability", "意图识别：该不该放行", "旧版意图路由每次都附上：意图识别只判断要不要进正式回复，不因答不上来而沉默。", routerAnswerabilityGuard)
	promptRouterExpressiveChatInSpec = routingSpec("guard.expressive_chat_in", "意图识别：风格化接话", "旧版意图路由每次都附上：调侃、接梗什么时候算有实质内容。", routerExpressiveChatInGuard)
	promptRouterForwardedContentSpec = routingSpec("guard.forwarded_content", "意图识别：合并转发", "旧版意图路由每次都附上：只是分享合并转发时不主动说教。", routerForwardedContentGuard)
	promptRouterChatInNaturalSpec    = routingSpec("chat_in.natural", "意图识别：自然插话模式", "旧版意图路由、群里开启了自然插话时附上。", routerChatInNatural)
	promptRouterChatInLevelSpec      = routingSpec("chat_in.level", "意图识别：闲聊插话档位", "旧版意图路由、开启了闲聊插话时附上，告诉判断模型当前档位。", routerChatInLevel, PromptVar{Name: "level", Description: "闲聊插话档位的取值，如 low"}, PromptVar{Name: "label", Description: "档位的中文名"})
	promptRouterChatInOffSpec        = routingSpec("chat_in.off", "意图识别：闲聊插话关闭", "旧版意图路由、关闭了闲聊插话时附上，封掉 chat_in 分类。", routerChatInOff)
)

func proactiveReplyRouterSystemPrompt(configured string, configs ...BotConfig) string {
	overrides := promptOverridesOf(configs)
	runtimeGuard := overrides.text(promptRouterAnswerabilitySpec) + "\n" + overrides.text(promptRouterExpressiveChatInSpec) + "\n" + overrides.text(promptRouterForwardedContentSpec) + "\n" + overrides.text(promptMessageAddressingSpec)
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return runtimeGuard
	}
	configured = strings.ReplaceAll(configured, "planner", "Intent Recognition")
	configured = strings.ReplaceAll(configured, "严格主动回复路由器", "Intent Recognition（意图识别）")
	return configured + "\n\n" + runtimeGuard
}

// proactiveReplyRouterPromptForChatIn 在关闭闲聊插话时直接封掉 chat_in 分类，避免路由
// 器反复给出一个运行时必然拒绝的结论。social 打开时再补一条社交性回应的放行规则。
//
// configs 传机器人配置时，运行时约束和档位说明读它的覆盖值；不传时用内置默认值。
func proactiveReplyRouterPromptForChatIn(configured, criteria string, chatIn chatInSettings, social bool, configs ...BotConfig) string {
	overrides := promptOverridesOf(configs)
	if chatIn.Participation != nil {
		// 评分档位和口径由 Participation 决定；管理员的补充判据只拼在尾部，评分契约
		// （两项、裸 JSON）不交给用户改。configured 是被取代的旧路由提示词，仍然不读。
		return appendRouterCriteria(chatIn.Participation.promptWith(overrides), criteria, overrides)
	}
	prompt := proactiveReplyRouterSystemPrompt(configured, configs...)
	if chatIn.SuperActive {
		if strings.TrimSpace(configured) == "" || strings.TrimSpace(configured) == defaultProactiveReplyRouterPrompt {
			return overrides.text(promptSuperActiveIntentSpec)
		}
		return prompt + "\n\n" + overrides.text(promptSuperActiveIntentSpec)
	}
	if social {
		prompt += "\n\n" + overrides.text(promptSocialReplyGuardSpec)
	}
	if chatIn.Assistant {
		return prompt + "\n\n" + overrides.text(promptAssistantIntentSpec)
	}
	if chatIn.Natural {
		return prompt + "\n\n" + overrides.text(promptRouterChatInNaturalSpec)
	}
	if chatIn.Enabled {
		return prompt + "\n\n" + overrides.render(promptRouterChatInLevelSpec, map[string]string{
			"level": string(chatIn.Level),
			"label": chatIn.Level.Label(),
		})
	}
	return prompt + "\n\n" + overrides.text(promptRouterChatInOffSpec)
}

func newRuntimeAgentLLMProvider(runtime *Runtime, ctx context.Context) *runtimeAgentLLMProvider {
	return &runtimeAgentLLMProvider{runtime: runtime, ctx: ctx, providers: map[string]LLMProvider{}}
}

func (p *runtimeAgentLLMProvider) providerForGroup(group string) (LLMProvider, error) {
	group = llm.NormalizeProfileGroup(group)
	p.mu.Lock()
	defer p.mu.Unlock()
	if provider := p.providers[group]; provider != nil {
		return provider, nil
	}
	var provider LLMProvider
	_, err := p.runtime.runRawLLMProviderForGroup(p.ctx, group, func(client LLMProvider) (string, error) {
		provider = client
		return "", nil
	})
	if err != nil {
		return nil, err
	}
	if provider == nil {
		return nil, fmt.Errorf("diana: no llm provider is configured for group %q", group)
	}
	p.providers[group] = provider
	return provider, nil
}

// recordLLMUsage 每次调用成功就记一条，不管上游报没报用量、有没有挂在某条消息
// 名下。以前这两种情况直接跳过：中转没回 usage 的调用连调用次数都不算，后台建
// 表情包索引、定时任务这些没有消息 ID 的调用整条消失，统计出来的总量比账单少一截
// 还看不出来少在哪。
func (r *Runtime) recordLLMUsage(ctx context.Context, event MessageEvent, provider llm.Provider, model string, usage llm.Usage, purpose string, duration time.Duration, ttft time.Duration) {
	if usage.TotalTokens <= 0 && (usage.InputTokens > 0 || usage.OutputTokens > 0) {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	}
	// 运行期合计先记：它给总览页读，不该因为没配日志写入器就停掉。
	r.recordLLMUsageTotals(usage)
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	entry := applog.Entry{
		Kind:    applog.KindOperation,
		Level:   applog.LevelInfo,
		Action:  "llm_usage",
		Message: "LLM 调用用量已记录",
		Actor:   oneBotEventActor(event),
		Target:  event.MessageID,
		Metadata: map[string]any{
			"group_id": event.GroupID,
			// profile_id 让同一个群里的两台机器人各算各的额度。
			"profile_id":          event.ProfileID,
			"user_id":             event.UserID,
			"message_id":          event.MessageID,
			"provider":            string(provider),
			"model":               model,
			"purpose":             strings.TrimSpace(purpose),
			"input_tokens":        usage.InputTokens,
			"output_tokens":       usage.OutputTokens,
			"total_tokens":        usage.TotalTokens,
			"cached_input_tokens": usage.CachedInputTokens,
			// duration_ms 是这一次调用的墙钟耗时，tokens_per_second 是它的输出速率。
			// 事件详情里的 duration_ms 说的是整条消息的处理耗时，两者不是一回事，
			// 所以聚合到事件上时那个字段叫 llm_duration_ms。
			"duration_ms":       duration.Milliseconds(),
			"tokens_per_second": TokensPerSecond(usage.OutputTokens, duration),
		},
	}
	// TTFT 只有流式跑通时才有。为 0 时整个键不写：写一个 0 进去，聚合那边分不清
	// 「没开流式」和「首 token 真的是 0 毫秒」。
	if ttft > 0 {
		entry.Metadata["ttft_ms"] = ttft.Milliseconds()
	}
	if usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.TotalTokens == 0 {
		// 调用确实发生了，只是上游没报用量。标出来，免得被当成「这次没花钱」。
		entry.Metadata["usage_missing"] = true
	}
	// 调用方的 ctx 可能正好在这时到期或被取消（带超时的旁路调用很常见），用它写日志
	// 会把刚花掉的用量丢掉。
	logCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_ = writer.AppendLog(logCtx, entry)
}

func (r *Runtime) enrichImagePromptWithChatContext(ctx context.Context, event MessageEvent, prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if event.Kind != EventKindGroup || strings.TrimSpace(event.GroupID) == "" {
		return prompt
	}
	var lines []string
	if group, err := r.getGroupInfoForEvent(ctx, event, event.GroupID); err == nil {
		line := "群聊：" + firstNonEmpty(group.GroupName, group.GroupID)
		if group.GroupID != "" {
			line += " (" + group.GroupID + ")"
		}
		if group.AvatarURL != "" {
			line += "，群头像：" + group.AvatarURL
		}
		lines = append(lines, line)
	}
	if sender, err := r.getGroupMemberInfoForEvent(ctx, event, event.GroupID, event.UserID); err == nil && sender.UserID != "" {
		lines = append(lines, "当前发送者："+sender.DisplayName()+" ("+sender.UserID+")，头像："+sender.AvatarURL)
	}
	cfg := r.effectiveConfigForEvent(event)
	botIDs := map[string]bool{}
	for _, id := range []string{event.SelfID, cfg.BotAccount} {
		if id = strings.TrimSpace(id); id != "" {
			botIDs[id] = true
		}
	}
	for _, userID := range mentionedUserIDs(event.Segments) {
		if botIDs[userID] {
			continue
		}
		member, err := r.getGroupMemberInfoForEvent(ctx, event, event.GroupID, userID)
		if err != nil || member.UserID == "" {
			lines = append(lines, "被@成员："+userID+"，头像："+MemberAvatarURL(r.currentPlatform(event), userID))
			continue
		}
		lines = append(lines, "被@成员："+member.DisplayName()+" ("+member.UserID+")，头像："+member.AvatarURL)
	}
	if len(lines) == 0 {
		return prompt
	}
	return prompt + "\n\n" + r.effectiveConfigForEvent(event).prompt(promptImageChatContextSpec) + "\n" + strings.Join(lines, "\n")
}

const promptImageChatContext = "群聊上下文（仅供理解群名、成员和头像来源；不要在图片中加入文字，除非用户明确要求）："

var promptImageChatContextSpec = registerPrompt(PromptSpec{
	Key:     "media.image_chat_context",
	Group:   PromptGroupMedia,
	Title:   "生图时附带的群聊上下文",
	Usage:   "群聊里生图时，接在画图描述后面、群名和成员头像信息前面，说明这些信息只供理解、别画进图里。",
	Default: promptImageChatContext,
})

func (r *Runtime) runLLMProvider(ctx context.Context, run llmProviderRunFunc) (string, error) {
	return r.runLLMProviderForGroup(ctx, llm.GroupChat, run)
}

func (r *Runtime) runLLMProviderForGroup(ctx context.Context, group string, run llmProviderRunFunc) (string, error) {
	run = withEmojiSemanticsRun(run)
	run = withDecisionOnlyNoticeRun(ctx, run)
	run = r.withLLMIdentityPrivacyRun(ctx, run)
	run = r.withContextBudgetCapRun(ctx, run)
	run = r.withImageBudgetRun(group, run)
	run = r.withDebugTraceRun(ctx, run)
	run = r.withPromptCacheProbeRun(ctx, run)
	run = r.withLLMUsageAccountingRun(ctx, run)
	// Streaming must be the last wrapper added so it sits closest to the real
	// provider. The other decorators expose Generate only and would otherwise
	// hide the provider's Stream method.
	run = r.withLLMStreamingRun(ctx, run)
	return r.runRawLLMProviderForGroup(ctx, group, run)
}

func (r *Runtime) wrapLLMProviderForContext(ctx context.Context, provider LLMProvider) LLMProvider {
	var wrapped LLMProvider
	run := func(client LLMProvider) (string, error) {
		wrapped = client
		return "", nil
	}
	run = withEmojiSemanticsRun(run)
	run = withDecisionOnlyNoticeRun(ctx, run)
	group := ModelBindingGroupOf(llmUsagePurposeFromContext(ctx))
	if group == "" {
		group = llm.GroupChat
	}
	run = r.withLLMIdentityPrivacyRun(ctx, run)
	run = r.withContextBudgetCapRun(ctx, run)
	run = r.withImageBudgetRun(group, run)
	run = r.withDebugTraceRun(ctx, run)
	run = r.withPromptCacheProbeRun(ctx, run)
	run = r.withLLMUsageAccountingRun(ctx, run)
	run = r.withLLMStreamingRun(ctx, run)
	_, _ = run(provider)
	if wrapped == nil {
		return provider
	}
	return wrapped
}

func (r *Runtime) runRawLLMProviderForGroup(ctx context.Context, group string, run llmProviderRunFunc) (string, error) {
	roles := r.modelRolesForContext(ctx)
	r.mu.RLock()
	cfgFactory := r.llmCfgFactory
	factory := r.llmFactory
	store := r.llmStore
	registry := r.llmRegistry
	r.mu.RUnlock()
	if registry == nil {
		if registryStore, ok := store.(LLMProviderRegistryStore); ok {
			registry, _ = registryStore.ProviderRegistry()
		}
	}
	registry = r.bindLLMRegistry(registry)
	if registry != nil && store != nil {
		set := store.Profiles().WithDefaults()
		var profiles []llm.Profile
		if profileID, ok := replyRuleLLMProfileID(ctx); ok {
			for _, profile := range set.Profiles {
				if strings.TrimSpace(profile.ID) == profileID {
					profiles = []llm.Profile{profile}
					break
				}
			}
			if len(profiles) == 0 {
				return "", fmt.Errorf("diana: reply rule llm profile %q not found", profileID)
			}
		} else {
			var roleErr error
			profiles, roleErr = r.roleBoundProfiles(llmUsagePurposeFromContext(ctx), set, group, roles)
			if roleErr != nil {
				return "", roleErr
			}
			if len(profiles) == 0 {
				profiles = llmProfilesInGroup(set, llm.NormalizeProfileGroup(group))
			}
			if len(profiles) == 0 {
				profiles = fallbackProfilesForGroup(set, group)
			}
		}
		if len(profiles) > 0 {
			provider, err := newRegistryFailoverLLMProvider(registry, profiles, true, len(profiles) > 1)
			if err != nil {
				return "", err
			}
			provider.report = r.reportLLMEvent
			return run(provider)
		}
	}

	if cfgFactory != nil && store != nil {
		set := store.Profiles().WithDefaults()
		if profileID, ok := replyRuleLLMProfileID(ctx); ok {
			for _, profile := range set.Profiles {
				if strings.TrimSpace(profile.ID) == profileID {
					return r.runLLMProviderProfileAttempts(ctx, []llm.Profile{profile}, cfgFactory, true, run)
				}
			}
			return "", fmt.Errorf("diana: reply rule llm profile %q not found", profileID)
		}
		profiles, roleErr := r.roleBoundProfiles(llmUsagePurposeFromContext(ctx), set, group, roles)
		if roleErr != nil {
			return "", roleErr
		}
		if len(profiles) > 0 {
			provider, err := newProfileFailoverLLMProvider(profiles, cfgFactory, true, nil, len(profiles) > 1)
			if err != nil {
				return "", err
			}
			provider.report = r.reportLLMEvent
			return run(provider)
		}
		// 没有角色绑定就按本次调用的分组取候选，组内顺序即降级顺序。
		//
		// 这里以前多绕一道：候选来自「激活配置所在的分组」，所以激活的是生图那套时
		// 聊天调用会拿到一串生图配置，得先用 activeProfileForGroup 做一次能力检查再
		// 退回本分组。分组直接由调用方给出之后，那类错配从源头就不成立了。
		groupKey := llm.NormalizeProfileGroup(group)
		if profiles := llmProfilesInGroup(set, groupKey); len(profiles) > 0 {
			logUnboundGroupFallback(roles, group, profiles[0].ID)
			provider, err := newProfileFailoverLLMProvider(profiles, cfgFactory, true, nil, len(profiles) > 1)
			if err != nil {
				return "", err
			}
			provider.report = r.reportLLMEvent
			return run(provider)
		}
		return r.runLLMProviderWithFailover(ctx, store, cfgFactory, run)
	}
	if factory == nil {
		return "", fmt.Errorf("diana: llm provider is not configured")
	}
	client, err := factory()
	if err != nil {
		return "", err
	}
	return run(withTransientLLMRetry(client, true))
}

func (r *Runtime) imageProviderConfigs(contexts ...context.Context) []llm.ProviderConfig {
	r.mu.RLock()
	store := r.llmStore
	r.mu.RUnlock()
	var ctx context.Context
	if len(contexts) > 0 {
		ctx = contexts[0]
	}
	roles := r.modelRolesForContext(ctx)
	if store == nil {
		return nil
	}
	set := store.Profiles().WithDefaults()
	role, explicitImageRole := roles["image"]
	if !explicitImageRole {
		role = roles["chat"]
	}
	if role.ProviderID != "" && role.ModelID != "" {
		role.ProfileID = role.ProviderID
		role.Model = strings.TrimPrefix(role.ModelID, role.ProviderID+":")
	}
	var profiles []llm.Profile
	if role.Group != "" {
		profiles = set.GroupProfiles(role.Group)
	} else if role.ProfileID != "" {
		for _, profile := range set.Profiles {
			if profile.ID == role.ProfileID {
				profiles = []llm.Profile{profile}
				break
			}
		}
	}
	if len(profiles) == 0 {
		if current, ok := set.FirstProfile(); ok {
			profiles = []llm.Profile{current}
		}
	}
	configs := make([]llm.ProviderConfig, 0, len(profiles))
	for _, profile := range profiles {
		cfg := profile.Config.WithDefaults()
		if explicitImageRole {
			cfg.ImageModel = role.Model
		}
		configs = append(configs, cfg)
	}
	return configs
}

func appendLLMMessageText(message llm.Message, suffix string) llm.Message {
	suffix = strings.TrimSpace(suffix)
	if suffix == "" {
		return message
	}
	if strings.TrimSpace(message.Content) == "" {
		message.Content = suffix
	} else {
		message.Content = strings.TrimSpace(message.Content) + "\n\n" + suffix
	}
	for index := range message.Parts {
		if message.Parts[index].Type == llm.ContentPartText {
			message.Parts[index].Text = message.Content
			return message
		}
	}
	if len(message.Parts) > 0 {
		message.Parts = append([]llm.ContentPart{{Type: llm.ContentPartText, Text: message.Content}}, message.Parts...)
	}
	return message
}

func replyRuleLLMProfileID(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	value, _ := ctx.Value(replyRuleContextKey{}).(string)
	value = strings.TrimSpace(value)
	return value, value != ""
}

func (r *Runtime) runLLMRouterProvider(ctx context.Context, run llmProviderRunFunc) (string, error) {
	return r.runLLMRouterProviderWithRetry(ctx, true, run)
}

func (r *Runtime) runLLMRouterProviderOnce(ctx context.Context, run llmProviderRunFunc) (string, error) {
	return r.runLLMRouterProviderWithRetry(ctx, false, run)
}

func (r *Runtime) runLLMRouterProviderWithRetry(ctx context.Context, retryTransient bool, run llmProviderRunFunc) (string, error) {
	roles := r.modelRolesForContext(ctx)
	// 旁路调用以前一律按 intent 分组取候选，用途自己归在哪一组不起作用——因为
	// 「本次调用的分组」排在「用途归属的分组」前面。后台生成拆出来之后这条必须
	// 改：不然记忆抽取、好感度评估照样落在意图识别那一档上，拆了等于没拆。
	group := llm.GroupIntent
	if owner := ModelBindingGroupOf(llmUsagePurposeFromContext(ctx)); owner != "" {
		group = owner
	}
	run = withEmojiSemanticsRun(run)
	run = withDecisionOnlyNoticeRun(ctx, run)
	run = r.withLLMIdentityPrivacyRun(ctx, run)
	run = r.withContextBudgetCapRun(ctx, run)
	run = r.withImageBudgetRun(group, run)
	run = r.withDebugTraceRun(ctx, run)
	run = r.withPromptCacheProbeRun(ctx, run)
	run = r.withLLMUsageAccountingRun(ctx, run)
	r.mu.RLock()
	cfgFactory := r.llmCfgFactory
	factory := r.llmFactory
	store := r.llmStore
	registry := r.llmRegistry
	r.mu.RUnlock()
	if registry == nil {
		if registryStore, ok := store.(LLMProviderRegistryStore); ok {
			registry, _ = registryStore.ProviderRegistry()
		}
	}
	registry = r.bindLLMRegistry(registry)
	if registry != nil && store != nil {
		set := store.Profiles().WithDefaults()
		// 判定链路和对话链路走同一套降级：先把角色绑定连同它的 fallbacks 展开成候选，
		// 交给 registryFailoverLLMProvider 按顺序试。
		//
		// 这里原来只取一条 selection 就直接跑，绑定里配的 fallbacks 从来没被用过——
		// 线上把 intent 绑到只做判断的模型之后，所有没备判断题表的用途整条失败，配好
		// 的降级档一次都没被碰。降级是全局承诺，不该只有对话享有。
		profiles, roleErr := r.roleBoundProfiles(llmUsagePurposeFromContext(ctx), set, group, roles)
		if roleErr != nil {
			return "", roleErr
		}
		if len(profiles) == 0 {
			profiles = llmProfilesInGroup(set, group)
		}
		if len(profiles) == 0 {
			profiles = fallbackProfilesForGroup(set, group)
		}
		if len(profiles) > 0 {
			provider, err := newRegistryFailoverLLMProvider(registry, profiles, retryTransient, len(profiles) > 1)
			if err == nil {
				provider.report = r.reportLLMEvent
				return run(provider)
			}
			// 注册表里没有能对上的模型时不硬顶，退回下面按单条选择的老路。
		}
		selection, ok, err := registrySelectionForGroup(registry, set, roles, llmUsagePurposeFromContext(ctx), group, "")
		if err != nil {
			return "", err
		}
		if ok {
			return run(registryLLMProvider(registry, selection, retryTransient))
		}
	}

	if cfgFactory != nil && store != nil {
		set := store.Profiles().WithDefaults()
		profiles, roleErr := r.roleBoundProfiles(llmUsagePurposeFromContext(ctx), set, group, roles)
		if roleErr != nil {
			return "", roleErr
		}
		if len(profiles) > 0 {
			// retryTransient=false 的含义是「同一档不因瞬时错误重试」，不是「不许降级」。
			// 这里原来会把候选截成一条，于是摘要、语义承接这些走 Once 变体的用途根本
			// 没有降级可言。
			return r.runLLMProviderProfileAttempts(ctx, profiles, cfgFactory, retryTransient, run)
		}
		for _, group := range semanticRouteProfileGroups {
			profiles := llmProfilesInGroup(set, group)
			if len(profiles) == 0 {
				continue
			}
			return r.runLLMProviderProfileAttempts(ctx, profiles, cfgFactory, retryTransient, run)
		}
		if current, ok := set.FirstProfile(); ok {
			return r.runLLMProviderProfileAttempts(ctx, []llm.Profile{current}, cfgFactory, retryTransient, run)
		}
		return "", fmt.Errorf("diana: no llm profile is configured")
	}
	if factory == nil {
		return "", fmt.Errorf("diana: llm provider is not configured")
	}
	client, err := factory()
	if err != nil {
		return "", err
	}
	return run(withTransientLLMRetry(client, retryTransient))
}

func llmProfilesInGroup(set llm.ProfileSet, group string) []llm.Profile {
	group = llm.NormalizeProfileGroup(group)
	profiles := make([]llm.Profile, 0, len(set.Profiles))
	for _, profile := range set.Profiles {
		if llm.NormalizeProfileGroup(profile.Group) != group {
			continue
		}
		profile.Group = llm.NormalizeProfileGroup(profile.Group)
		profile.Config = profile.Config.WithDefaults()
		profiles = append(profiles, profile)
	}
	return profiles
}

func (r *Runtime) runLLMProviderProfileAttempts(ctx context.Context, profiles []llm.Profile, factory LLMProviderConfigFactory, retryTransient bool, run llmProviderRunFunc) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	provider, err := newProfileFailoverLLMProvider(profiles, factory, retryTransient, nil, false)
	if err != nil {
		return "", err
	}
	provider.report = r.reportLLMEvent
	return run(provider)
}

func (r *Runtime) runLLMProviderWithFailover(ctx context.Context, store LLMProfileStore, factory LLMProviderConfigFactory, run llmProviderRunFunc) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	set := store.Profiles().WithDefaults()
	// 候选以前来自「激活配置所在的分组」，且从激活那条开始绕圈；降级成功后还会把
	// 激活项写回，于是列表顺序和实跑顺序对不上。现在退到默认分组、按列表顺序走，
	// 默认分组也空了才拿第一条兜底。
	attempts := llmProfilesInGroup(set, llm.GroupChat)
	if len(attempts) == 0 {
		attempts = fallbackProfilesForGroup(set, llm.GroupChat)
	}
	if len(attempts) == 0 {
		return "", fmt.Errorf("diana: no llm profile is configured")
	}
	provider, err := newProfileFailoverLLMProvider(attempts, factory, true, nil, true)
	if err != nil {
		return "", err
	}
	provider.report = r.reportLLMEvent
	return run(provider)
}

func shouldFailoverLLMError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, llm.ErrUnverifiedRejection) {
		return true
	}
	// 绑到只做判断的模型、而这个用途要的是文本，属于能力不匹配，不是上游故障——
	// 正好是降级链该接手的情况。不降级的话，把 intent 整组绑到判断模型就会让所有
	// 没备判断题表的判定用途（语义承接、发送前审核、记忆抽取…）整条失败，而不是
	// 退到下一档对话模型。
	if errors.Is(err, llm.ErrDecisionRequired) {
		return true
	}
	if errors.Is(err, errContentPolicyRejection) || isContentPolicyRejection(err) {
		return false
	}
	if isModelUnavailableLLMError(err) {
		return true
	}
	if errors.Is(err, llm.ErrCompletionHasNoText) {
		return false
	}
	if errors.Is(err, llm.ErrCompletionTruncatedNoText) {
		return true
	}
	if errors.Is(err, llm.ErrCompletionEmpty) {
		return true
	}
	if shouldRetryTransientLLMError(err) {
		return true
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{
		"401", "403", "429",
		"unauthorized", "forbidden", "too many requests",
		"api key", "apikey", "authentication", "auth",
		"permission", "permission_error",
		"quota", "insufficient_quota", "billing", "credit",
		"rate limit", "rate_limit",
		"未授权", "无权限", "额度", "限流", "失效", "无效",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func shouldRetryTransientLLMError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errContentPolicyRejection) || isContentPolicyRejection(err) {
		return false
	}
	if errors.Is(err, llm.ErrCompletionHasNoText) || errors.Is(err, llm.ErrCompletionTruncatedNoText) {
		return false
	}
	if errors.Is(err, llm.ErrCompletionEmpty) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{
		"502", "503", "504",
		"bad gateway", "service unavailable", "gateway timeout",
		"cloudflare",
		"context deadline exceeded", "client.timeout exceeded", "timeout awaiting response headers",
		"eof", "connection reset", "connection refused", "connection aborted",
		"unexpected end of file", "server closed idle connection",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// systemPrompt 组合系统提示词和插件上下文。
func (r *Runtime) systemPrompt(event MessageEvent, pluginResponses []PluginResponse) string {
	return r.systemPromptWithMode(event, pluginResponses, false)
}

func (r *Runtime) systemPromptWithMode(event MessageEvent, pluginResponses []PluginResponse, proactiveTriggered bool) string {
	return r.systemPromptWithRelationship(event, pluginResponses, proactiveTriggered, relationshipPolicyForEvent(r.effectiveConfigForEvent(event), UserMemoryProfile{}, event))
}

func (r *Runtime) systemPromptWithRelationship(event MessageEvent, pluginResponses []PluginResponse, proactiveTriggered bool, relationship RelationshipPolicy) string {
	return r.systemPromptWithRelationshipAndAgent(event, pluginResponses, proactiveTriggered, relationship, r.effectiveConfigForEvent(event).AgentEnabled)
}

func (r *Runtime) systemPromptWithRelationshipAndAgent(event MessageEvent, pluginResponses []PluginResponse, proactiveTriggered bool, relationship RelationshipPolicy, agentEnabled bool) string {
	return r.systemPromptWithRelationshipAndAgentTools(event, pluginResponses, proactiveTriggered, relationship, agentEnabled, nil)
}

// 尾部实时时钟与时区的几段文案。时钟那句的开头 agent.RuntimeClockMarker 是 agent
// 用来认出「调用方已经自带时钟」的标记，由代码拼在前面，不进可覆盖的正文。
const (
	promptRuntimeClock            = "{datetime}（时区 {zone}，UTC{utc_offset}）。这是机器人所在机器提供的可信实时时间；用户询问当前日期或几点时直接据此回答，不要猜测训练数据日期，也不要声称无法访问实时时钟。"
	promptSpeakerTimezone         = "当前发言者所在时区：{timezone}（{zone}，UTC{utc_offset}，{offset}）；他那边现在是 {local_time}。跟他说时间点时按他的当地时间说并标明是他那边的时间，必要时再补一句你这边的时间；换算由你来做，不要让对方自己换。你自己的「现在」仍以上面的运行时钟为准。{recorded_note}对方说出自己那边的当地时间、或说自己在别的地方，和这条记录对不上时，以他当下说的为准，不要拿旧记录纠正他。"
	promptSpeakerTimezoneRecorded = "这条时区记于 {recorded_date}（{age}前），不是实时位置。"
	promptSpeakerTimezoneStale    = "记录较旧，约具体时间前先自然地确认一句他现在在哪个时区。"
)

func tailSpec(key, title, usage, text string, vars ...PromptVar) *PromptSpec {
	return registerPrompt(PromptSpec{Key: "reply." + key, Group: PromptGroupReplyTail, Title: title, Usage: usage, Default: text, Vars: vars})
}

var (
	promptRuntimeClockSpec = tailSpec("clock", "可信实时时钟", "「注入时间」打开时紧跟在当前时间那行后面，告诉模型这是可信的实时时间。",
		promptRuntimeClock,
		PromptVar{Name: "datetime", Description: "机器人所在机器的当前时间，如 2026-09-23 14:05:00"},
		PromptVar{Name: "zone", Description: "时区缩写，如 CST"},
		PromptVar{Name: "utc_offset", Description: "相对 UTC 的偏移，如 +08:00"})
	promptSpeakerTimezoneSpec = tailSpec("speaker_timezone", "发言者的时区", "画像里记过发言者时区时注入，给出他那边的当地时间，要求按他的时间说话。",
		promptSpeakerTimezone,
		PromptVar{Name: "timezone", Description: "时区名，如 America/New_York"},
		PromptVar{Name: "zone", Description: "时区缩写，如 EDT"},
		PromptVar{Name: "utc_offset", Description: "相对 UTC 的偏移，如 -04:00"},
		PromptVar{Name: "offset", Description: "和机器人这边的时差说明"},
		PromptVar{Name: "local_time", Description: "他那边的当前时间，如 2026-09-23 02:05"},
		PromptVar{Name: "recorded_note", Description: "「时区记于何时」那一句，没有记录时间时为空"})
	promptSpeakerTimezoneRecordedSpec = tailSpec("speaker_timezone.recorded", "发言者时区的记录时间", "发言者时区有记录时间时，填进上一段的 {recorded_note}，提醒这不是实时位置。",
		promptSpeakerTimezoneRecorded,
		PromptVar{Name: "recorded_date", Description: "记下时区的日期，如 2026-03-01"},
		PromptVar{Name: "age", Description: "距今多久，如 3 个月"})
	promptSpeakerTimezoneStaleSpec = tailSpec("speaker_timezone.stale", "发言者时区记录较旧", "时区记录超过一定时间时接在记录时间后面，提醒约时间前先确认。", promptSpeakerTimezoneStale)
)

// runtimeClockPrompt 返回本轮的可信实时时间提示。返回值每次调用都不同，只能作为尾部
// 独立 system 消息注入；拼进人设提示词会让那段最长的前缀每秒失效一次。
func (r *Runtime) runtimeClockPrompt(event MessageEvent) string {
	cfg := r.effectiveConfigForEvent(event)
	if !boolValue(cfg.PromptInjectTime, true) {
		return ""
	}
	now := r.clock()
	zoneName, zoneOffset := now.Zone()
	var builder strings.Builder
	builder.WriteString(renderPromptTemplate(cfg.prompt(promptTimeTemplateSpec), map[string]string{
		"datetime": now.Format("2006-01-02 15:04:05"),
		"weekday":  chineseWeekday(now.Weekday()),
	}))
	appendPromptSection(&builder, agent.RuntimeClockMarker+cfg.promptf(promptRuntimeClockSpec, map[string]string{
		"datetime":   now.Format("2006-01-02 15:04:05"),
		"zone":       zoneName,
		"utc_offset": formatUTCOffset(zoneOffset),
	}))
	if speaker := r.speakerTimezonePrompt(event, now); speaker != "" {
		appendPromptSection(&builder, speaker)
	}
	return strings.TrimSpace(builder.String())
}

// speakerTimezonePrompt 在画像里记过对方时区时，给出他那边的当地时间和时差。
// 机器人自己的「现在几点」仍然只看运行时钟，也就是本机时区。
func (r *Runtime) speakerTimezonePrompt(event MessageEvent, now time.Time) string {
	if !event.userProfileLoaded {
		return ""
	}
	location, recordedAt := PortraitTimezoneWithRecordedAt(event.userProfile.Portrait)
	if location == nil {
		return ""
	}
	cfg := r.effectiveConfigForEvent(event)
	local := now.In(location)
	zoneName, zoneOffset := local.Zone()
	// 人会搬家、会出差：这条时区是过去某一次对话记下的，不是实时定位。
	recorded := ""
	if !recordedAt.IsZero() {
		recorded = cfg.promptf(promptSpeakerTimezoneRecordedSpec, map[string]string{
			"recorded_date": recordedAt.In(now.Location()).Format("2006-01-02"),
			"age":           formatApproximateAge(now.Sub(recordedAt)),
		})
		if now.Sub(recordedAt) >= PortraitTimezoneStaleAfter {
			recorded += cfg.prompt(promptSpeakerTimezoneStaleSpec)
		}
	}
	return cfg.promptf(promptSpeakerTimezoneSpec, map[string]string{
		"timezone":      location.String(),
		"zone":          zoneName,
		"utc_offset":    formatUTCOffset(zoneOffset),
		"offset":        FormatTimezoneOffset(now, location, now.Location()),
		"local_time":    local.Format("2006-01-02 15:04"),
		"recorded_note": recorded,
	})
}

// 闲聊插话那一轮追加在主动接话说明后面的两段。
const (
	promptChatInReply       = "本次回复是主动插话，已根据用户发言偏好决定参与。顺着当前话题自然回应，可以接梗、表达感受或回答问题，不要求增加新知识。遵守人设和用户要求，不复读、不编造事实。"
	promptChatInNoAgreement = "如果这一轮唯一能做的事只是赞同一个你无法核实的判断，就别发：要么说出一件你确实知道的具体的事，要么放弃这次插话。不要用「确实」「没错」开头去附和一个无法核实的判断，也不要给它补充听起来内行但没有依据的理由。别人凭印象下的结论，你没有证据就是没有证据，说不清楚就直说不确定。"
)

var (
	promptChatInReplySpec       = ruleSpec("chat_in_reply", "闲聊插话说明", "机器人闲聊插话的那一轮注入：顺着话题自然回应，不要求新知识。", promptChatInReply)
	promptChatInNoAgreementSpec = ruleSpec("chat_in_no_agreement", "插话别空口附和", "机器人闲聊插话的那一轮注入：只能附和一个无法核实的判断时就别发。", promptChatInNoAgreement)
)

// systemPromptWithRelationshipAndAgentTools 返回整段系统提示词（稳定头部 + 发言者
// 尾部），给只发一条 system 消息的旁路（定时订阅、后续评论）和测试用。主回复链路
// 用 systemPromptPartsWithRelationshipAndAgentTools 把两段分开放。
func (r *Runtime) systemPromptWithRelationshipAndAgentTools(event MessageEvent, pluginResponses []PluginResponse, proactiveTriggered bool, relationship RelationshipPolicy, agentEnabled bool, registry *agent.ToolRegistry) string {
	head, tail := r.systemPromptPartsWithRelationshipAndAgentTools(event, pluginResponses, proactiveTriggered, relationship, agentEnabled, registry)
	return joinPromptSections(head, tail)
}

// systemPromptPartsWithRelationshipAndAgentTools 把系统提示词拆成两段：
//
//   - head 只依赖机器人配置、本群配置和本轮注册的工具：同一个群里不管谁说话、
//     说什么，它逐字节相同。它作为第一条 system 消息发出，供应商的前缀缓存
//     （tools → system → messages）从它开始命中，后面的历史才有机会一起命中。
//   - tail 随「谁在说话、这条说了什么」变化：权限档位、主人专属工具规则、主动接话
//     与闲聊插话说明、发言者昵称、命中的别名、时段与心情语气、语气锚点。它由调用方作为独立 system
//     消息放在历史之后、当前消息之前。以前这段直接拼在同一条 system 里，换一个
//     人说话整条 system 就变，Anthropic / Gemini / Responses 把 system 放在所有
//     消息之前，system 一变，几千 token 的历史缓存也跟着全部作废。
//
// 语气锚点留在 tail 末尾的理由和以前一样：离生成越近越管用，现在它离得更近了。
func (r *Runtime) systemPromptPartsWithRelationshipAndAgentTools(event MessageEvent, pluginResponses []PluginResponse, proactiveTriggered bool, relationship RelationshipPolicy, agentEnabled bool, registry *agent.ToolRegistry) (string, string) {
	cfg := r.effectiveConfigForEvent(event)
	var builder strings.Builder
	// tail 收集随发言者权限档位变化的段落（主人专属工具规则、按好感度解锁的日程
	// 工具规则）。注入条件保持原样，只是不写进 head：夹在中间会让它后面几千 token
	// 的稳定规则永远命中不了供应商的前缀缓存。
	var tail strings.Builder
	tail.WriteString(addressingPrompt(event, cfg))
	hasTool := func(name string) bool {
		if registry == nil {
			return true
		}
		_, ok := registry.Get(name)
		return ok
	}
	// 订阅工具合成一个之后，「本轮有没有某一种订阅」不能再靠工具名判断：三种都在
	// subscription 里，github 那种是否可用由构造时收没收进 backends 决定。
	hasSubscriptionKind := func(kind string) bool {
		if registry == nil {
			return true
		}
		tool, ok := registry.Get(dianaSubscriptionToolName)
		if !ok {
			return false
		}
		subscription, ok := tool.(*dianaSubscriptionTool)
		if !ok {
			return false
		}
		return slicesContains(subscription.kinds(), kind)
	}
	hasAnyTool := func(names ...string) bool {
		for _, name := range names {
			if hasTool(name) {
				return true
			}
		}
		return false
	}
	// SOUL.md 排在整条系统提示词的最前面，不加任何包装：她是谁、在乎什么、为什么，
	// 后面所有规则都在它的框架里读。
	builder.WriteString(cfg.SystemPrompt)
	appendPromptSection(&builder, replyPresentationPrompt(!chatSplitLimitsForEvent(cfg, event).SingleMessage, cfg))
	appendPromptSection(&builder, replyLineBreakPrompt(cfg))
	appendPromptSection(&builder, replyLineSplitPrompt(chatSplitLimitsForEvent(cfg, event)))
	// 实时时钟不再拼进人设提示词：它每秒都不同，会让这段最长的 system 提示词永远
	// 无法命中供应商的前缀缓存。改由 runtimeClockPrompt 作为尾部独立 system 消息注入。
	if boolValue(cfg.PromptChineseSlangHint, true) {
		appendPromptSection(&builder, cfg.prompt(promptChineseSlangSpec))
	}
	if event.Kind == EventKindGroup {
		// 场景说明分「被触发」和「主动接话」两串：后者那一轮没人点名机器人，
		// 再说「只有被提到才回复」会和下面的主动插话说明当场打架。
		builder.WriteString("\n" + groupScopePrompt(event, cfg))
		builder.WriteString("\n" + cfg.prompt(promptGroupOwnerDistinctionSpec))
	}
	// 称呼不分群聊私聊：私聊里没有触发这回事，但「别人怎么叫你」仍然是身份的一部分。
	if aliases := quotedPromptItems(cfg.GroupTriggers); aliases != "" {
		builder.WriteString("\n" + cfg.promptf(promptAliasSpec, map[string]string{"aliases": aliases}))
	}
	if agentEnabled && relationship.Owner && hasTool("llm_config") {
		tail.WriteString("\n" + cfg.prompt(promptToolLLMConfigSpec))
	}
	if agentEnabled && hasTool(dianaGitHubToolName) {
		builder.WriteString("\n" + cfg.prompt(promptToolRepositoryIssuesSpec))
	}
	if agentEnabled && hasTool(dianaPlatformToolName) {
		builder.WriteString("\n" + cfg.prompt(promptToolPlatformSpec))
	}
	// 这条对所有人逐字相同（能不能指定别人或指定群由工具自己判身份），所以进稳定头部。
	if agentEnabled && hasTool(dianaCrossSessionToolName) {
		builder.WriteString("\n" + cfg.prompt(promptToolCrossSessionSpec))
	}
	// 破坏性动作只对主人出现在工具 schema 里；提示词也只对主人注入，且必须进随发言者
	// 变化的尾部，不能写进按前缀缓存的稳定头部（否则主人和普通成员的提示词会提前分叉）。
	if agentEnabled && relationship.Owner && hasTool(dianaPlatformToolName) {
		tail.WriteString("\n" + cfg.prompt(promptToolPlatformModerationSpec))
	}
	if agentEnabled && relationship.Owner && hasTool(dianaOneBotRequestsToolName) {
		tail.WriteString("\n" + cfg.prompt(promptToolOneBotRequestsSpec))
	}
	if agentEnabled && hasTool(dianaHistoryImagesToolName) {
		builder.WriteString("\n" + cfg.prompt(promptToolHistoryImagesSpec))
	}
	if agentEnabled && hasTool(dianaMemoryToolName) {
		builder.WriteString("\n" + cfg.prompt(promptToolMemorySpec))
	}
	if agentEnabled && hasAnyTool(dianaChatHistoryToolName, dianaHistoryImagesToolName) {
		builder.WriteString("\n" + cfg.prompt(promptInternalIdentifiersSpec))
		// 引用被管理员关掉时不教这一手：那是「永不带引用」的明确配置。
		if replyReferenceMode(cfg) != ReplyDecorationOff {
			builder.WriteString("\n" + cfg.prompt(promptQuoteHistoryMessageSpec))
		}
	}
	if agentEnabled && relationship.Owner && hasTool("relationship") {
		tail.WriteString("\n" + cfg.prompt(promptOwnerRelationshipTargetSpec))
	}
	if agentEnabled && relationship.Owner && hasAnyTool("tasks", "reminder", dianaSubscriptionToolName) {
		tail.WriteString("\n" + cfg.prompt(promptOwnerTaskTargetSpec))
	}
	// 任务工具规则进稳定头部：AllowPersonalSchedule 在每个关系等级都是 true
	//（见 RelationshipPolicyFor），所以这几段对谁都注入，只随本轮注册了哪些工具
	// 变化——和头部其余工具规则的性质完全一样。它们以前跟着「按好感度解锁」的
	// 假设待在尾部，实测占尾部 436 token 里的绝大部分，等于每条消息都重发一遍
	// 一段人人相同的文本，且永远命不中前缀缓存。
	if agentEnabled && relationship.AllowPersonalSchedule && hasTool("reminder") {
		builder.WriteString("\n" + cfg.prompt(promptTaskReminderSpec))
	}
	if agentEnabled && relationship.AllowPersonalSchedule && hasTool(dianaEventTriggerToolName) {
		builder.WriteString("\n" + cfg.prompt(promptTaskEventTriggerSpec))
	}
	if agentEnabled && relationship.AllowPersonalSchedule && hasSubscriptionKind(subscriptionKindSchedule) {
		builder.WriteString("\n" + cfg.prompt(promptTaskScheduleSpec))
	}
	if agentEnabled && relationship.AllowPersonalSchedule && hasSubscriptionKind(subscriptionKindRSS) {
		builder.WriteString("\n" + cfg.prompt(promptTaskRSSSpec))
	}
	if agentEnabled && relationship.AllowPersonalSchedule && hasTool("tasks") {
		builder.WriteString("\n" + cfg.prompt(promptTaskListSpec))
	}
	if agentEnabled && hasSubscriptionKind(subscriptionKindGitHub) {
		builder.WriteString("\n" + cfg.prompt(promptTaskRepositoryWatchSpec))
	}
	if agentEnabled && relationship.AllowPersonalSchedule && hasAnyTool("tasks", "reminder", dianaSubscriptionToolName) {
		builder.WriteString("\n" + cfg.prompt(promptTaskNoSubstituteSpec))
	}
	// 模型身份的规则在 everyone 下对谁都一样，进 head；owner 下随发言者是不是
	// 主人分叉，进 tail，免得主人和普通成员的前缀提前分叉。
	switch everyone := normalizeModelDisclosure(cfg.ModelDisclosure) == ModelDisclosureEveryone; {
	case !everyone && !relationship.Owner:
		tail.WriteString("\n" + cfg.prompt(promptModelUndisclosedSpec))
	case !agentEnabled || !hasTool(dianaRuntimeModelToolName):
	case everyone:
		builder.WriteString("\n" + cfg.prompt(promptToolRuntimeModelSpec))
	default:
		tail.WriteString("\n" + cfg.prompt(promptToolRuntimeModelSpec))
	}
	// 项目地址的披露规则和模型身份同理：everyone 下对谁都一样，进 head；owner 下
	// 随发言者是不是主人分叉，进 tail，免得主人和普通成员的前缀提前分叉。
	switch everyone := normalizeRepositoryDisclosure(cfg.RepositoryDisclosure) == RepositoryDisclosureEveryone; {
	case !agentEnabled || !hasTool(dianaVersionToolName):
	case everyone:
		builder.WriteString("\n" + cfg.prompt(promptToolVersionSpec))
	case relationship.Owner:
		tail.WriteString("\n" + cfg.prompt(promptToolVersionSpec))
	default:
		tail.WriteString("\n" + cfg.prompt(promptToolVersionNoRepositorySpec))
	}
	if agentEnabled && hasTool(dianaNotebookToolName) {
		builder.WriteString("\n" + cfg.prompt(promptToolNotebookSpec))
	}
	if agentEnabled && hasTool(dianaCodingToolName) {
		builder.WriteString("\n" + cfg.prompt(promptToolCodingSpec))
	}
	if agentEnabled && r.threadStateStore() != nil && hasTool(dianaThreadStateToolName) {
		builder.WriteString("\n" + cfg.prompt(promptToolThreadStateSpec))
	}
	// 自述的规则进 head：开关是机器人配置，对同一个群里的所有人逐字相同。
	if agentEnabled && hasTool(dianaSelfNoteToolName) {
		builder.WriteString("\n" + cfg.prompt(promptToolSelfNoteSpec))
	}
	if agentEnabled && hasTool("capabilities") {
		builder.WriteString("\n" + cfg.prompt(promptToolCapabilitiesSpec))
	}
	groupEvent := groupToolEventForConfig(event, cfg)
	if agentEnabled && hasTool(r.groupToolName(groupEvent)) {
		builder.WriteString("\n" + r.groupToolPrompt(groupEvent, cfg))
	}
	if agentEnabled && hasTool(botParticipationToolName) {
		builder.WriteString("\n" + cfg.prompt(promptToolBotConfigSpec))
	}
	if agentEnabled && hasTool(replyBlockToolName) {
		builder.WriteString("\n" + cfg.prompt(promptToolReplyBlockSpec))
	}
	if agentEnabled && hasTool("relationship") {
		builder.WriteString("\n" + cfg.prompt(promptToolRelationshipListSpec))
		builder.WriteString("\n" + cfg.prompt(promptToolRelationshipQuerySpec))
		builder.WriteString("\n" + cfg.prompt(promptToolRelationshipPortraitSpec))
		// 恋爱模式的规则跟着配置走：同一台机器人整段稳定，不影响前缀缓存。
		// 关着时一个字不注入——模型不知道有这回事，被表白就按普通关系自然回应。
		if boolValue(cfg.RomanceEnabled, false) {
			builder.WriteString("\n" + cfg.prompt(promptToolRelationshipRomanceSpec))
		}
	}
	if agentEnabled && hasTool(dianaImageToolName) {
		builder.WriteString("\n" + cfg.prompt(promptToolImageSpec))
	}
	if agentEnabled && hasTool("tts") {
		builder.WriteString("\n" + cfg.prompt(promptToolTTSSpec))
	}
	builder.WriteString("\n" + cfg.prompt(promptRelationshipTierSpec) + cfg.prompt(promptIdentityAuthoritySpec))
	builder.WriteString("\n" + cfg.prompt(promptLongTermMemorySpec))
	builder.WriteString("\n" + refusalStrategyPrompt(cfg.RefusalStrategy, cfg))
	if agentEnabled {
		// 静默只有 agent_finalize 这一个出口，没开 Agent 时说了也做不到。
		// 它逐字不变，跟着拒答规则一起留在稳定头部：两条规则读在一起，模型才
		// 分得清「不说话」和「拒绝」不是一回事。
		builder.WriteString("\n" + cfg.prompt(promptSilentFinishSpec))
	}
	builder.WriteString("\n" + cfg.prompt(promptToolFindingsSpec))
	builder.WriteString("\n" + cfg.prompt(promptSelfCharacterizationSpec))
	builder.WriteString("\n" + cfg.prompt(promptCurrentMessageSpec))
	builder.WriteString("\n" + cfg.prompt(promptHistoryFormatSpec))
	builder.WriteString("\n" + cfg.prompt(promptAdjacentSupplementSpec))
	if boolValue(cfg.PromptInjectPlaintextRules, true) {
		appendPromptSection(&builder, platformOutputRulesForConfig(cfg))
	}
	// 主动接话和闲聊插话的说明逐条消息变化：同一个群里这条是被点名、下一条是主动
	// 接话，以前写在 head 里，head 一变，后面整段历史的前缀缓存就跟着作废。放进
	// tail，和下面的识图说明一样按「这一轮是什么情况」注入。
	if proactiveTriggered {
		tail.WriteString("\n")
		tail.WriteString(strings.TrimSpace(cfg.prompt(promptProactiveReplySpec)))
		tail.WriteString("\n" + cfg.prompt(promptProactiveToolResultSpec))
	}
	if event.chatInReply {
		tail.WriteString("\n" + cfg.prompt(promptChatInPacingSpec))
		tail.WriteString("\n" + cfg.prompt(promptChatInReplySpec))
		// 线上 6% 的插话以「确实/没错/对，/是的」开头：模型无话可说时最省力的出路
		// 就是赞同对方，再给这个无法核实的判断补一段听起来内行的理由。
		tail.WriteString("\n" + cfg.prompt(promptChatInNoAgreementSpec))
	}
	if eventCarriesImages(event) {
		// 逐条消息变化，压到尾部，别把前面几千 token 的稳定规则挤出前缀缓存。
		tail.WriteString("\n" + cfg.prompt(promptImageReplySpec))
	}
	for _, resp := range pluginResponses {
		if strings.TrimSpace(resp.Context) == "" {
			continue
		}
		builder.WriteString("\n" + cfg.prompt(promptPluginAuthoritySpec))
		break
	}
	// 会变的内容全部进 tail，按易变程度从低到高排列：权限档位段落和发送者昵称在
	// 同一发言者的连续消息之间保持稳定，命中别名则逐条消息都不同。tail 由调用方放
	// 在历史之后，所以这里怎么变都不影响 head 和历史的前缀缓存。
	appendPromptSection(&tail, relationshipPermissionContext(relationship, cfg))
	if event.Kind == EventKindGroup {
		if boolValue(cfg.PromptInjectGroupSender, true) {
			appendPromptSection(&tail, renderPromptTemplate(cfg.prompt(promptGroupSenderSpec), map[string]string{
				"sender": promptSenderIdentity(event),
			}))
		}
		if matched := quotedPromptItems(matchedGroupAliases(event, cfg, event.RawMessage)); matched != "" {
			appendPromptSection(&tail, cfg.promptf(promptMatchedAliasSpec, map[string]string{"aliases": matched}))
		}
	}
	// 本群消息长度和心情语气紧挨着锚点注入，理由和锚点一样：都是「此刻怎么说」，
	// 离生成越近越管用。
	appendPromptSection(&tail, r.groupLengthNormPrompt(event, cfg))
	appendPromptSection(&tail, r.moodToneForConfig(cfg, event.ProfileID))
	// 语气锚点必须留在最后：前面的工具规则、权限说明和拒答流程都是公文体，离生成
	// 最近的一段最容易被模仿，这里重新把语域拉回配置的表达风格。
	appendPromptSection(&tail, personaClosingAnchor(cfg))
	return builder.String(), strings.TrimSpace(tail.String())
}

// joinPromptSections 用换行拼接非空段落。
func joinPromptSections(sections ...string) string {
	var builder strings.Builder
	for _, section := range sections {
		section = strings.TrimSpace(section)
		if section == "" {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString(section)
	}
	return builder.String()
}

// replyMentionPrompt 说明「怎么 @ 别人」：候选名单和 CQ at 的写法。
//
// 「要不要 @ 当前发言者」不在这里,由 replyDecorationPrompt 按本轮的装饰件模式
// 单独给出。两段提示词曾经各说各的:这一段写死「发送层会引用并 @ 当前发言者,
// 这部分不需要你输出 CQ at」,那句话只有 on 档成立;而 auto 档发送层一个装饰件
// 都不加,另一段却在请模型自己写 @。模型两段都收到,前一段是陈述句("发送层会
// 做"),后一段是选择题,于是按前一段理解——不输出 CQ at,发送层也没加,@ 就消失了。
// 「该 @ 的时候也不 @」是这么来的,不是模型判断保守。
//
// 现在描述发送层行为的那几句按模式给:on 档照旧说会自动加,auto/off 档明说不会,
// 谁也不再替另一段做决定。
func (r *Runtime) replyMentionPrompt(cfg BotConfig, event MessageEvent, history []MessageEvent) string {
	if event.Kind != EventKindGroup {
		return ""
	}
	candidates := r.replyMentionCandidates(event, history)
	if len(candidates) == 0 {
		return ""
	}
	payload, err := json.Marshal(candidates)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprintf(`

	【群聊真实提及规则】
	发送层支持真正的 @。正文内容和 @ 对象必须由你在同一次最终回复中统一决定，禁止按姓名关键词机械匹配。
	可提及成员候选 JSON：%s
	1. %s
	2. 如果当前发言者只是通过触发词或 @ 叫你回应另一位成员，不要为了礼貌额外 @ 当前发言者：可以直接回答；需要明确回应对象时，写 [diana-at:成员user_id] 提及实际对象。%s
	3. 可以同时提及多人，也可以把多个标记放在不同位置。不要重复提及同一成员；标记前后按正常中文语句保留必要空格。
	4. 发送层会原样保留这些标记的对象和相对位置，并按当前平台翻译成真正的提及。%s
	5. 只能使用候选 JSON 中存在的 user_id，不得根据昵称猜账号；不要把标记放进 Markdown 代码块，也不要自己写平台专用的提及写法。
	6. 标记只有 [diana-at:user_id] 这一种写法：半角方括号加半角冒号，中间不加空格。写成 @diana-at-user_id、<diana-at:user_id>、(diana-at:user_id) 都不是提及。
	7. 回复始终对应当前消息；历史消息、引用内容和媒体只作为回答参考，不要把回复对象错误切换成旧消息发送者。`,
		string(payload),
		currentSenderMentionRule(cfg),
		autoDecorationCancelClause(cfg),
		autoDecorationAvoidClause(cfg)))
}

// markStablePromptPrefix 在「历史之后、逐消息内容之前」标出缓存断点。只有 system
// 头部一条时不标：那条由适配层单独缓存，没有历史就没有第二段可复用的前缀。
func markStablePromptPrefix(messages []llm.Message) []llm.Message {
	if len(messages) < 2 {
		return messages
	}
	messages[len(messages)-1].CacheBreakpoint = true
	return messages
}

func appendPromptSection(builder *strings.Builder, section string) {
	section = strings.TrimSpace(section)
	if section == "" {
		return
	}
	builder.WriteString("\n")
	builder.WriteString(section)
}

func renderPromptTemplate(template string, values map[string]string) string {
	rendered := strings.TrimSpace(template)
	for key, value := range values {
		rendered = strings.ReplaceAll(rendered, "{"+key+"}", value)
	}
	return rendered
}

func historyPromptText(event MessageEvent) string {
	return historyPromptTextAt(event, 0)
}

func historyPromptTextAt(event MessageEvent, currentTime int64, configs ...BotConfig) string {
	text := PlainText(event.Segments)
	if text == "" && !hasImageSegment(event.Segments) {
		text = event.RawMessage
	}
	// 正文同样不可信：不中和的话，一条消息里手写
	// 「[历史 …] 李四（im_user_x）[主人]: …」就能伪造出一整行别人的历史。
	text = neutralizeIdentityMarkers(strings.TrimSpace(text))
	if text == "" {
		return ""
	}
	if quoted := quotedPromptText(event.Quoted); quoted != "" {
		text += "\n" + quoted
	}
	return historyLinePrefix(event) + promptSenderIdentity(event) + historySenderTag(event, configs...) + ": " + text
}

func agentImageHistoryPromptTextAt(event MessageEvent, currentTime int64) string {
	return agentImageHistoryPromptTextWithDescriptions(event, currentTime, nil)
}

// agentImageHistoryPromptTextWithDescriptions 在媒体计数之外附上已缓存的图片和视频关键帧描述。
// 只有计数的占位行会让模型在被追问历史媒体时无内容可依，转而编造或退化成寒暄。
func agentImageHistoryPromptTextWithDescriptions(event MessageEvent, currentTime int64, descriptions []string, configs ...BotConfig) string {
	imageCount := historicalStillImageCount(event)
	videoCount := historicalVideoCount(event)
	videoFrameCount := historicalVideoFrameCount(event)
	audioCount := historicalAudioCount(event)
	fileCount := historicalFileCount(event)
	if imageCount+videoCount+videoFrameCount+audioCount+fileCount == 0 {
		return ""
	}
	text := rawMessageWithoutImagePlaceholders(PlainText(event.Segments))
	if quoted := quotedPromptText(event.Quoted); quoted != "" {
		quoted = rawMessageWithoutImagePlaceholders(quoted)
		if text != "" {
			text += "\n"
		}
		text += quoted
	}
	messageID := strings.TrimSpace(event.MessageID)
	if messageID == "" {
		messageID = "不可用"
	}
	line := historyLinePrefix(event) + promptSenderIdentity(event) + historySenderTag(event, configs...)
	if text != "" {
		line += ": " + text
	}
	// 只列有的媒体种类和数量。以前这一行把五种计数（多数是 0）、「当前未附加
	// 原件」和一整句怎么调用 history_media 都写一遍，每条带图的历史要多付
	// 近百个 token——群里表情包一条接一条，这笔开销比正文还大。「摘要不等于看过
	// 原件」和「怎么取原件」在 promptToolHistoryImages 里只说一次就够了。
	line += "\n【媒体 message_id=" + messageID + "：" + historicalMediaSummary(imageCount, videoCount, videoFrameCount, audioCount, fileCount) + "】"
	if len(descriptions) > 0 {
		line += "\n" + strings.Join(descriptions, "\n")
	}
	return line
}

// 下面这些是附在当前消息正文后面的注解：按消息里有没有 @、引用、语义来源、视频、
// 长图，逐条补一句该怎么读。【…】标记是结构，留在代码里；标记后面的说明可以覆盖。
var (
	promptNoteSupplementSpec = registerPrompt(PromptSpec{
		Key:     "reply.note.supplement",
		Group:   PromptGroupReplyRules,
		Title:   "当前消息注解 · 同轮补充消息",
		Usage:   "同一轮里用户补发的每条消息前都带这句，放在「【当前同轮补充消息，」之后、「】」之前，要求和最后那条当前消息合起来一并回答。",
		Default: "必须与最后的当前消息合并理解并一并回答；若本消息明确纠正原要求，以纠正后的条件为准，保留未被修改的要求",
	})
	promptNoteMentionOnlySpec = registerPrompt(PromptSpec{
		Key:     "reply.note.mention_only",
		Group:   PromptGroupReplyRules,
		Title:   "当前消息注解 · 只有 @ 或引用",
		Usage:   "当前消息除了 @ 和引用没有别的正文、又没附「只叫一声」指引时，补在正文后面。",
		Default: "这条当前消息主要由 @ 或引用组成，没有额外正文，也要把它当成一次有效唤醒并自然回复。",
	})
	promptNoteAtOtherSpec = registerPrompt(PromptSpec{
		Key:     "reply.note.at_other",
		Group:   PromptGroupReplyRules,
		Title:   "当前消息注解 · @ 了别人",
		Usage:   "当前消息里 @ 了机器人以外的人时，补在正文后面，提醒 @ 关系也是消息的一部分。",
		Default: "当前消息包含 @ 标记，@ 是当前消息的一部分，不要忽略。",
	})
	promptNoteAtSelfSpec = registerPrompt(PromptSpec{
		Key:     "reply.note.at_self",
		Group:   PromptGroupReplyRules,
		Title:   "当前消息注解 · @ 了机器人",
		Usage:   "当前消息里的 @ 只指向机器人自己时，补在正文后面。",
		Default: "正文里那个 @ 指的就是你，等于有人直接叫了你一声。",
	})
	promptNoteReplySpec = registerPrompt(PromptSpec{
		Key:     "reply.note.reply",
		Group:   PromptGroupReplyRules,
		Title:   "当前消息注解 · 带引用",
		Usage:   "当前消息引用或回复了某条消息时，补在正文后面。",
		Default: "当前消息包含引用/回复标记，引用关系是当前消息的一部分；如果引用内容能从历史参考中看出，可以结合它回复。",
	})
	promptNoteSourcesTextImagesSpec = registerPrompt(PromptSpec{
		Key:     "reply.note.sources_text_images",
		Group:   PromptGroupReplyRules,
		Title:   "当前消息注解 · 多条指代来源（文字和图片）",
		Usage:   "指代判断为当前消息找到多条历史来源、其中既有文字又附上了图片时，补在正文后面。",
		Default: "语义指代已定位到 {sources} 条历史来源，其中有 {text_sources} 条文字来源、实际附加 {images} 张可读取图片；必须逐条核对文字并逐张查看图片后综合回答。",
		Vars:    promptNoteSourceVars("sources", "text_sources", "images"),
	})
	promptNoteSourcesImagesSpec = registerPrompt(PromptSpec{
		Key:     "reply.note.sources_images",
		Group:   PromptGroupReplyRules,
		Title:   "当前消息注解 · 多条指代来源（只有图片）",
		Usage:   "指代判断为当前消息找到多条历史来源、附上了图片但没有文字来源时，补在正文后面。",
		Default: "语义指代已定位到 {sources} 条历史来源，实际附加 {images} 张可读取图片；图片按原消息从旧到新排列，必须逐张查看并综合回答。",
		Vars:    promptNoteSourceVars("sources", "images"),
	})
	promptNoteSourcesTextSpec = registerPrompt(PromptSpec{
		Key:     "reply.note.sources_text",
		Group:   PromptGroupReplyRules,
		Title:   "当前消息注解 · 多条指代来源（只有文字）",
		Usage:   "指代判断为当前消息找到多条历史来源、只有文字来源没有图片时，补在正文后面。",
		Default: "语义指代已定位到 {sources} 条历史来源，其中 {text_sources} 条包含文字；完整来源已按顺序列出，必须逐条核对并综合回答。",
		Vars:    promptNoteSourceVars("sources", "text_sources"),
	})
	promptNoteSourcesRecordsSpec = registerPrompt(PromptSpec{
		Key:     "reply.note.sources_records",
		Group:   PromptGroupReplyRules,
		Title:   "当前消息注解 · 多条指代来源（无文字无图）",
		Usage:   "指代判断为当前消息找到多条历史来源、但既没有文字来源也没附上图片时，补在正文后面。",
		Default: "语义指代已定位到 {sources} 条历史来源；必须按已提供的来源记录逐条核对，不要假定存在未附加的图片。",
		Vars:    promptNoteSourceVars("sources"),
	})
	promptNoteSourcesMissingSpec = registerPrompt(PromptSpec{
		Key:     "reply.note.sources_missing",
		Group:   PromptGroupReplyRules,
		Title:   "当前消息注解 · 指代来源缺失",
		Usage:   "多条指代来源里有的没能从历史记录里找回时，紧接在上一句来源说明后面（不换行）。",
		Default: "其中 {missing} 条来源未能从持久化历史解析，必须明确说明缺失范围，不要编造其内容。",
		Vars:    promptNoteSourceVars("missing"),
	})
	promptNoteVideoReadSpec = registerPrompt(PromptSpec{
		Key:     "reply.note.video_read",
		Group:   PromptGroupReplyRules,
		Title:   "当前消息注解 · 视频已读取",
		Usage:   "当前消息或它引用的消息里有视频、并且成功取到了画面时，跟在「【媒体读取事实】」后面。",
		Default: "系统已成功读取并附加当前消息中的视频画面；不得声称媒体为空、未加载、不可见、工具不可用或读取失败。若画面本身难以辨认，只能如实说明无法从已看到的画面确认具体内容。",
	})
	promptNoteVideoForwardSpec = registerPrompt(PromptSpec{
		Key:     "reply.note.video_forward",
		Group:   PromptGroupReplyRules,
		Title:   "当前消息注解 · 合并转发里的视频",
		Usage:   "视频来自合并转发时，跟在「【合并转发媒体节点】」和节点清单后面，提醒文字和视频是分开的节点。",
		Default: "转发中的文字和视频是独立节点；除非节点归属明确，不得声称某句文字出现在某个视频里。",
	})
	promptNoteVideoQuotedFramesSpec = registerPrompt(PromptSpec{
		Key:     "reply.note.video_quoted_frames",
		Group:   PromptGroupReplyRules,
		Title:   "当前消息注解 · 引用视频的画面",
		Usage:   "视频在被引用的消息里、画面已附上时，跟在「【当前引用视频的关键帧如下】」后面。",
		Default: "请只根据这些关键帧回答当前视频问题；不要把历史消息里的其他视频、链接标题或解析结果当成当前视频。" + videoFrameNarrationRule,
	})
	promptNoteVideoFramesSpec = registerPrompt(PromptSpec{
		Key:     "reply.note.video_frames",
		Group:   PromptGroupReplyRules,
		Title:   "当前消息注解 · 视频画面",
		Usage:   "视频就在当前消息里、画面已附上时，跟在「【当前视频的关键帧如下】」后面。",
		Default: "请根据这些关键帧回答当前问题。" + videoFrameNarrationRule,
	})
	promptNoteVideoFailedSpec = registerPrompt(PromptSpec{
		Key:     "reply.note.video_failed",
		Group:   PromptGroupReplyRules,
		Title:   "当前消息注解 · 视频读取失败",
		Usage:   "当前消息里有视频但没取到画面时，跟在「【系统提示】」后面，让模型把具体原因转告用户。",
		Default: "当前视频没能读出画面，原因：{reason}把这个原因用自己的话告诉用户，别只说一句读不了。不得使用历史消息里的其他视频、链接标题或解析结果猜测当前视频。" + videoFrameNarrationRule,
		Vars:    []PromptVar{{Name: "reason", Description: "读不出画面的原因，如没装 ffmpeg、视频超过大小上限，末尾带一个空格"}},
	})
	promptNoteImageOnlySingleSpec = registerPrompt(PromptSpec{
		Key:     "reply.note.image_only_single",
		Group:   PromptGroupReplyRules,
		Title:   "当前消息注解 · 只发一张图",
		Usage:   "附图的消息没有文字时，用这句代替正文。和「只发图片时的正文」不同，这句用在已经把图片取好、准备连图一起发给模型的那一步。",
		Default: "用户发送了一张图片，请根据图片内容回答。",
	})
	promptNoteImageOnlyMultiSpec = registerPrompt(PromptSpec{
		Key:     "reply.note.image_only_multi",
		Group:   PromptGroupReplyRules,
		Title:   "当前消息注解 · 只发多张图",
		Usage:   "附了多张图、没有文字的消息，用这句代替正文。",
		Default: "用户发送了 {images} 张图片，请逐张查看并综合回答。",
		Vars:    promptNoteSourceVars("images"),
	})
	promptNoteLongImageSpec = registerPrompt(PromptSpec{
		Key:     "reply.note.long_image",
		Group:   PromptGroupReplyRules,
		Title:   "当前消息注解 · 长图切片",
		Usage:   "附带的图片里有超长图、被切成多段发送时，跟在「【长图处理】」后面，说明切片顺序和重叠。",
		Default: "部分超长图片已按“完整总览 → 沿长边顺序切片”展开；相邻切片有重叠，请按收到顺序阅读并合并重复内容。",
	})
)

// promptNoteSourceVars 按名字挑出来源注解用到的占位符说明。
func promptNoteSourceVars(names ...string) []PromptVar {
	descriptions := map[string]string{
		"sources":      "指代判断选中的历史来源条数",
		"text_sources": "其中带文字的来源条数",
		"images":       "实际附上、模型能看到的图片张数",
		"missing":      "没能从历史记录里找回的来源条数",
	}
	vars := make([]PromptVar, 0, len(names))
	for _, name := range names {
		vars = append(vars, PromptVar{Name: name, Description: descriptions[name]})
	}
	return vars
}

func proactiveTurnPromptTextAt(event MessageEvent, fallbackText string, currentTime int64, overrides PromptOverrides) string {
	text := strings.TrimSpace(PlainText(event.Segments))
	if text == "" && !hasImageSegment(event.Segments) {
		text = strings.TrimSpace(firstNonEmpty(fallbackText, event.RawMessage))
	}
	if text == "" {
		return ""
	}
	if quoted := quotedPromptText(event.Quoted); quoted != "" {
		text += "\n" + quoted
	}
	return "【当前同轮补充消息，" + overrides.text(promptNoteSupplementSpec) + "】" + contextMessageTiming(event.Time, currentTime) + promptSenderIdentity(event) + ": " + text
}

func currentPromptText(event MessageEvent, text string) string {
	return currentPromptTextWithSemanticContext(event, text, semanticReferenceContext{
		RequestedSourceCount: len(eventSemanticSourceMessageIDs(event)),
	}, promptAnnotation{})
}

// currentPromptTextWithSemanticContext 组装交给模型的当前消息。
//
// annotation.WakeGuidance 是配置里的「只被唤醒」提示词。它是注解，不是正文替身：
// 正文永远是用户的原话，这句只在「这条消息除了叫一声什么都没有」时附在后面，
// 告诉模型该怎么接。
func currentPromptTextWithSemanticContext(event MessageEvent, text string, sourceContext semanticReferenceContext, annotation promptAnnotation) string {
	text = strings.TrimSpace(text)
	botID := annotation.botID(event)
	wakeGuidanceAttached := false
	hasAtSegment := eventHasSegmentType(event, "at")
	hasReplySegment := eventHasSegmentType(event, "reply")
	if text == "" {
		// 这里曾经又抄了一遍那句「用户只唤醒了你」的字面量，和 cleanInput 用的
		// 配置项各写各的：改了配置这条路径上不生效，改了默认值这里也不跟着变。
		// cleanInput 正常情况下已经把空文本换成了配置值，走到这儿说明是别的
		// 调用路径，至少要和内置默认值保持同一份。
		text = annotation.wakeGuidance()
		wakeGuidanceAttached = true
	} else if bareWakeMention(event, text, botID, annotation.TriggerWords) {
		// 只是叫了一声：原话照留，接话方式作为注解跟在后面。
		text += "\n\n" + annotation.wakeGuidance()
		wakeGuidanceAttached = true
	}
	if currentMessageOnlyMentionsOrReplies(event, text) && !wakeGuidanceAttached {
		// 唤醒指引已经把「这是一次有效唤醒、该怎么接」说全了，不再补这句泛泛的。
		text += "\n\n" + annotation.Overrides.text(promptNoteMentionOnlySpec)
	}
	if hasAtSegment {
		if mentionsSomeoneElseFor(event, botID) {
			text += "\n\n" + annotation.Overrides.text(promptNoteAtOtherSpec)
		} else {
			text += "\n\n" + annotation.Overrides.text(promptNoteAtSelfSpec)
		}
	}
	if hasReplySegment {
		text += "\n\n" + annotation.Overrides.text(promptNoteReplySpec)
	}
	if sourceContext.RequestedSourceCount > 1 {
		overrides := annotation.Overrides
		counts := map[string]string{
			"sources":      itoa(sourceContext.RequestedSourceCount),
			"text_sources": itoa(sourceContext.TextSourceCount),
			"images":       itoa(sourceContext.AttachedImageCount),
			"missing":      itoa(sourceContext.MissingSourceCount),
		}
		switch {
		case sourceContext.TextSourceCount > 0 && sourceContext.AttachedImageCount > 0:
			text += "\n\n" + overrides.render(promptNoteSourcesTextImagesSpec, counts)
		case sourceContext.AttachedImageCount > 0:
			text += "\n\n" + overrides.render(promptNoteSourcesImagesSpec, counts)
		case sourceContext.TextSourceCount > 0:
			text += "\n\n" + overrides.render(promptNoteSourcesTextSpec, counts)
		default:
			text += "\n\n" + overrides.render(promptNoteSourcesRecordsSpec, counts)
		}
		if sourceContext.MissingSourceCount > 0 {
			text += overrides.render(promptNoteSourcesMissingSpec, counts)
		}
	}
	if notice := strings.TrimSpace(event.imageContextNotice); notice != "" {
		text += "\n\n【媒体状态】" + notice
	}
	quotedCoveredBySemanticBlock := event.Quoted != nil && event.Quoted.Semantic && len(eventSemanticSourceMessageIDs(event)) > 0
	if quoted := quotedPromptText(event.Quoted); quoted != "" && !quotedCoveredBySemanticBlock {
		text += "\n\n" + quoted
	}
	if reference := recentTextReferencePrompt(event.recentTextReference); reference != "" {
		text += "\n\n" + reference
	}
	return "【当前需要回复的消息】" + contextMessageTiming(event.Time, 0) + "【当前发言者】" + promptSenderIdentity(event) + "\n" + text
}

func quotedPromptText(quoted *QuotedMessage) string {
	if quoted == nil {
		return ""
	}
	text := PlainText(quoted.Segments)
	if hasImageSegment(quoted.Segments) {
		text = rawMessageWithoutImagePlaceholders(text)
	}
	if strings.TrimSpace(text) == "" && !hasImageSegment(quoted.Segments) {
		text = strings.TrimSpace(quoted.RawMessage)
	}
	if strings.TrimSpace(text) == "" {
		return ""
	}
	sender := formatPromptIdentity(quoted.SenderName, quoted.UserID)
	label := "被引用的消息"
	if quoted.Semantic {
		label = "指代判断选中的历史消息"
	}
	// 引用发言者的别名已经在上面的 sender 里（formatPromptIdentity 渲染成
	// 「昵称（别名）」），以前还会再跟一行
	// 【引用发言者身份】{"quoted_sender_user_id":"…"}。线上抽样的 30 条引用里，
	// 这一行的 role 全是空的，剩下的就只有那个重复的别名——整段是纯冗余。
	return fmt.Sprintf("【%s】%s: %s", label, sender, strings.TrimSpace(text))
}

func llmMessageFromEvent(event MessageEvent, text string, options ...any) llm.Message {
	if len(options) == 0 {
		return llmMessageFromEventWithImages(event, text, nil)
	}

	imageOnlyText := "用户发送了一张图片，请根据图片内容回答。"
	if value, ok := options[0].(string); ok && strings.TrimSpace(value) != "" {
		imageOnlyText = strings.TrimSpace(value)
	}
	var resolveImage func(string) string
	if len(options) > 1 {
		resolveImage, _ = options[1].(func(string) string)
	}

	text = strings.TrimSpace(text)
	imageURLs := availableImageURLs(event.Segments)
	if event.Quoted != nil {
		imageURLs = append(imageURLs, availableImageURLs(event.Quoted.Segments)...)
	}
	if len(imageURLs) == 0 {
		return llm.Message{Role: llm.RoleUser, Content: text}
	}
	if imageOnlyPrompt(text, event) {
		text = imageOnlyText
	}
	parts := make([]llm.ContentPart, 0, len(imageURLs)+1)
	if text != "" {
		parts = append(parts, llm.ContentPart{Type: llm.ContentPartText, Text: text})
	}
	for _, imageURL := range imageURLs {
		if resolveImage != nil {
			imageURL = resolveImage(imageURL)
		}
		parts = append(parts, llm.ContentPart{Type: llm.ContentPartImageURL, ImageURL: imageURL, Detail: "high"})
	}
	return llm.Message{Role: llm.RoleUser, Content: text, Parts: parts}
}

func llmMessageFromEventWithVideoFrames(ctx context.Context, event MessageEvent, text string, extraImageURLs []string) llm.Message {
	message, _ := llmMessageFromEventWithVideoFramesDetailed(ctx, event, text, extraImageURLs)
	return message
}

func llmMessageFromEventWithVideoFramesDetailed(ctx context.Context, event MessageEvent, text string, extraImageURLs []string) (llm.Message, bool) {
	message, failures := llmMessageFromEventWithVideoFramesDiagnostics(ctx, event, text, extraImageURLs)
	return message, len(failures) == 0
}

func llmMessageFromEventWithVideoFramesDiagnostics(ctx context.Context, event MessageEvent, text string, extraImageURLs []string) (llm.Message, []error) {
	groups := [][]MessageSegment{event.Segments}
	if event.Quoted != nil {
		groups = append(groups, event.Quoted.Segments)
	}
	ctx = withVideoMediaIdentities(ctx, event.Platform, groups...)
	videoURLs := videoSourceCandidates(event.Segments)
	cachedFrames := cachedVideoFrameURLs(event.Segments)
	quotedVideo := false
	if event.Quoted != nil {
		quotedURLs := videoSourceCandidates(event.Quoted.Segments)
		quotedVideo = hasVideoSegment(event.Quoted.Segments)
		videoURLs = append(videoURLs, quotedURLs...)
		cachedFrames = append(cachedFrames, cachedVideoFrameURLs(event.Quoted.Segments)...)
	}
	frames := cachedFrames
	cleanupFrames := false
	videoFailure := ""
	if len(frames) == 0 {
		frames, videoFailure = extractVideoContextFramesDetailed(ctx, videoURLs, 0)
		cleanupFrames = true
	}
	if cleanupFrames {
		defer cleanupVideoContextFrames(frames)
	}
	if len(videoURLs) > 0 || len(cachedFrames) > 0 {
		overrides := promptOverridesFromContext(ctx)
		if len(frames) > 0 {
			text += "\n\n【媒体读取事实】" + overrides.text(promptNoteVideoReadSpec)
			if manifest := forwardVideoFrameManifest(event); manifest != "" {
				text += "\n【合并转发媒体节点】" + manifest + overrides.text(promptNoteVideoForwardSpec)
			}
			if quotedVideo {
				text += "\n\n【当前引用视频的关键帧如下】" + overrides.text(promptNoteVideoQuotedFramesSpec)
			} else {
				text += "\n\n【当前视频的关键帧如下】" + overrides.text(promptNoteVideoFramesSpec)
			}
		} else {
			// 原因照实说出来。以前这里只写「读取或抽帧失败」，模型只能照着复述，
			// 用户得到一句「我暂时读不了这个视频」——既不知道是这台机器没装
			// ffmpeg、还是视频超了大小上限，也就不知道该找谁修。
			text += "\n\n【系统提示】" + overrides.render(promptNoteVideoFailedSpec, map[string]string{"reason": videoFailureReason(videoFailure)})
		}
	}
	extraImageURLs = append(extraImageURLs, frames...)
	return llmMessageFromEventWithImagesForContextDiagnostics(ctx, event, text, extraImageURLs)
}

func llmMessageFromEventWithImages(event MessageEvent, text string, extraImageURLs []string) llm.Message {
	return llmMessageFromEventWithImagesForContext(context.Background(), event, text, extraImageURLs)
}

func llmMessageFromEventWithImagesForContext(ctx context.Context, event MessageEvent, text string, extraImageURLs []string) llm.Message {
	message, _ := llmMessageFromEventWithImagesForContextDetailed(ctx, event, text, extraImageURLs)
	return message
}

func llmMessageFromEventWithImagesForContextDetailed(ctx context.Context, event MessageEvent, text string, extraImageURLs []string) (llm.Message, bool) {
	message, failures := llmMessageFromEventWithImagesForContextDiagnostics(ctx, event, text, extraImageURLs)
	return message, len(failures) == 0
}

func llmMessageFromEventWithImagesForContextDiagnostics(ctx context.Context, event MessageEvent, text string, extraImageURLs []string) (llm.Message, []error) {
	return llmMessageFromEventWithImageDetail(ctx, event, text, extraImageURLs, "high")
}

// llmMessageFromEventWithImageDetail 和上面一样拼图文消息，只是图片清晰度由调用方定。
// 正式回复要看清图里的字和细节，一直用 high；只做是非判断的路由用 low 就够——
// 它要知道「这是张什么图」，不用逐字读，high 档一张图的 token 往往比整段上下文还多。
func llmMessageFromEventWithImageDetail(ctx context.Context, event MessageEvent, text string, extraImageURLs []string, detail string) (llm.Message, []error) {
	text = strings.TrimSpace(text)
	imageURLs := availableImageURLs(event.Segments)
	if event.Quoted != nil {
		imageURLs = append(imageURLs, availableImageURLs(event.Quoted.Segments)...)
	}
	imageURLs = append(imageURLs, extraImageURLs...)
	imageGroups, failures := loadLLMImageURLGroupsDetailed(ctx, imageURLs)
	imageGroups = dedupeLLMImageGroups(imageGroups)
	sourceImageCount := len(imageGroups)
	imageURLs = flattenLLMImageGroups(imageGroups)
	expandedLongImages := len(imageURLs) > sourceImageCount
	if len(imageURLs) == 0 {
		return llm.Message{Role: llm.RoleUser, Content: text}, failures
	}
	// 覆盖从 ctx 上取：这条函数的调用方很多，只有正式回复那一路挂了机器人的覆盖，
	// 其余路径（路由、审核）拿到 nil，照旧用内置默认值。
	overrides := promptOverridesFromContext(ctx)
	if imageOnlyPrompt(text, event) {
		if sourceImageCount == 1 {
			text = overrides.text(promptNoteImageOnlySingleSpec)
		} else {
			text = overrides.render(promptNoteImageOnlyMultiSpec, map[string]string{"images": itoa(sourceImageCount)})
		}
	}
	if expandedLongImages {
		text += "\n\n【长图处理】" + overrides.text(promptNoteLongImageSpec)
	}
	parts := make([]llm.ContentPart, 0, len(imageURLs)+1)
	if text != "" {
		parts = append(parts, llm.ContentPart{Type: llm.ContentPartText, Text: text})
	}
	for _, imageURL := range imageURLs {
		parts = append(parts, llm.ContentPart{Type: llm.ContentPartImageURL, ImageURL: imageURL, Detail: detail})
	}
	return llm.Message{Role: llm.RoleUser, Content: text, Parts: parts}, failures
}

func imageOnlyPrompt(text string, event MessageEvent) bool {
	if !hasImageSegment(event.Segments) {
		return false
	}
	text = strings.TrimSpace(text)
	return text == "" || text == "[图片]"
}

func runtimeLLMMessageEmpty(msg llm.Message) bool {
	if strings.TrimSpace(msg.Content) != "" {
		return false
	}
	return len(msg.Parts) == 0
}

// contextHistory 返回当前会话历史副本。
func (r *Runtime) contextHistory(event MessageEvent) []MessageEvent {
	current, store := r.sessionContextHistory(event)
	if store == nil {
		return current
	}
	crossGroup := r.crossGroupContextEvents(event, store)
	return mergeCrossGroupContextHistory(current, crossGroup)
}

// sessionContextHistory returns only the current conversation. Background
// memory extraction uses this path because its recent-message prompt does not
// need an expensive cross-group semantic search for every queued event.
func (r *Runtime) sessionContextHistory(event MessageEvent) ([]MessageEvent, MessageHistoryStore) {
	if event.replyHistoryLoaded {
		// 历史已经在本轮更早的地方加载过，直接用缓存并且不返回 store：
		// 返回 store 会让 contextHistory 顺手补一次跨群检索，而这条正是回复
		// 热路径，每轮会走好几次，等于凭空多出好几次全表文本搜索。跨群上下文
		// 在历史首次加载时就已经并进去了。
		return append([]MessageEvent(nil), event.replyHistory...), nil
	}
	session := sessionKey(event)
	r.mu.RLock()
	// 返回副本，生成回复时遍历历史不会和新消息写入互相影响。
	history := r.history[session]
	limit := r.effectiveConfigForEventLocked(event).RecentContextLimit
	if limit <= 0 {
		limit = 20
	}
	if len(history) > limit {
		history = history[len(history)-limit:]
	}
	memory := append([]MessageEvent(nil), history...)
	store := r.messageStore
	r.mu.RUnlock()
	if store == nil {
		return memory, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stored, err := listContextMessageEvents(ctx, store, session, limit)
	if err != nil {
		log.Printf("diana message history load failed: %v", err)
		return memory, store
	}
	return mergeMessageHistory(memory, stored, limit), store
}

func mergeMessageHistory(memory []MessageEvent, stored []MessageEvent, limit int) []MessageEvent {
	if limit <= 0 {
		limit = 20
	}
	merged := make([]MessageEvent, 0, len(stored)+len(memory))
	seen := map[string]bool{}
	appendOne := func(event MessageEvent) {
		key := messageHistoryDedupeKey(event)
		if key != "" && seen[key] {
			return
		}
		if key != "" {
			seen[key] = true
		}
		merged = append(merged, event)
	}
	for _, event := range stored {
		appendOne(event)
	}
	for _, event := range memory {
		appendOne(event)
	}
	// Persisted recent history and the in-memory window can overlap in different
	// positions. Sort the deduplicated union before trimming so old memory-only
	// entries cannot displace newer persisted events at the tail of the slice.
	sort.SliceStable(merged, func(left, right int) bool {
		return merged[left].Time < merged[right].Time
	})
	if len(merged) > limit {
		merged = merged[len(merged)-limit:]
	}
	return merged
}

func messageHistoryDedupeKey(event MessageEvent) string {
	if event.MessageID != "" {
		return string(event.Kind) + "|" + event.GroupID + "|" + event.UserID + "|" + event.MessageID
	}
	text := firstNonEmpty(strings.TrimSpace(PlainText(event.Segments)), strings.TrimSpace(event.RawMessage))
	if text == "" {
		return ""
	}
	return string(event.Kind) + "|" + event.GroupID + "|" + event.UserID + "|" + strconv.FormatInt(event.Time, 10) + "|" + text
}

// renderLLMProfiles 渲染提供商配置档列表。
func (r *Runtime) renderLLMProfiles() string {
	if r.llmStore == nil {
		return "当前未接入提供商配置集。"
	}
	set := r.llmStore.Profiles()
	if len(set.Profiles) == 0 {
		return "当前没有可用的提供商配置。"
	}
	// 按列表原顺序输出，不再按名字排序：组内顺序就是降级顺序，排过序的列表会把
	// 这个含义抹掉。以前用 * 标出激活项，那个概念已经没有了。
	lines := []string{"提供商配置列表（组内自上而下即降级顺序）："}
	for _, profile := range set.Profiles {
		lines = append(lines, fmt.Sprintf("- %s [%s] (%s / %s)", profile.Name, llm.NormalizeProfileGroup(profile.Group), profile.Config.Provider, profile.Config.Model))
	}
	return strings.Join(lines, "\n")
}
