// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/SuInk/diana/model/llm"
)

// identityAliasPrefix 是所有脱敏别名的共同前缀。
//
// 刻意不带平台名：同一套脱敏要服务 QQ、Telegram 以及以后接入的平台，叫 qq_ 会让
// 模型以为当前一定是 QQ。im_ 取 instant messaging，平台中立。
const identityAliasPrefix = "im_"

// identityAliasRoles 是别名里出现过的全部角色，顺序即提示词里的列举顺序。
// 除 message 外都来自 normalizeIdentityPrivacyRole 的返回值。
//
// 主人这一档叫 bot_owner 而不是 owner：群成员角色里的 owner 是群主，两个词撞在
// 一起，模型看到 im_owner_xxx 就会把机器人的主人说成群主。加上 bot_ 之后，光秃
// 秃的 owner 就只剩群主一个意思。
var identityAliasRoles = []string{"bot_owner", "current_user", "bot", "user", "group", "message"}

// llmIdentityPrivacyPrompt 里的前缀由 identityAliasPrefix 拼出来，不写死。
//
// 这里原本是手写的 im_owner、im_message_xxx 一串字面量。前缀从 qq_ 改成 im_ 那次，
// 提示词跟着改了，散落在别处讲同一件事的注释没跟上，于是照着注释找 bug 的人会被
// 带到一个已经不存在的前缀上。拼出来之后，改前缀这一处就够了。
var llmIdentityPrivacyPrompt = llmIdentityPrivacyIntro + llmIdentityPrivacyContract

// 拆成两段：前半段是说明，可以改措辞；后半段教模型原样复制别名，代理靠它把别名换回
// 真实标识，锁定为 Contract。
//
// 「用户问号码就写别名」放在 Contract 里：只说「不要猜测真实数字」时，模型会把别名
// 当成拿不到的东西，用户问群号就回「获取不到群号」，调群工具也不敢填。改过前半段的
// 人设同样需要这句，所以不能放在可改的说明里。
var (
	llmIdentityPrivacyIntro = "【会话标识隐私代理】消息中的真实用户 ID、群 ID 和消息 ID 已由本地代理替换为不透明别名。相同别名始终表示同一对象；" +
		identityAliasRoleList() + " 前缀保留角色语义。理解对话时按角色和昵称判断，不要自己猜测或编造真实数字。"
	llmIdentityPrivacyContract = "调用工具或在回复中需要引用标识时，必须原样复制别名——包括 [diana-reply:" + identityAlias("message") + "xxx]、" +
		"[diana-at:" + identityAlias("user") + "xxx] 这类标记；本地代理会在执行工具或发送消息前自动恢复真实标识。" +
		"别名就代表真实号码：用户问群号、QQ 号时，直接在回复里写出对应别名，发出去就是真实号码，不要说获取不到；" +
		"工具需要当前群或某个人时，照抄 " + identityAlias("group") + "、" + identityAlias("user") + " 这类别名填进参数。"
)

var promptIdentityPrivacySpec = registerPrompt(PromptSpec{
	Key:      "reply.identity_privacy",
	Group:    PromptGroupReplyRules,
	Title:    "会话标识隐私代理",
	Usage:    "开启「对模型隐藏账号 ID」时，加在每次请求第一条 system 消息最前面，说明账号和消息 ID 已换成别名。末尾「原样复制别名」那句是锁定的，代理靠它把别名换回真实标识。",
	Default:  llmIdentityPrivacyIntro,
	Contract: llmIdentityPrivacyContract,
})

// identityAlias 拼出某个角色的别名前缀，例如 im_message_。
func identityAlias(role string) string {
	return identityAliasPrefix + role + "_"
}

// identityAliasRoleList 把角色前缀连成提示词里那串顿号分隔的列举。
func identityAliasRoleList() string {
	parts := make([]string, 0, len(identityAliasRoles))
	for _, role := range identityAliasRoles {
		parts = append(parts, identityAliasPrefix+role)
	}
	return strings.Join(parts, "、")
}

