// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/llm"
)

const llmQQPrivacyPrompt = `【QQ 标识隐私代理】消息中的真实 QQ 号和群号已由本地代理替换为不透明别名。相同别名始终表示同一对象；qq_owner、qq_current_user、qq_bot、qq_user、qq_group 前缀保留角色语义。理解对话时按角色和昵称判断，不要猜测真实数字。调用工具或在回复中需要引用标识时，必须原样复制别名；本地代理会在执行工具或发送 QQ 消息前自动恢复真实标识。`

var (
	qqPrivacyJSONIDPattern = regexp.MustCompile(`(?i)"([a-z0-9_]*(?:user_id|group_id|qq|uin)|owner_id|operator_id|self_id)"\s*:\s*(?:"([1-9][0-9]{4,13})"|([1-9][0-9]{4,13}))`)
	qqPrivacyCQIDPattern   = regexp.MustCompile(`(?i)\[CQ:(?:at|contact),[^\]]*(?:qq|id)=([1-9][0-9]{4,13})`)
	qqPrivacyLabelPattern  = regexp.MustCompile(`(?i)(?:QQ号|QQ群号|QQ|UIN)\s*[:：=为]?\s*([1-9][0-9]{4,13})`)
)

type qqPrivacyContextKey struct{}

type qqPrivacyContextState struct {
	enabled bool
	scope   *qqPrivacyScope
}

type qqPrivacyScope struct {
	mu          sync.Mutex
	salt        string
	realToAlias map[string]string
	aliasToReal map[string]string
}

type qqPrivacyProvider struct {
	provider LLMProvider
	scope    *qqPrivacyScope
}

func newQQPrivacyScope() *qqPrivacyScope {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		sum := sha256.Sum256([]byte(time.Now().String()))
		random = sum[:16]
	}
	return &qqPrivacyScope{
		salt:        hex.EncodeToString(random),
		realToAlias: map[string]string{},
		aliasToReal: map[string]string{},
	}
}

func qqPrivacyScopeFromContext(ctx context.Context) *qqPrivacyScope {
	state, _ := qqPrivacyStateFromContext(ctx)
	if state == nil || !state.enabled {
		return nil
	}
	return state.scope
}

func qqPrivacyStateFromContext(ctx context.Context) (*qqPrivacyContextState, bool) {
	if ctx == nil {
		return nil, false
	}
	state, ok := ctx.Value(qqPrivacyContextKey{}).(*qqPrivacyContextState)
	return state, ok
}

func withQQPrivacyScope(ctx context.Context, scope *qqPrivacyScope) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if scope == nil || qqPrivacyScopeFromContext(ctx) == scope {
		return ctx
	}
	return context.WithValue(ctx, qqPrivacyContextKey{}, &qqPrivacyContextState{enabled: true, scope: scope})
}

func (r *Runtime) withQQPrivacyContext(ctx context.Context, event MessageEvent, history []MessageEvent) context.Context {
	cfg := r.effectiveConfigForEvent(event)
	if !llmQQIDMaskingEnabled(cfg) {
		if ctx == nil {
			ctx = context.Background()
		}
		return context.WithValue(ctx, qqPrivacyContextKey{}, &qqPrivacyContextState{enabled: false})
	}
	scope := qqPrivacyScopeFromContext(ctx)
	if scope == nil {
		scope = newQQPrivacyScope()
		ctx = withQQPrivacyScope(ctx, scope)
	}
	scope.register(cfg.OwnerID, "owner")
	scope.register(firstNonEmpty(cfg.BotQQ, event.SelfID), "bot")
	scope.register(event.UserID, "current_user")
	scope.register(event.GroupID, "group")
	scope.registerEvent(event)
	for _, item := range history {
		scope.registerEvent(item)
	}
	return ctx
}

func llmQQIDMaskingEnabled(cfg BotConfig) bool {
	cfg = cfg.WithDefaults()
	return cfg.LLMQQIDMaskingEnabled != nil && *cfg.LLMQQIDMaskingEnabled
}

func (r *Runtime) withLLMQQPrivacyRun(ctx context.Context, run llmProviderRunFunc) llmProviderRunFunc {
	if run == nil {
		return run
	}
	state, hasState := qqPrivacyStateFromContext(ctx)
	if hasState && (state == nil || !state.enabled) {
		return run
	}
	if !hasState && !llmQQIDMaskingEnabled(r.Config()) {
		return run
	}
	scope := qqPrivacyScopeFromContext(ctx)
	if scope == nil {
		scope = newQQPrivacyScope()
	}
	return func(provider LLMProvider) (string, error) {
		return run(&qqPrivacyProvider{provider: provider, scope: scope})
	}
}

