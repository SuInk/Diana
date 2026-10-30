// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/SuInk/diana/internal/secretmask"
	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// 敏感值隐私代理：密钥、令牌、密码、身份证号、银行卡号、手机号和邮箱在发给模型前
// 换成 pv_api_key_3f9a1c2e4b7d 这类占位符，模型返回后再换回来。和账号隐私代理
// （llm_identity_privacy.go）是同一套做法，套在它里面一层：先换账号，再换敏感值，
// 11 位的 QQ 号就不会被当成手机号。
//
// 起因是模型重抄长串会抄错：用户发来一个 164 位的 OpenAI 密钥，第一次测试照抄对了，
// 第二次漏了中间一个字符，接口回 401，模型就跟用户说「密钥可能被撤销了」，下一次
// 又照着自己抄错的那份再抄一遍。换成占位符以后模型只抄 12 位十六进制，执行前由这里
// 换回原文；占位符抄错了还原不出来，执行层直接拦下（secretmask.UnresolvedPlaceholderError）。
// 原值也不再交给模型供应商。
//
// 换回时分两种：
//
//   - 工具参数一律换回原值，命令才跑得通。
//   - 要说给人看的话（回复正文和 send 工具的参数）里，密钥只换回掩码（sk-p****upkA），
//     不把原文发进聊天；个人信息只有本会话里出现过的才换回原值，从记忆、跨群联想带
//     进来的只给掩码，不把别处的手机号说到这个群里。
//
// 占位符由全局固定的盐和原值算出来，同一个值在哪、什么时候都是同一个占位符，历史
// 逐字不变，上游的前缀缓存照常命中。原值只放在内存里：每次请求都从原文重新认一遍，
// 重启不丢东西。

// secretPrivacyContextValueLimit 是全局记住的「只能靠上下文认出来」的值的上限。
const secretPrivacyContextValueLimit = 512

// secretPrivacySecretKinds 是按密钥处理的类别，其余是个人信息。
var secretPrivacySecretKinds = map[string]bool{"api_key": true, "private_key": true, "secret": true}

var (
	llmSecretPrivacyIntro = "【敏感信息隐私代理】消息和工具结果里的密钥、令牌、密码、身份证号、银行卡号、手机号和邮箱，已由本地代理换成 " +
		secretmask.PlaceholderPrefix + "api_key_…、" + secretmask.PlaceholderPrefix + "phone_… 这类占位符。相同占位符始终代表同一个值，它就是那个值本身，不是拿不到。"
	llmSecretPrivacyContract = "命令、请求头和工具参数要用这些值时，必须逐字照抄占位符，不要改写、截断、拼接或猜测原文；本地代理会在执行前换回原值。" +
		"回复里写出占位符时，密钥只会显示成掩码，不要为了让对方看到原文而拆开或转述占位符。"
)

var promptSecretPrivacySpec = registerPrompt(PromptSpec{
	Key:      "reply.secret_privacy",
	Group:    PromptGroupReplyRules,
	Title:    "敏感信息隐私代理",
	Usage:    "开启「对模型隐藏密钥和个人信息」时，加在每次请求第一条 system 消息最前面，说明这些值已换成占位符。末尾「逐字照抄占位符」那句是锁定的，代理靠它把占位符换回原值。",
	Default:  llmSecretPrivacyIntro,
	Contract: llmSecretPrivacyContract,
})

func llmSecretMaskingEnabled(cfg BotConfig) bool {
	cfg = cfg.WithDefaults()
	return cfg.LLMSecretMaskingEnabled != nil && *cfg.LLMSecretMaskingEnabled
}

// secretSpan 是正文里认出来的一段敏感值。
type secretSpan struct {
	start, end int
	kind       string
}

// secretRule 是一条按形态认的规则。group 为 0 取整段匹配，否则取那一组。
type secretRule struct {
	kind  string
	re    *regexp.Regexp
	group int
	ok    func(value string) bool
	// key 是规则里键名所在的组，非 0 时键名要过 secretmask.SensitiveName。
	key int
	// bounded 要求前后不紧挨字母数字，否则是更长编号里的一段。
	bounded bool
}