var (
	identityPrivacyAliasTokenPattern = regexp.MustCompile(`im_[A-Za-z0-9_]+`)
	identityPrivacyJSONIDPattern     = regexp.MustCompile(`(?i)"([a-z0-9_]*(?:user_id|group_id|qq|uin)|owner_id|operator_id|self_id)"\s*:\s*(?:"([^"\\]+)"|([1-9][0-9]{4,13}))`)
	identityPrivacyCQIDPattern       = regexp.MustCompile(`(?i)\[CQ:(?:at|contact),[^\]]*(?:qq|id)=([1-9][0-9]{4,13})`)
	identityPrivacyLabelPattern      = regexp.MustCompile(`(?i)(?:QQ号|QQ群号|QQ|UIN)\s*[:：=为]?\s*([1-9][0-9]{4,13})`)
	// 消息 ID 单独匹配：它允许负号，长度范围也和 QQ 号不同。
	identityPrivacyMessageIDPattern   = regexp.MustCompile(`(?i)"([a-z0-9_]*message_ids?)"\s*:\s*(?:"(-?[0-9]{4,19})"|(-?[0-9]{4,19}))`)
	identityPrivacyReplyMarkerPattern = regexp.MustCompile(`\[(?:diana-reply|回复):(-?[0-9]{4,19})\]`)
	// 提及标记里的 id 也要脱敏。它通常在候选 JSON 里已经登记过，但提示词里
	// 只出现在标记中的那一个（当前发言者）不该漏网。
	identityPrivacyMentionMarkerPattern = regexp.MustCompile(`\[diana-at:([1-9][0-9]{4,13})\]`)
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
	// overrides 是建 scope 时那台机器人的提示词覆盖，只用来取隐私说明的正文。
	overrides PromptOverrides
}

type identityPrivacyProvider struct {
	provider LLMProvider
	scope    *identityPrivacyScope
}

// IdentityAliasSaltStore 持久化脱敏别名的盐。
//
// 盐必须跨进程重启保持不变，否则每次重启所有别名都会换一遍，等于把历史缓存全部作废。
type IdentityAliasSaltStore interface {
	LoadIdentityAliasSalt(ctx context.Context) (string, error)
	SaveIdentityAliasSalt(ctx context.Context, salt string) error
}

func newIdentityAliasSalt() string {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		sum := sha256.Sum256([]byte(time.Now().String()))
		random = sum[:16]
	}
	return hex.EncodeToString(random)
}

func newIdentityPrivacyScope() *identityPrivacyScope {
	return newIdentityPrivacyScopeWithSalt(newIdentityAliasSalt())
}

func newIdentityPrivacyScopeWithSalt(salt string) *identityPrivacyScope {
	if strings.TrimSpace(salt) == "" {
		salt = newIdentityAliasSalt()
	}
	return &identityPrivacyScope{
		salt:        salt,
		realToAlias: map[string]string{},
		aliasToReal: map[string]string{},
	}
}