func (p *qqPrivacyProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	if p == nil || p.provider == nil {
		return nil, errors.New("qqbot: QQ privacy provider is not configured")
	}
	if p.scope == nil {
		return p.provider.Generate(ctx, req)
	}
	protected := p.scope.protectRequest(req)
	response, err := p.provider.Generate(ctx, protected)
	if err != nil || response == nil {
		return response, err
	}
	copyResponse := *response
	copyResponse.Text = p.scope.restoreText(response.Text)
	// 工具参数同样要还原。提示词明确告诉模型「原样复制别名，本地代理会在执行工具前
	// 自动恢复真实标识」，模型照做了，可这里以前只还原回复正文，别名就原封不动地进了
	// 工具——提醒工具收到 qq_user_f49c630bf7cf 这种值，只能报「必须是有效 QQ 号」。
	copyResponse.ToolCalls = p.scope.restoreToolCalls(response.ToolCalls)
	return &copyResponse, nil
}

// restoreToolCalls 把工具参数里的别名换回真实标识。参数是任意 JSON 结构，字符串可能
// 藏在嵌套的对象或数组里，所以要递归走一遍。
func (s *qqPrivacyScope) restoreToolCalls(calls []llm.ToolCall) []llm.ToolCall {
	if s == nil || len(calls) == 0 {
		return calls
	}
	out := make([]llm.ToolCall, 0, len(calls))
	for _, call := range calls {
		restored := call
		if len(call.Arguments) > 0 {
			arguments := make(map[string]any, len(call.Arguments))
			for key, value := range call.Arguments {
				arguments[key] = s.restoreValue(value)
			}
			restored.Arguments = arguments
		}
		out = append(out, restored)
	}
	return out
}

func (s *qqPrivacyScope) restoreValue(value any) any {
	switch typed := value.(type) {
	case string:
		return s.restoreText(typed)
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = s.restoreValue(item)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, s.restoreValue(item))
		}
		return out
	}
	return value
}

func (s *qqPrivacyScope) registerEvent(event MessageEvent) {
	s.register(event.UserID, "user")
	s.register(event.OperatorID, "user")
	s.register(event.GroupID, "group")
	if event.Quoted != nil {
		s.register(event.Quoted.UserID, "user")
		s.register(event.Quoted.GroupID, "group")
		s.registerSegments(event.Quoted.Segments)
	}
	s.registerSegments(event.Segments)
}

func (s *qqPrivacyScope) registerSegments(segments []MessageSegment) {
	for _, segment := range segments {
		for key, value := range segment.Data {
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "group_id", "source_group_id":
				s.register(value, "group")
			case "qq", "user_id", "uin", "operator_id", "source_user_id":
				s.register(value, "user")
			}
		}
	}
}

func (s *qqPrivacyScope) register(realID string, role string) string {
	realID = strings.TrimSpace(realID)
	if !isLikelyQQIdentifier(realID) {
		return realID
	}
	role = normalizeQQPrivacyRole(role)
	s.mu.Lock()
	defer s.mu.Unlock()
	if alias := s.realToAlias[realID]; alias != "" {
		return alias
	}
	sum := sha256.Sum256([]byte(s.salt + "\x00" + role + "\x00" + realID))
	alias := "qq_" + role + "_" + hex.EncodeToString(sum[:6])
	s.realToAlias[realID] = alias
	s.aliasToReal[alias] = realID
	return alias
}

func normalizeQQPrivacyRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "owner":
		return "owner"
	case "current", "current_user", "sender":
		return "current_user"
	case "bot", "self":
		return "bot"
	case "group":
		return "group"
	default:
		return "user"
	}
}

