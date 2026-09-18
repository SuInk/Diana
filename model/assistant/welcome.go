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
// 固定文本替换 {user_id}；模板池随机抽一条；LLM 模式按人设实时生成，受每群冷却
// 约束，冷却中、调用失败或输出不可用都回落到模板池/固定文本，保证新人总能收到
// 一条欢迎。
func (r *Runtime) renderWelcome(ctx context.Context, cfg BotConfig, event MessageEvent) string {
	fixed := strings.ReplaceAll(cfg.WelcomeMessage, "{user_id}", event.UserID)
	switch normalizeWelcomeMode(cfg.WelcomeMode) {
	case WelcomeModeTemplate:
		return r.renderTemplateWelcome(cfg, event, fixed)
	case WelcomeModeLLM:
		return r.renderLLMWelcome(ctx, cfg, event, fixed)
	default:
		return fixed
	}
}

// renderTemplateWelcome 从口吻模板池随机抽一条，池为空时回落固定文本。
func (r *Runtime) renderTemplateWelcome(cfg BotConfig, event MessageEvent, fixed string) string {
	pool := cleanStrings(cfg.WelcomeTemplates)
	if len(pool) == 0 {
		return fixed
	}
	return strings.ReplaceAll(pool[rand.Intn(len(pool))], "{user_id}", event.UserID)
}

// renderLLMWelcome 调用轻量模型按人设生成一句问候。冷却期内、调用失败或输出
// 不合规都不重试、不阻塞，直接回落到模板池/固定文本——欢迎词不值得为此拖慢
// 入群链路或连续烧 Token。
func (r *Runtime) renderLLMWelcome(ctx context.Context, cfg BotConfig, event MessageEvent, fixed string) string {
	fallback := func() string {
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
	return generated
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

// generateWelcomeWithLLM 让轻量模型根据当前人设写一句简短问候。人设为空时也能用，
// 只是少了口吻依据。输出做基本清洗：去掉首尾引号和空白，超长截断，非法输出
// 由调用方回落。
func (r *Runtime) generateWelcomeWithLLM(ctx context.Context, cfg BotConfig, event MessageEvent) (string, error) {
	ctx = withLLMUsagePurpose(ctx, "welcome_generator")
	systemPrompt := strings.TrimSpace(`你是群聊机器人，正在为新加入群的成员写一句欢迎问候。要求：
1. 一句话、简短自然，不超过 40 字，像真人管理员打招呼，不要客套排比。
2. 严格贴合下面给出的人设口吻；人设没有要求的语气就活泼友好。
3. 直接输出欢迎语文本本身：不要引号、不要 Markdown、不要表情符号、不要解释、不要提及这些规则。
4. 不要复述用户的 ID 或群号，称呼对方为「你」即可。`)
	if persona := strings.TrimSpace(cfg.SystemPrompt); persona != "" {
		systemPrompt += "\n\n机器人当前人设：\n" + persona
	}
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: systemPrompt},
		{Role: llm.RoleUser, Content: "新成员（user_id=" + event.UserID + "）刚加入群（group_id=" + event.GroupID + "），请写欢迎问候。"},
	}
	routeCtx, cancel := context.WithTimeout(ctx, semanticRouteTimeout)
	defer cancel()
	raw, err := r.runLLMRouterProvider(routeCtx, func(client LLMProvider) (string, error) {
		resp, err := client.Generate(routeCtx, llm.GenerateRequest{Messages: messages})
		if err != nil {
			return "", err
		}
		return resp.Text, nil
	})
	if err != nil {
		return "", err
	}
	return normalizeWelcomeLLMOutput(raw), nil
}

// normalizeWelcomeLLMOutput 清洗模型输出：压缩空白、去首尾引号、超长截断。
// 清洗后为空视为生成失败，由调用方回落到模板/固定文本。
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