var (
	// secretHeaderPattern 认出请求头写法的凭据，第 1 组是去掉认证方案后的凭据本身。
	secretHeaderPattern = regexp.MustCompile(`(?i)\b(?:(?:proxy-)?authorization|x-api-key|api-key|x-goog-api-key|x-auth-token|private-token)\s*[:=]\s*(?:(?:bearer|token|basic)\s+)?([^\s,;)\]}'"]+)`)
	// secretBearerPattern 认出单独出现的 Bearer 凭据。
	secretBearerPattern = regexp.MustCompile(`(?i)\bbearer\s+([A-Za-z0-9._~+/=-]{12,})`)
	// secretCJKPattern 认出聊天里的中文写法：「密码是 xxx」「密钥：xxx」。
	secretCJKPattern = regexp.MustCompile(`(?i)(?:密码|口令|密钥|秘钥|令牌|token|api ?key)\s*(?:是|为|[:：=])\s*([!-~]{6,})`)
)

var secretRules = []secretRule{
	{kind: "private_key", re: secretmask.PrivateKeyPattern},
	{kind: "api_key", re: secretmask.TokenPattern},
	{kind: "secret", re: secretmask.ConnectionPasswordPattern, group: 1, ok: plausibleSecretValue},
	{kind: "secret", re: secretHeaderPattern, group: 1, ok: plausibleSecretValue},
	{kind: "secret", re: secretBearerPattern, group: 1, ok: func(value string) bool {
		return plausibleSecretValue(value) && strings.ContainsAny(value, "0123456789")
	}},
	{kind: "secret", re: secretmask.QuotedCredentialPattern, group: 3, key: 2, ok: plausibleSecretValue},
	{kind: "secret", re: secretmask.CredentialPattern, group: 3, key: 1, ok: plausibleSecretValue},
	{kind: "secret", re: secretCJKPattern, group: 1, ok: plausibleSecretValue},
	{kind: "id_card", re: secretmask.IDCardPattern, bounded: true, ok: secretmask.ValidIDCard},
	{kind: "bank_card", re: secretmask.BankCardPattern, bounded: true, ok: secretmask.ValidLuhn},
	{kind: "phone", re: secretmask.PhonePattern},
	{kind: "email", re: secretmask.EmailPattern, ok: plausiblePersonalEmail},
}

// plausibleSecretValue 过滤「键名像凭据、值却不是」的写法：占位符和掩码本身、模板
// 变量、纯数字（max_tokens: 4096）、太短的值和带中文的句子。
func plausibleSecretValue(value string) bool {
	value = strings.Trim(value, `"'`)
	if len(value) < 6 || strings.Contains(value, secretmask.Marker) || strings.Contains(value, "REDACTED") {
		return false
	}
	if strings.HasPrefix(value, secretmask.PlaceholderPrefix) || strings.HasPrefix(value, identityAliasPrefix) {
		return false
	}
	if strings.ContainsAny(value, "<>{}$%`") {
		return false
	}
	digits := true
	for i := 0; i < len(value); i++ {
		if value[i] < '!' || value[i] > '~' {
			return false
		}
		digits = digits && value[i] >= '0' && value[i] <= '9'
	}
	if digits {
		return false
	}
	switch strings.ToLower(value) {
	// 认证方案名：「Authorization: Bearer xxx」按键值规则取到的是 Bearer，凭据在后面。
	case "bearer", "basic", "digest", "negotiate",
		"undefined", "required", "optional", "missing", "enabled", "disabled":
		return false
	}
	return true
}

// plausiblePersonalEmail 放过不属于具体某个人的地址：git@github.com 这种 SSH 远端、
// noreply 和文档里的示例域名。
func plausiblePersonalEmail(value string) bool {
	local, domain, ok := strings.Cut(strings.ToLower(value), "@")
	if !ok {
		return false
	}
	if local == "git" || strings.Contains(local, "noreply") || strings.Contains(local, "no-reply") {
		return false
	}
	switch domain {
	case "example.com", "example.org", "example.net", "users.noreply.github.com":
		return false
	}
	return true
}