func isLikelyQQIdentifier(value string) bool {
	if len(value) < 5 || len(value) > 14 || value[0] == '0' {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func (s *qqPrivacyScope) protectRequest(req llm.GenerateRequest) llm.GenerateRequest {
	protected := req
	protected.Messages = make([]llm.Message, len(req.Messages))
	for index, message := range req.Messages {
		protectedMessage := message
		protectedMessage.Content = s.protectText(message.Content)
		if len(message.Parts) > 0 {
			protectedMessage.Parts = make([]llm.ContentPart, len(message.Parts))
			for partIndex, part := range message.Parts {
				protectedPart := part
				protectedPart.Text = s.protectText(part.Text)
				protectedMessage.Parts[partIndex] = protectedPart
			}
		}
		// Agent 循环会把上一轮的工具调用连同参数回放进历史。那些参数在执行前已经被
		// 还原成真实 QQ 号，不在这里重新替换回别名，真实标识就会从历史里漏回模型，
		// 隐私代理等于白做。
		protectedMessage.ToolCalls = s.protectToolCalls(message.ToolCalls)
		protected.Messages[index] = protectedMessage
	}
	for index := range protected.Messages {
		if protected.Messages[index].Role == llm.RoleSystem {
			protected.Messages[index].Content = llmQQPrivacyPrompt + "\n\n" + protected.Messages[index].Content
			return protected
		}
	}
	protected.Messages = append([]llm.Message{{Role: llm.RoleSystem, Content: llmQQPrivacyPrompt}}, protected.Messages...)
	return protected
}

func (s *qqPrivacyScope) protectToolCalls(calls []llm.ToolCall) []llm.ToolCall {
	if s == nil || len(calls) == 0 {
		return calls
	}
	out := make([]llm.ToolCall, 0, len(calls))
	for _, call := range calls {
		protected := call
		if len(call.Arguments) > 0 {
			arguments := make(map[string]any, len(call.Arguments))
			for key, value := range call.Arguments {
				arguments[key] = s.protectValue(value)
			}
			protected.Arguments = arguments
		}
		out = append(out, protected)
	}
	return out
}

func (s *qqPrivacyScope) protectValue(value any) any {
	switch typed := value.(type) {
	case string:
		return s.protectText(typed)
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = s.protectValue(item)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, s.protectValue(item))
		}
		return out
	}
	return value
}

func (s *qqPrivacyScope) protectText(text string) string {
	if strings.TrimSpace(text) == "" {
		return text
	}
	s.discoverStructuredIDs(text)
	s.mu.Lock()
	pairs := make([][2]string, 0, len(s.realToAlias))
	for realID, alias := range s.realToAlias {
		pairs = append(pairs, [2]string{realID, alias})
	}
	s.mu.Unlock()
	sort.Slice(pairs, func(i, j int) bool { return len(pairs[i][0]) > len(pairs[j][0]) })
	for _, pair := range pairs {
		text = replaceNumericIdentifier(text, pair[0], pair[1])
	}
	return text
}

func (s *qqPrivacyScope) discoverStructuredIDs(text string) {
	for _, match := range qqPrivacyJSONIDPattern.FindAllStringSubmatch(text, -1) {
		value := firstNonEmpty(match[2], match[3])
		role := "user"
		key := strings.ToLower(match[1])
		if strings.Contains(key, "group_id") {
			role = "group"
		} else if key == "owner_id" {
			role = "owner"
		} else if key == "bot_qq" || key == "self_id" {
			role = "bot"
		}
		s.register(value, role)
	}
	for _, match := range qqPrivacyCQIDPattern.FindAllStringSubmatch(text, -1) {
		s.register(match[1], "user")
	}
	for _, match := range qqPrivacyLabelPattern.FindAllStringSubmatch(text, -1) {
		s.register(match[1], "user")
	}
}

func (s *qqPrivacyScope) restoreText(text string) string {
	if strings.TrimSpace(text) == "" {
		return text
	}
	s.mu.Lock()
	pairs := make([][2]string, 0, len(s.aliasToReal))
	for alias, realID := range s.aliasToReal {
		pairs = append(pairs, [2]string{alias, realID})
	}
	s.mu.Unlock()
	sort.Slice(pairs, func(i, j int) bool { return len(pairs[i][0]) > len(pairs[j][0]) })
	for _, pair := range pairs {
		text = strings.ReplaceAll(text, pair[0], pair[1])
	}
	return text
}

func replaceNumericIdentifier(text string, identifier string, replacement string) string {
	if identifier == "" || !strings.Contains(text, identifier) {
		return text
	}
	var builder strings.Builder
	remaining := text
	for {
		index := strings.Index(remaining, identifier)
		if index < 0 {
			builder.WriteString(remaining)
			break
		}
		beforeDigit := index > 0 && remaining[index-1] >= '0' && remaining[index-1] <= '9'
		afterIndex := index + len(identifier)
		afterDigit := afterIndex < len(remaining) && remaining[afterIndex] >= '0' && remaining[afterIndex] <= '9'
		builder.WriteString(remaining[:index])
		if beforeDigit || afterDigit {
			builder.WriteString(identifier)
		} else {
			builder.WriteString(replacement)
		}
		remaining = remaining[afterIndex:]
	}
	return builder.String()
}
