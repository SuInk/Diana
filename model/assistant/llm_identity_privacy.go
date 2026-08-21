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

const llmIdentityPrivacyPrompt = `【账号标识隐私代理】消息中的真实账号和群号已由本地代理替换为不透明别名。相同别名始终表示同一对象；chat_owner、chat_current_user、chat_bot、chat_user、chat_group 前缀保留角色语义。理解对话时按角色和昵称判断，不要猜测真实数字。调用工具或在回复中需要引用标识时，必须原样复制别名；本地代理会在执行工具或发送 OneBot v11 消息前自动恢复真实标识。`

var (
	identityPrivacyJSONIDPattern = regexp.MustCompile(`(?i)"([a-z0-9_]*(?:user_id|group_id|qq|uin)|owner_id|operator_id|self_id)"\s*:\s*(?:"([1-9][0-9]{4,13})"|([1-9][0-9]{4,13}))`)
	identityPrivacyCQIDPattern   = regexp.MustCompile(`(?i)\[CQ:(?:at|contact),[^\]]*(?:qq|id)=([1-9][0-9]{4,13})`)
	identityPrivacyLabelPattern  = regexp.MustCompile(`(?i)(?:账号|QQ群号|QQ|UIN)\s*[:：=为]?\s*([1-9][0-9]{4,13})`)
)

type identityPrivacyContextKey struct{}

type identityPrivacyContextState struct {
	enabled bool
	scope   *identityPrivacyScope
}

type identityPrivacyScope struct {
	mu          sync.Mutex
	salt        string
	realToAlias map[string]string
	aliasToReal map[string]string
}

type identityPrivacyProvider struct {
	provider LLMProvider
	scope    *identityPrivacyScope
}

func newIdentityPrivacyScope() *identityPrivacyScope {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		sum := sha256.Sum256([]byte(time.Now().String()))
		random = sum[:16]
	}
	return &identityPrivacyScope{
		salt:        hex.EncodeToString(random),
		realToAlias: map[string]string{},
		aliasToReal: map[string]string{},
	}
}

func identityPrivacyScopeFromContext(ctx context.Context) *identityPrivacyScope {
	state, _ := identityPrivacyStateFromContext(ctx)
	if state == nil || !state.enabled {
		return nil
	}
	return state.scope
}

func identityPrivacyStateFromContext(ctx context.Context) (*identityPrivacyContextState, bool) {
	if ctx == nil {
		return nil, false
	}
	state, ok := ctx.Value(identityPrivacyContextKey{}).(*identityPrivacyContextState)
	return state, ok
}

func withIdentityPrivacyScope(ctx context.Context, scope *identityPrivacyScope) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if scope == nil || identityPrivacyScopeFromContext(ctx) == scope {
		return ctx
	}
	return context.WithValue(ctx, identityPrivacyContextKey{}, &identityPrivacyContextState{enabled: true, scope: scope})
}

func (r *Runtime) withIdentityPrivacyContext(ctx context.Context, event MessageEvent, history []MessageEvent) context.Context {
	cfg := r.effectiveConfigForEvent(event)
	if !llmIdentityMaskingEnabled(cfg) {
		if ctx == nil {
			ctx = context.Background()
		}
		return context.WithValue(ctx, identityPrivacyContextKey{}, &identityPrivacyContextState{enabled: false})
	}
	scope := identityPrivacyScopeFromContext(ctx)
	if scope == nil {
		scope = newIdentityPrivacyScope()
		ctx = withIdentityPrivacyScope(ctx, scope)
	}
	scope.register(cfg.OwnerID, "owner")
	scope.register(firstNonEmpty(cfg.BotAccount, event.SelfID), "bot")
	scope.register(event.UserID, "current_user")
	scope.register(event.GroupID, "group")
	scope.registerEvent(event)
	for _, item := range history {
		scope.registerEvent(item)
	}
	return ctx
}

func llmIdentityMaskingEnabled(cfg BotConfig) bool {
	cfg = cfg.WithDefaults()
	return cfg.LLMIdentityMaskingEnabled != nil && *cfg.LLMIdentityMaskingEnabled
}

func (r *Runtime) withLLMIdentityPrivacyRun(ctx context.Context, run llmProviderRunFunc) llmProviderRunFunc {
	if run == nil {
		return run
	}
	state, hasState := identityPrivacyStateFromContext(ctx)
	if hasState && (state == nil || !state.enabled) {
		return run
	}
	if !hasState && !llmIdentityMaskingEnabled(r.Config()) {
		return run
	}
	scope := identityPrivacyScopeFromContext(ctx)
	if scope == nil {
		scope = newIdentityPrivacyScope()
	}
	return func(provider LLMProvider) (string, error) {
		return run(&identityPrivacyProvider{provider: provider, scope: scope})
	}
}

func (p *identityPrivacyProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	if p == nil || p.provider == nil {
		return nil, errors.New("chatbot: chat identity privacy provider is not configured")
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
	return &copyResponse, nil
}

func (s *identityPrivacyScope) registerEvent(event MessageEvent) {
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

func (s *identityPrivacyScope) registerSegments(segments []MessageSegment) {
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

func (s *identityPrivacyScope) register(realID string, role string) string {
	realID = strings.TrimSpace(realID)
	if !isLikelyAccountIdentifier(realID) {
		return realID
	}
	role = normalizeIdentityPrivacyRole(role)
	s.mu.Lock()
	defer s.mu.Unlock()
	if alias := s.realToAlias[realID]; alias != "" {
		return alias
	}
	sum := sha256.Sum256([]byte(s.salt + "\x00" + role + "\x00" + realID))
	alias := "chat_" + role + "_" + hex.EncodeToString(sum[:6])
	s.realToAlias[realID] = alias
	s.aliasToReal[alias] = realID
	return alias
}

func normalizeIdentityPrivacyRole(role string) string {
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

func isLikelyAccountIdentifier(value string) bool {
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

func (s *identityPrivacyScope) protectRequest(req llm.GenerateRequest) llm.GenerateRequest {
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
		protected.Messages[index] = protectedMessage
	}
	for index := range protected.Messages {
		if protected.Messages[index].Role == llm.RoleSystem {
			protected.Messages[index].Content = llmIdentityPrivacyPrompt + "\n\n" + protected.Messages[index].Content
			return protected
		}
	}
	protected.Messages = append([]llm.Message{{Role: llm.RoleSystem, Content: llmIdentityPrivacyPrompt}}, protected.Messages...)
	return protected
}

func (s *identityPrivacyScope) protectText(text string) string {
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

func (s *identityPrivacyScope) discoverStructuredIDs(text string) {
	for _, match := range identityPrivacyJSONIDPattern.FindAllStringSubmatch(text, -1) {
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
	for _, match := range identityPrivacyCQIDPattern.FindAllStringSubmatch(text, -1) {
		s.register(match[1], "user")
	}
	for _, match := range identityPrivacyLabelPattern.FindAllStringSubmatch(text, -1) {
		s.register(match[1], "user")
	}
}

func (s *identityPrivacyScope) restoreText(text string) string {
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