// findSecretSpans 按形态找出正文里的敏感值，重叠时取起点靠前、更长的那段；同一段
// 被几条规则认出时取排在前面的规则，类别每次都一样，占位符才不会变。
func findSecretSpans(text string) []secretSpan {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var spans []secretSpan
	for _, rule := range secretRules {
		for _, match := range rule.re.FindAllStringSubmatchIndex(text, -1) {
			start, end := match[0], match[1]
			if rule.group > 0 {
				start, end = match[2*rule.group], match[2*rule.group+1]
			}
			if start < 0 || end <= start {
				continue
			}
			if rule.key > 0 && match[2*rule.key] >= 0 && !secretmask.SensitiveName(text[match[2*rule.key]:match[2*rule.key+1]]) {
				continue
			}
			// 带引号的值只换引号里面那段，引号留着 JSON 才不坏。
			if end-start >= 2 && (text[start] == '"' || text[start] == '\'') && text[end-1] == text[start] {
				start, end = start+1, end-1
			}
			if rule.bounded && (secretBoundary(text, start-1) || secretBoundary(text, end)) {
				continue
			}
			value := text[start:end]
			if rule.ok != nil && !rule.ok(value) {
				continue
			}
			spans = append(spans, secretSpan{start: start, end: end, kind: rule.kind})
		}
	}
	return resolveSecretSpans(spans)
}

func resolveSecretSpans(spans []secretSpan) []secretSpan {
	if len(spans) < 2 {
		return spans
	}
	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i].start != spans[j].start {
			return spans[i].start < spans[j].start
		}
		return spans[i].end > spans[j].end
	})
	out := spans[:0]
	end := -1
	for _, span := range spans {
		if span.start < end {
			continue
		}
		out = append(out, span)
		end = span.end
	}
	return out
}

// secretContextRegistry 记住只能靠上下文认出来的值（「密码是 xxx」「api_key=xxx」
// 里的 xxx），整个进程共用。带形态的值在哪都认得出来，不必记；这类值换了个说法就
// 认不出了：模型把命令输出里的密码抄进下一条命令，或者这句话掉出历史窗口后它在别处
// 又出现。见过一次就一直认，和 identityAliasRegistry 一个道理。
type secretContextRegistry struct {
	mu     sync.RWMutex
	values map[string]bool
	order  []string
}

func (g *secretContextRegistry) remember(value string) {
	if g == nil || value == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.values == nil {
		g.values = map[string]bool{}
	}
	if g.values[value] {
		return
	}
	g.values[value] = true
	g.order = append(g.order, value)
	if over := len(g.order) - secretPrivacyContextValueLimit; over > 0 {
		for _, old := range g.order[:over] {
			delete(g.values, old)
		}
		g.order = append([]string(nil), g.order[over:]...)
	}
}