// identityAliasSalt 返回全局固定的盐，必要时生成并落库。
//
// 这里原本是每个 scope 一个随机盐，而 scope 每轮对话新建一次。后果是同一个账号每轮
// 拿到不同的别名——别名遍布每一条历史行，线上单条请求里出现 982 处——于是整段历史
// 文本逐字不同，供应商的前缀缓存在历史这一段永远不可能命中。
//
// 本机实测：同一轮 agent 内的三次调用别名一致，换一轮就全变：
//
//	19:24:35  im_bot_owner_d8922d4b7dbd
//	19:24:41  im_bot_owner_d8922d4b7dbd
//	19:25:21  im_bot_owner_d8922d4b7dbd
//	19:27:25  im_bot_owner_1bfe23ea72dc   ← 换轮，全变
//
// 改成全局固定后，同一个账号在任何时间、任何会话都是同一个别名，历史可以整段命中。
// 盐落库保证重启后不变；没有存储时退回进程级，至少单进程内稳定。
func (r *Runtime) identityAliasSalt(ctx context.Context) string {
	r.mu.Lock()
	if r.aliasSalt != "" {
		salt := r.aliasSalt
		r.mu.Unlock()
		return salt
	}
	store, _ := r.messageStore.(IdentityAliasSaltStore)
	r.mu.Unlock()

	if store != nil {
		loadCtx, cancel := context.WithTimeout(ctx, auditPersistTimeout)
		saved, err := store.LoadIdentityAliasSalt(loadCtx)
		cancel()
		if err == nil && strings.TrimSpace(saved) != "" {
			r.mu.Lock()
			r.aliasSalt = saved
			r.mu.Unlock()
			return saved
		}
	}

	salt := newIdentityAliasSalt()
	r.mu.Lock()
	if r.aliasSalt != "" {
		// 并发下别人已经定了，用它的，保证全进程一个值。
		salt = r.aliasSalt
		r.mu.Unlock()
		return salt
	}
	r.aliasSalt = salt
	r.mu.Unlock()

	if store != nil {
		saveCtx, cancel := context.WithTimeout(ctx, auditPersistTimeout)
		err := store.SaveIdentityAliasSalt(saveCtx, salt)
		// 落库是「没有才写」，所以写完要回读一次：别的进程先写过的话，以库里那个为准，
		// 否则两个进程各用各的盐，别名还是对不上。
		if err == nil {
			if stored, loadErr := store.LoadIdentityAliasSalt(saveCtx); loadErr == nil && strings.TrimSpace(stored) != "" && stored != salt {
				salt = stored
				r.mu.Lock()
				r.aliasSalt = salt
				r.mu.Unlock()
			}
		} else {
			log.Printf("diana identity privacy: 别名盐落库失败，重启后别名会变、历史缓存会失效: %v", err)
		}
		cancel()
	}
	return salt
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
		scope = newIdentityPrivacyScopeWithSalt(r.identityAliasSalt(ctx))
		scope.overrides = cfg.PromptOverrides
		ctx = withIdentityPrivacyScope(ctx, scope)
	}
	scope.register(cfg.OwnerIDForEvent(event), "bot_owner")
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
	if !hasState && !llmIdentityMaskingEnabled(r.configForContext(ctx)) {
		return run
	}
	scope := identityPrivacyScopeFromContext(ctx)
	if scope == nil {
		// 兜底路径同样要用全局盐，否则这一条链路的别名会和主路径对不上。
		scope = newIdentityPrivacyScopeWithSalt(r.identityAliasSalt(ctx))
		scope.overrides = r.configForContext(ctx).PromptOverrides
	}
	return func(provider LLMProvider) (string, error) {
		return run(&identityPrivacyProvider{provider: provider, scope: scope})
	}
}

