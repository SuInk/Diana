// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"math/rand"
	"strings"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// renderWelcome 按生效配置的欢迎词模式生成入群欢迎文本（关闭 #575）。
// 固定文本和模板支持 {user_id}{nickname}{group}{group_id}；模板池随机抽一条；LLM 模式像普通回复一样
// 带着人设和群聊上下文生成，受每群冷却约束，冷却中、调用失败或输出不可用时回落到
// 主人配过的模板池/固定文本，都没配就返回空、这次不欢迎。
func (r *Runtime) renderWelcome(ctx context.Context, cfg BotConfig, event MessageEvent) string {
	fixed := strings.ReplaceAll(cfg.WelcomeMessage, "{user_id}", event.UserID)
	var text string
	switch normalizeWelcomeMode(cfg.WelcomeMode) {
	case WelcomeModeTemplate:
		text = r.renderTemplateWelcome(cfg, event, fixed)
	case WelcomeModeLLM:
		text = r.renderLLMWelcome(ctx, cfg, event, fixed)
	default:
		text = fixed
	}
	return r.expandWelcomePlaceholders(ctx, event, text)
}

// welcomeMemberName 查新人的群名片或昵称，查不到返回空。
//
// 入群通知不带昵称，而接入端渲染 @ 时只从自己的群成员缓存找名字；新人刚进群
// 还不在缓存里，@ 就被写成「@QQ号」，连 uid 都解析不出来、对方也收不到提醒。
// 这里强制刷新查一次：接入端的缓存顺带补上，查到的昵称也随 at 段一起发出去。
func (r *Runtime) welcomeMemberName(ctx context.Context, event MessageEvent) string {
	if name := strings.TrimSpace(event.SenderName); name != "" {
		return name
	}
	member, err := r.getGroupMemberInfoForEvent(ctx, event, event.GroupID, event.UserID)
	if err != nil {
		return ""
	}
	return firstNonEmpty(strings.TrimSpace(member.Card), strings.TrimSpace(member.Nickname))
}