func (g *secretContextRegistry) snapshot() []string {
	if g == nil {
		return nil
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return append([]string(nil), g.order...)
}

type secretPrivacyEntry struct {
	value string
	kind  string
}

// secretPrivacyScope 是一次模型调用的对照表：请求里换掉了哪些值，响应里就只认这些。
type secretPrivacyScope struct {
	mu        sync.Mutex
	salt      string
	overrides PromptOverrides
	// local 是本会话消息里出现过的值，个人信息只有它们会在回复里换回原文。
	local map[string]bool
	known *secretContextRegistry
	// contextValues 是本次请求里靠上下文认出来的值，正文别处原样出现时一起换掉。
	contextValues map[string]bool
	entries       map[string]secretPrivacyEntry
	// spans 缓存每段正文按形态认出的结果：一次请求先认一遍收集上下文值，再认一遍
	// 替换，历史有一百多条，不必跑两遍正则。
	spans map[string][]secretSpan
}

func newSecretPrivacyScope(salt string, local map[string]bool, known *secretContextRegistry) *secretPrivacyScope {
	return &secretPrivacyScope{
		salt:          salt,
		local:         local,
		known:         known,
		contextValues: map[string]bool{},
		entries:       map[string]secretPrivacyEntry{},
		spans:         map[string][]secretSpan{},
	}
}

func (s *secretPrivacyScope) placeholder(kind, value string) string {
	sum := sha256.Sum256([]byte(s.salt + "\x00secret\x00" + value))
	placeholder := secretmask.PlaceholderPrefix + kind + "_" + hex.EncodeToString(sum[:6])
	s.mu.Lock()
	s.entries[placeholder] = secretPrivacyEntry{value: value, kind: kind}
	s.mu.Unlock()
	return placeholder
}

// discover 先把整个请求过一遍，收齐靠上下文认出来的值。一条消息里写着「密码是
// xxx」，同一个 xxx 出现在后面的工具参数里也得换掉。
func (s *secretPrivacyScope) shapeSpans(text string) []secretSpan {
	s.mu.Lock()
	spans, ok := s.spans[text]
	s.mu.Unlock()
	if ok {
		return spans
	}
	spans = findSecretSpans(text)
	s.mu.Lock()
	s.spans[text] = spans
	s.mu.Unlock()
	return spans
}

func (s *secretPrivacyScope) discover(text string) {
	for _, span := range s.shapeSpans(text) {
		if span.kind != "secret" {
			continue
		}
		value := text[span.start:span.end]
		s.mu.Lock()
		s.contextValues[value] = true
		s.mu.Unlock()
		s.known.remember(value)
	}
}

func (s *secretPrivacyScope) protectText(text string) string {
	if strings.TrimSpace(text) == "" {
		return text
	}
	spans := append([]secretSpan(nil), s.shapeSpans(text)...)
	spans = append(spans, s.contextSpans(text)...)
	spans = resolveSecretSpans(spans)
	if len(spans) == 0 {
		return text
	}
	var builder strings.Builder
	last := 0
	for _, span := range spans {
		builder.WriteString(text[last:span.start])
		builder.WriteString(s.placeholder(span.kind, text[span.start:span.end]))
		last = span.end
	}
	builder.WriteString(text[last:])
	return builder.String()
}

// contextSpans 找出靠上下文认出来的值在正文别处的原样出现。短的值前后紧挨字母数字
// 时不算，那多半是更长的一串里碰巧有这一段；长的值碰巧撞上的机会很小，而
// mysql -pPASSWORD 这种紧贴参数名的写法很常见，照样换。
func (s *secretPrivacyScope) contextSpans(text string) []secretSpan {
	s.mu.Lock()
	values := make([]string, 0, len(s.contextValues))
	for value := range s.contextValues {
		values = append(values, value)
	}
	s.mu.Unlock()
	values = append(values, s.known.snapshot()...)
	var spans []secretSpan
	for _, value := range values {
		for offset := 0; ; {
			index := strings.Index(text[offset:], value)
			if index < 0 {
				break
			}
			start := offset + index
			end := start + len(value)
			if len(value) >= 10 || !secretBoundary(text, start-1) && !secretBoundary(text, end) {
				spans = append(spans, secretSpan{start: start, end: end, kind: "secret"})
			}
			offset = end
		}
	}
	return spans
}

func secretBoundary(text string, index int) bool {
	if index < 0 || index >= len(text) {
		return false
	}
	c := text[index]
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func (s *secretPrivacyScope) protectRequest(req llm.GenerateRequest) llm.GenerateRequest {
	for _, message := range req.Messages {
		s.discover(message.Content)
		for _, part := range message.Parts {
			s.discover(part.Text)
		}
		for _, call := range message.ToolCalls {
			walkStrings(call.Arguments, s.discover)
		}
	}
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
		// 上一轮的工具调用连同参数会回放进历史，那些参数执行前已经换回了原值。
		if len(message.ToolCalls) > 0 {
			protectedMessage.ToolCalls = make([]llm.ToolCall, len(message.ToolCalls))
			for callIndex, call := range message.ToolCalls {
				protectedCall := call
				protectedCall.Arguments = mapStrings(call.Arguments, s.protectText)
				protectedMessage.ToolCalls[callIndex] = protectedCall
			}
		}
		protected.Messages[index] = protectedMessage
	}
	privacyPrompt := s.overrides.text(promptSecretPrivacySpec)
	for index := range protected.Messages {
		if protected.Messages[index].Role == llm.RoleSystem {
			protected.Messages[index].Content = privacyPrompt + "\n\n" + protected.Messages[index].Content
			return protected
		}
	}
	protected.Messages = append([]llm.Message{{Role: llm.RoleSystem, Content: privacyPrompt}}, protected.Messages...)
	return protected
}

// restoreForTool 把占位符换回原值，给工具执行用。
func (s *secretPrivacyScope) restoreForTool(text string) string {
	return s.restore(text, func(entry secretPrivacyEntry) string { return entry.value })
}

// restoreForReply 把占位符换回要说给人看的样子：密钥给掩码，个人信息本会话里出现
// 过的给原文，其余给掩码。
func (s *secretPrivacyScope) restoreForReply(text string) string {
	return s.restore(text, func(entry secretPrivacyEntry) string {
		if !secretPrivacySecretKinds[entry.kind] && s.local[entry.value] {
			return entry.value
		}
		return maskSecretForReply(entry)
	})
}

func (s *secretPrivacyScope) restore(text string, render func(secretPrivacyEntry) string) string {
	if !strings.Contains(text, secretmask.PlaceholderPrefix) {
		return text
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return secretmask.PlaceholderPattern.ReplaceAllStringFunc(text, func(placeholder string) string {
		if entry, ok := s.entries[placeholder]; ok {
			return render(entry)
		}
		return placeholder
	})
}

func (s *secretPrivacyScope) restoreToolCalls(calls []llm.ToolCall) []llm.ToolCall {
	if len(calls) == 0 {
		return calls
	}
	out := make([]llm.ToolCall, len(calls))
	for index, call := range calls {
		restored := call
		restore := s.restoreForTool
		if isSpeechToolName(call.Name) {
			restore = s.restoreForReply
		}
		restored.Arguments = mapStrings(call.Arguments, restore)
		out[index] = restored
	}
	return out
}

// isSpeechToolName 认出参数是「要说的话」的工具：send，以及改名前的 agent_finalize 和 say。
func isSpeechToolName(name string) bool {
	switch strings.TrimSpace(name) {
	case agent.FinalizeToolName, "agent_finalize", "say":
		return true
	}
	return false
}

// maskSecretForReply 给出能认出「是哪一个」、又还原不了的样子。
func maskSecretForReply(entry secretPrivacyEntry) string {
	value := entry.value
	switch entry.kind {
	case "private_key":
		return "[私钥已隐藏]"
	case "phone", "id_card":
		digits := []rune(value)
		if len(digits) < 8 {
			return secretmask.Marker
		}
		return string(digits[:3]) + secretmask.Marker + string(digits[len(digits)-4:])
	case "bank_card":
		compact := strings.NewReplacer(" ", "", "-", "").Replace(value)
		return secretmask.Marker + " " + compact[len(compact)-4:]
	case "email":
		local, domain, _ := strings.Cut(value, "@")
		if local == "" {
			return secretmask.Marker + "@" + domain
		}
		return string([]rune(local)[:1]) + secretmask.Marker + "@" + domain
	}
	return secretmask.Mask(value)
}

func walkStrings(value any, visit func(string)) {
	switch typed := value.(type) {
	case string:
		visit(typed)
	case map[string]any:
		for _, item := range typed {
			walkStrings(item, visit)
		}
	case []any:
		for _, item := range typed {
			walkStrings(item, visit)
		}
	case []string:
		for _, item := range typed {
			visit(item)
		}
	}
}

// mapStrings 返回把每个字符串都换过一遍的副本。参数是任意 JSON 结构，字符串可能藏在
// 嵌套的对象或数组里。
func mapStrings[T any](value T, transform func(string) string) T {
	out, _ := mapStringsAny(value, transform).(T)
	return out
}

func mapStringsAny(value any, transform func(string) string) any {
	switch typed := value.(type) {
	case string:
		return transform(typed)
	case map[string]any:
		if typed == nil {
			return typed
		}
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = mapStringsAny(item, transform)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = mapStringsAny(item, transform)
		}
		return out
	case []string:
		out := make([]string, len(typed))
		for index, item := range typed {
			out[index] = transform(item)
		}
		return out
	}
	return value
}

type secretPrivacyLocalKey struct{}

// withSecretPrivacyLocal 记下本会话消息里出现过的敏感值。回复正文里的个人信息只有
// 这些会换回原文。
func withSecretPrivacyLocal(ctx context.Context, event MessageEvent, history []MessageEvent) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	local := map[string]bool{}
	for value := range secretPrivacyLocalFromContext(ctx) {
		local[value] = true
	}
	collect := func(text string) {
		for _, span := range findSecretSpans(text) {
			local[text[span.start:span.end]] = true
		}
	}
	collectEvent := func(item MessageEvent) {
		collect(item.RawMessage)
		for _, segment := range item.Segments {
			collect(segment.Data["text"])
		}
		if item.Quoted != nil {
			collect(item.Quoted.RawMessage)
			for _, segment := range item.Quoted.Segments {
				collect(segment.Data["text"])
			}
		}
	}
	collectEvent(event)
	for _, item := range history {
		collectEvent(item)
	}
	return context.WithValue(ctx, secretPrivacyLocalKey{}, local)
}