func (p *identityPrivacyProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	if p == nil || p.provider == nil {
		return nil, errors.New("diana: identity privacy provider is not configured")
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
	// 工具——提醒工具收到 im_user_f49c630bf7cf 这种值，只能报「必须是有效账号」。
	copyResponse.ToolCalls = p.scope.restoreToolCalls(response.ToolCalls)
	return &copyResponse, nil
}

// restoreToolCalls 把工具参数里的别名换回真实标识。参数是任意 JSON 结构，字符串可能
// 藏在嵌套的对象或数组里，所以要递归走一遍。
func (s *identityPrivacyScope) restoreToolCalls(calls []llm.ToolCall) []llm.ToolCall {
	if s == nil || len(calls) == 0 {
		return calls
	}
	out := make([]llm.ToolCall, 0, len(calls))
	for _, call := range calls {
		restored := call
		if len(call.Arguments) > 0 {
			arguments := make(map[string]any, len(call.Arguments))
			for key, value := range call.Arguments {
				// 执行前校验：身份类参数里还原不出来的别名一律清掉，不放行成垃圾字符串。
				if cleared, rejected := s.rejectUnresolvableIdentityArgument(key, value); rejected {
					arguments[key] = cleared
					continue
				}
				arguments[key] = s.restoreValue(value)
			}
			restored.Arguments = arguments
		}
		out = append(out, restored)
	}
	return out
}

// identityArgumentKeyPattern 标出「这个参数是一个身份标识」的参数名。
//
// 只对这些键做严格校验：自由文本参数（要发送的正文、搜索词）里出现别名形态的字符串
// 是正常的——用户就在聊这个——整段拒绝会把正常功能一起拒掉。
var identityArgumentKeyPattern = regexp.MustCompile(`(?i)(^|_)(user|group|owner|operator|target|sender|member|message)_?ids?$|^(user|group|target|qq|uin)$`)

// rejectUnresolvableIdentityArgument 在工具拿到参数之前清掉还原不出来的身份标识。
//
// 别名只可能由本轮的隐私代理生成。一个 im_ 开头的标识还原不出真实账号，就说明它不是
// 本轮提供的候选——要么模型自己编的，要么是从用户正文里抄来的伪造标识（用户完全可以
// 在消息里手写 im_bot_owner_deadbeef，而系统提示词恰恰告诉模型这个前缀带角色语义）。
//
// 以前这种值是「原样返回」，于是伪造标识直接进到工具内部。多数工具拿它比对真实 ID 会
// 失败，算是兜住了，但兜住的方式是「碰巧没匹配」而不是「明确拒绝」，而且这个由攻击者
// 控制的字符串还会出现在错误信息里。
//
// 清成空串之后，每个工具现成的必填校验都会拒绝它，并给出自己那句明确的提示（例如
// 「请指定目标账号 ID 或引用目标消息」），模型据此就知道要改用 identity_check 核实或
// 从本轮候选里逐字复制，不需要在执行层再铺一套管道。
func (s *identityPrivacyScope) rejectUnresolvableIdentityArgument(key string, value any) (any, bool) {
	if s == nil || !identityArgumentKeyPattern.MatchString(strings.TrimSpace(key)) {
		return value, false
	}
	text, ok := value.(string)
	if !ok || text == "" {
		return value, false
	}
	for _, alias := range identityPrivacyAliasTokenPattern.FindAllString(text, -1) {
		s.mu.Lock()
		_, known := s.aliasToReal[alias]
		s.mu.Unlock()
		if !known {
			log.Printf("diana identity privacy: 工具参数 %s 含无法还原的标识 %q，已清空（不是本轮候选）", key, alias)
			return "", true
		}
	}
	return value, false
}

func (s *identityPrivacyScope) restoreValue(value any) any {
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

func (s *identityPrivacyScope) registerEvent(event MessageEvent) {
	for _, mention := range event.MentionTargets {
		s.register(mention.UserID, "user")
	}
	s.register(event.UserID, "user")
	s.register(event.OperatorID, "user")
	s.register(event.GroupID, "group")
	s.registerMessageID(event.MessageID)
	if event.Quoted != nil {
		s.register(event.Quoted.UserID, "user")
		s.register(event.Quoted.GroupID, "group")
		s.registerMessageID(event.Quoted.MessageID)
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
			case "id", "message_id", "source_message_id":
				// reply 段的 id 指向被引用的那条消息。
				if strings.EqualFold(strings.TrimSpace(segment.Type), "reply") || strings.Contains(strings.ToLower(key), "message") {
					s.registerMessageID(value)
				}
			}
		}
	}
}

func (s *identityPrivacyScope) register(realID string, role string) string {
	realID = strings.TrimSpace(realID)
	if !isLikelyChatIdentifier(realID) && !isOpaqueChatIdentifier(realID) {
		return realID
	}
	role = normalizeIdentityPrivacyRole(role)
	s.mu.Lock()
	defer s.mu.Unlock()
	if alias := s.realToAlias[realID]; alias != "" {
		return alias
	}
	sum := sha256.Sum256([]byte(s.salt + "\x00" + role + "\x00" + realID))
	alias := identityAlias(role) + hex.EncodeToString(sum[:6])
	s.realToAlias[realID] = alias
	s.aliasToReal[alias] = realID
	return alias
}

func normalizeIdentityPrivacyRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "owner", "bot_owner":
		return "bot_owner"
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