// expandWelcomePlaceholders 替换 {nickname}{group}{group_id}。群名只在模板真用到
// 时才去平台查；昵称走入群通知时已由 welcomeMemberName 查好放进 SenderName。
// 查不到就回落成账号 ID / 群号，不把花括号原样发出去。
func (r *Runtime) expandWelcomePlaceholders(ctx context.Context, event MessageEvent, text string) string {
	if !strings.Contains(text, "{") {
		return text
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	nickname, group := "", ""
	if strings.Contains(text, "{nickname}") {
		nickname = r.welcomeMemberName(lookupCtx, event)
	}
	if strings.Contains(text, "{group}") {
		group = strings.TrimSpace(event.GroupName)
		if group == "" {
			if info, err := r.getGroupInfoForEvent(lookupCtx, event, event.GroupID); err == nil {
				group = strings.TrimSpace(info.GroupName)
			}
		}
	}
	// 一次替换完：昵称、群名是别人能随便起的，里面要是写着 {group_id}，分步替换会被二次展开。
	return strings.NewReplacer(
		"{nickname}", firstNonEmpty(nickname, event.UserID),
		"{group}", firstNonEmpty(group, event.GroupID),
		"{group_id}", event.GroupID,
		"{user_id}", event.UserID,
	).Replace(text)
}

// renderTemplateWelcome 从口吻模板池随机抽一条，池为空时回落固定文本。
func (r *Runtime) renderTemplateWelcome(cfg BotConfig, event MessageEvent, fixed string) string {
	pool := cleanStrings(cfg.WelcomeTemplates)
	if len(pool) == 0 {
		return fixed
	}
	return strings.ReplaceAll(pool[rand.Intn(len(pool))], "{user_id}", event.UserID)
}

// renderLLMWelcome 用对话模型按正常回复的上下文写欢迎。冷却期内、调用失败或
// 输出不可用都不重试、不阻塞，直接回落——欢迎词不值得为此拖慢入群链路或连续烧
// Token。回落只用主人自己配过的模板池或固定文本，内置默认文本宁可不发：那句话
// 生硬得像系统公告，比没有欢迎更出戏。
func (r *Runtime) renderLLMWelcome(ctx context.Context, cfg BotConfig, event MessageEvent, fixed string) string {
	fallback := func() string {
		if len(cleanStrings(cfg.WelcomeTemplates)) == 0 && isBuiltinWelcomeMessage(cfg.WelcomeMessage) {
			return ""
		}
		return r.renderTemplateWelcome(cfg, event, fixed)
	}
	key := strings.TrimSpace(event.ProfileID) + "|" + strings.TrimSpace(event.GroupID)
	if !r.claimWelcomeLLM(key, time.Now(), cfg.WelcomeLLMCooldownSeconds) {
		return fallback()
	}
	generated, err := r.generateWelcomeWithLLM(ctx, cfg, event)
	if err != nil || generated == "" {
		return fallback()
	}
	r.rememberWelcomeText(event.ProfileID, generated)
	return generated
}

// builtinWelcomeMessages 是历代内置的默认欢迎词。主人没改过就说明它只是占位，
// LLM 模式回落时不发。
var builtinWelcomeMessages = []string{
	"欢迎加入本群，可以直接 @我 开始聊天。",
	"欢迎加入本群，直接 @我 或发送触发词就可以开始聊天。",
}

func isBuiltinWelcomeMessage(message string) bool {
	message = strings.TrimSpace(message)
	if message == "" {
		return true
	}
	for _, builtin := range builtinWelcomeMessages {
		if message == builtin {
			return true
		}
	}
	return false
}

// claimWelcomeLLM 占用一次 LLM 欢迎额度，冷却期内返回 false。
func (r *Runtime) claimWelcomeLLM(key string, now time.Time, cooldownSeconds int) bool {
	cooldown := time.Duration(cooldownSeconds) * time.Second
	r.welcomeMu.Lock()
	defer r.welcomeMu.Unlock()
	if r.welcomeLLMLast == nil {
		r.welcomeLLMLast = map[string]time.Time{}
	}
	if last, ok := r.welcomeLLMLast[key]; ok && now.Sub(last) < cooldown {
		return false
	}
	// 顺手清掉早就过期的条目，别让这张表跟着入群次数一直长。
	for existing, at := range r.welcomeLLMLast {
		if now.Sub(at) > 24*time.Hour {
			delete(r.welcomeLLMLast, existing)
		}
	}
	r.welcomeLLMLast[key] = now
	return true
}

// welcomeNoticeDedupeWindow 内同一个人进同一个群只欢迎一次。
//
// 线上同一条入群通知会到两次（相隔一两秒）：第一次去调模型，第二次撞上冷却
// 回落成固定文本，新人先收到一句公告腔、两秒后又收到一句问候。
const welcomeNoticeDedupeWindow = 10 * time.Minute

// claimWelcomeNotice 占用这次入群的欢迎，窗口内重复的通知返回 false。
func (r *Runtime) claimWelcomeNotice(event MessageEvent, now time.Time) bool {
	key := strings.TrimSpace(event.ProfileID) + "|" + strings.TrimSpace(event.GroupID) + "|" + strings.TrimSpace(event.UserID)
	r.welcomeMu.Lock()
	defer r.welcomeMu.Unlock()
	if r.welcomeNoticeSeen == nil {
		r.welcomeNoticeSeen = map[string]time.Time{}
	}
	if last, ok := r.welcomeNoticeSeen[key]; ok && now.Sub(last) < welcomeNoticeDedupeWindow {
		return false
	}
	for existing, at := range r.welcomeNoticeSeen {
		if now.Sub(at) > welcomeNoticeDedupeWindow {
			delete(r.welcomeNoticeSeen, existing)
		}
	}
	r.welcomeNoticeSeen[key] = now
	return true
}

// welcomeRecentLimit 是带给模型「最近说过的欢迎」的条数。
const welcomeRecentLimit = 5

// rememberWelcomeText 按机器人记下最近几句欢迎。群历史里只看得到本群的，
// 这份跨群的清单让模型别在每个群都说同一句。
func (r *Runtime) rememberWelcomeText(profileID, text string) {
	key := strings.TrimSpace(profileID)
	text, _, _ = consumeReplyFormatting(text)
	text = strings.TrimSpace(strings.ReplaceAll(text, notificationSplitMarker, " "))
	r.welcomeMu.Lock()
	defer r.welcomeMu.Unlock()
	if r.welcomeRecent == nil {
		r.welcomeRecent = map[string][]string{}
	}
	recent := append(r.welcomeRecent[key], text)
	if len(recent) > welcomeRecentLimit {
		recent = recent[len(recent)-welcomeRecentLimit:]
	}
	r.welcomeRecent[key] = recent
}

func (r *Runtime) recentWelcomeTexts(profileID string) []string {
	r.welcomeMu.Lock()
	defer r.welcomeMu.Unlock()
	return append([]string(nil), r.welcomeRecent[strings.TrimSpace(profileID)]...)
}

const welcomeGeneratorPrompt = `有新成员刚进群，按你平时在这个群里说话的样子欢迎一下。
- 像群里的老成员随口打个招呼，不是客服或管理员致辞；长短、语气、带不带表情都照你平时的习惯。
- 能接就接：顺着群里正在聊的事、群名或对方的昵称说点具体的；没什么好接的就简单招呼一声，别硬凑，也别播报「群里正聊某某呢」。
- 别说「欢迎你来，随便聊聊」这类谁都能说的套话。开头、句式和收尾都换着来：不必以「欢迎」或「某某来啦」开头，也不必句句带「欢迎」两个字；你最近说过的欢迎里用过的开头和收尾（比如「来得正是时候」）这次别再用。
- 不介绍自己的功能、触发方式或群规，不复述这条通知，不写 QQ 号或群号。
- 直接给出要发的话。系统会自动 @ 对方，正文里不用再写 @。`

var promptWelcomeGeneratorSpec = registerPrompt(PromptSpec{
	Key:     "social.welcome",
	Group:   PromptGroupSocial,
	Title:   "入群欢迎语生成",
	Usage:   "入群欢迎设为由模型生成时，有新成员入群就用这段让模型写一句问候。它和普通回复一样带着人设、关系和最近的群聊，这段跟在入群通知后面。",
	Default: welcomeGeneratorPrompt,
})

// welcomeHistoryLimit 是带给欢迎生成的最近群聊条数：够看出大家在聊什么就行。
const welcomeHistoryLimit = 20

// generateWelcomeWithLLM 像普通回复一样生成欢迎：同一份系统提示词（人设、群风格、
// 和新人的关系）、最近的群聊历史、对话模型；只是不开工具，一轮出结果。
// 输出做基本清洗，非法输出由调用方回落。
func (r *Runtime) generateWelcomeWithLLM(ctx context.Context, cfg BotConfig, event MessageEvent) (string, error) {
	source := eventTriggerDeliveryEvent(event)
	ctx = withLLMUsagePurpose(withLLMUsageContext(ctx, source), PurposeWelcomeGenerator)
	timeout := cfg.RequestTimeout
	if timeout <= 0 {
		timeout = DefaultBotConfig().WithDefaults().RequestTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if strings.TrimSpace(source.GroupName) == "" {
		lookupCtx, lookupCancel := context.WithTimeout(ctx, 5*time.Second)
		if info, err := r.getGroupInfoForEvent(lookupCtx, source, source.GroupID); err == nil {
			source.GroupName = strings.TrimSpace(info.GroupName)
		}
		lookupCancel()
	}
	relationship := r.relationshipPolicy(ctx, source)
	messages := []llm.Message{{
		Role:     llm.RoleSystem,
		Content:  r.systemPromptWithRelationship(source, nil, false, relationship),
		Priority: llm.MessagePrioritySystem,
	}}
	history := sessionOnlyHistory(r.promptContextHistory(source, cfg))
	if len(history) > welcomeHistoryLimit {
		history = history[len(history)-welcomeHistoryLimit:]
	}
	for _, item := range history {
		if !samePromptGroupScope(source, item) {
			continue
		}
		messages = append(messages, r.renderPromptHistoryEvent(ctx, source, item, cfg, false)...)
	}
	if clock := r.runtimeClockPrompt(source); clock != "" {
		messages = append(messages, llm.Message{Role: llm.RoleSystem, Content: clock, Priority: llm.MessagePrioritySystem})
	}
	messages = append(messages, llm.Message{
		Role:     llm.RoleUser,
		Content:  r.welcomeRequestText(cfg, source),
		Priority: llm.MessagePriorityCurrent,
	})
	replyCfg := cfg
	replyCfg.AgentEnabled = false
	raw, err := r.generateReply(ctx, replyCfg, source, relationship, messages, nil)
	if err != nil {
		return "", err
	}
	return normalizeWelcomeLLMOutput(raw), nil
}

// welcomeRequestText 是这一轮的「当前消息」：谁进了哪个群，加上欢迎要求和最近
// 说过的欢迎。
func (r *Runtime) welcomeRequestText(cfg BotConfig, event MessageEvent) string {
	where := "本群"
	if name := strings.TrimSpace(event.GroupName); name != "" {
		where = "群「" + name + "」"
	}
	var builder strings.Builder
	builder.WriteString("【当前需要回复的消息】【入群通知】" + formatPromptIdentity(event.SenderName, event.UserID) + " 刚加入了" + where + "，还没说过话。\n\n")
	builder.WriteString(strings.TrimSpace(cfg.prompt(promptWelcomeGeneratorSpec)))
	if recent := r.recentWelcomeTexts(event.ProfileID); len(recent) > 0 {
		builder.WriteString("\n\n你最近说过的欢迎（这次换个开头和句式）：")
		for _, text := range recent {
			builder.WriteString("\n- " + text)
		}
	}
	return builder.String()
}

// normalizeWelcomeLLMOutput 清洗模型输出：压缩空白、去首尾引号、超长截断。
// 清洗后为空视为生成失败，由调用方回落。
func normalizeWelcomeLLMOutput(raw string) string {
	text := strings.TrimSpace(raw)
	text = strings.Trim(text, `"'“”‘’「」`)
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return ""
	}
	runes := []rune(text)
	if len(runes) > welcomeLLMMaxChars {
		text = string(runes[:welcomeLLMMaxChars])
	}
	return strings.TrimSpace(text)
}