func secretPrivacyLocalFromContext(ctx context.Context) map[string]bool {
	if ctx == nil {
		return nil
	}
	local, _ := ctx.Value(secretPrivacyLocalKey{}).(map[string]bool)
	return local
}

// secretPrivacyDataPurposes 是输出不直接给人看、而是存起来再用的调用：记忆、摘要、
// 关系和群风格。它们的正文照原值换回，不然记忆里存下的是掩码，以后问起来也答不上。
// 下次进上下文时会重新换成占位符，原文照样到不了模型。其余调用的正文都可能被发出去，
// 按回复处理。
var secretPrivacyDataPurposes = map[string]bool{
	PurposeMemoryExtract:        true,
	PurposeMemorySummary:        true,
	PurposeContextSummary:       true,
	PurposeRelationshipEvaluate: true,
	PurposeGroupStyle:           true,
}

type secretPrivacyProvider struct {
	provider  LLMProvider
	salt      string
	overrides PromptOverrides
	local     map[string]bool
	known     *secretContextRegistry
}

func (r *Runtime) withLLMSecretPrivacyRun(ctx context.Context, run llmProviderRunFunc) llmProviderRunFunc {
	if run == nil {
		return run
	}
	cfg := r.configForContext(ctx)
	if !llmSecretMaskingEnabled(cfg) {
		return run
	}
	salt := r.identityAliasSalt(ctx)
	local := secretPrivacyLocalFromContext(ctx)
	return func(provider LLMProvider) (string, error) {
		return run(&secretPrivacyProvider{provider: provider, salt: salt, overrides: cfg.PromptOverrides, local: local, known: &r.secretContextValues})
	}
}