// isLikelyMessageID 判定消息 ID。和 QQ 号不同，它允许前导负号，也允许比 QQ 号更短，
// OneBot 的消息 ID 就有负数形式。
func isLikelyMessageID(value string) bool {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "-")
	if len(value) < 4 || len(value) > 20 {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

// registerMessageID 给消息 ID 建别名。负号留在别名外面：正文里出现的是 -12345，
// 只把数字部分换掉会剩下一个孤零零的减号，所以连符号一起注册。
func (s *identityPrivacyScope) registerMessageID(realID string) string {
	realID = strings.TrimSpace(realID)
	if !isLikelyMessageID(realID) {
		return realID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if alias := s.realToAlias[realID]; alias != "" {
		return alias
	}
	sum := sha256.Sum256([]byte(s.salt + "\x00message\x00" + realID))
	alias := identityAlias("message") + hex.EncodeToString(sum[:6])
	s.realToAlias[realID] = alias
	s.aliasToReal[alias] = realID
	return alias
}

func isLikelyChatIdentifier(value string) bool {
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

// Opaque platform IDs (for example Feishu open_id and DingTalk staff IDs)
// are trusted when supplied by an event or an explicit identity field. Keep
// the existing numeric-ID heuristic and never register an already masked ID.
func isOpaqueChatIdentifier(value string) bool {
	if value == "" || value == "all" || strings.HasPrefix(value, identityAliasPrefix) {
		return false
	}
	hasLetter := false
	for _, char := range value {
		if unicode.IsSpace(char) || unicode.IsControl(char) {
			return false
		}
		hasLetter = hasLetter || unicode.IsLetter(char)
	}
	return hasLetter
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
		// Agent 循环会把上一轮的工具调用连同参数回放进历史。那些参数在执行前已经被
		// 还原成真实 QQ 号，不在这里重新替换回别名，真实标识就会从历史里漏回模型，
		// 隐私代理等于白做。
		protectedMessage.ToolCalls = s.protectToolCalls(message.ToolCalls)
		protected.Messages[index] = protectedMessage
	}
	privacyPrompt := s.overrides.text(promptIdentityPrivacySpec)
	for index := range protected.Messages {
		if protected.Messages[index].Role == llm.RoleSystem {
			protected.Messages[index].Content = privacyPrompt + "\n\n" + protected.Messages[index].Content
			return protected
		}
	}
	protected.Messages = append([]llm.Message{{Role: llm.RoleSystem, Content: privacyPrompt}}, protected.Messages...)
	return protected
}

func (s *identityPrivacyScope) protectToolCalls(calls []llm.ToolCall) []llm.ToolCall {
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

func (s *identityPrivacyScope) protectValue(value any) any {
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
		text = replacePrivacyIdentifier(text, pair[0], pair[1])
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
			role = "bot_owner"
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
	// 消息 ID 也要脱敏，否则模型手里握着一批真实 ID。入站渲染的引用标记和结构化
	// 载荷里的 message_id 都要认，不然历史里出现过、但事件里没登记的那些会漏网。
	for _, match := range identityPrivacyMessageIDPattern.FindAllStringSubmatch(text, -1) {
		s.registerMessageID(firstNonEmpty(match[2], match[3]))
	}
	for _, match := range identityPrivacyReplyMarkerPattern.FindAllStringSubmatch(text, -1) {
		s.registerMessageID(match[1])
	}
	for _, match := range identityPrivacyMentionMarkerPattern.FindAllStringSubmatch(text, -1) {
		s.register(match[1], "user")
	}
}

func (s *identityPrivacyScope) restoreText(text string) string {
	if strings.TrimSpace(text) == "" {
		return text
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// A valid alias followed by extra digits is a different, unknown identifier.
	// Substring replacement would turn that typo into a different numeric ID.
	return identityPrivacyAliasTokenPattern.ReplaceAllStringFunc(text, func(alias string) string {
		if realID, ok := s.aliasToReal[alias]; ok {
			return realID
		}
		return alias
	})
}

func replacePrivacyIdentifier(text string, identifier string, replacement string) string {
	opaque := isOpaqueChatIdentifier(identifier)
	isBoundaryByte := func(value byte) bool {
		if value >= '0' && value <= '9' {
			return true
		}
		return opaque && ((value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') || value == '_' || value == '-')
	}
	if identifier == "" || !strings.Contains(text, identifier) {
		return text
	}
	var builder strings.Builder
	remaining := text
	consumed := 0
	for {
		index := strings.Index(remaining, identifier)
		if index < 0 {
			builder.WriteString(remaining)
			break
		}
		beforeToken := consumed+index > 0 && isBoundaryByte(text[consumed+index-1])
		afterIndex := index + len(identifier)
		afterToken := afterIndex < len(remaining) && isBoundaryByte(remaining[afterIndex])
		builder.WriteString(remaining[:index])
		if beforeToken || afterToken {
			builder.WriteString(identifier)
		} else {
			builder.WriteString(replacement)
		}
		consumed += afterIndex
		remaining = remaining[afterIndex:]
	}
	return builder.String()
}