func (p *secretPrivacyProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	if p == nil || p.provider == nil {
		return nil, errors.New("diana: secret privacy provider is not configured")
	}
	// 每次调用一张对照表就够：模型只能引用这次请求里给过它的占位符，而历史每次都
	// 从原文重新换一遍，同一个值得到同一个占位符。
	scope := newSecretPrivacyScope(p.salt, p.local, p.known)
	scope.overrides = p.overrides
	response, err := p.provider.Generate(ctx, scope.protectRequest(req))
	if err != nil || response == nil {
		return response, err
	}
	restored := *response
	if secretPrivacyDataPurposes[llmUsagePurposeFromContext(ctx)] {
		restored.Text = scope.restoreForTool(response.Text)
	} else {
		restored.Text = scope.restoreForReply(response.Text)
	}
	restored.ToolCalls = scope.restoreToolCalls(response.ToolCalls)
	return &restored, nil
}

// maskSecretsForTrace 把要写进调试记录和运行日志的工具入参、输出里的敏感值换成
// 和发给模型时一样的占位符：记录里看到的就是模型看到的，原文不落进调试记录。
func (r *Runtime) maskSecretsForTrace(ctx context.Context, input map[string]any, output string) (map[string]any, string) {
	if r == nil || !llmSecretMaskingEnabled(r.configForContext(ctx)) {
		return input, output
	}
	scope := newSecretPrivacyScope(r.identityAliasSalt(ctx), nil, &r.secretContextValues)
	walkStrings(input, scope.discover)
	scope.discover(output)
	return mapStrings(input, scope.protectText), scope.protectText(output)
}
