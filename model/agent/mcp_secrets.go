// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// MCP 的请求头和环境变量按凭据对待：运行时在本地拼请求时用原文，凡是会进模型上下文
// 的地方（能力目录、工具描述、工具结果、报错）只给掩码。模型需要知道的是「配没配」，
// 不是令牌本身——令牌一旦进了上下文，就会经过模型提供商、可能被记日志，提示词被套出来
// 时也跟着走。
//
// 掩码保留头尾几个字符，够人和模型认出「是不是那一个」，又远不够还原。

// secretMaskMarker 是掩码中间那段。提交回来的值里带着它，就是有人把掩码原样交了回来。
const secretMaskMarker = "****"

// minRedactedSecretLength 以下的值不做文本替换：太短的值在正文里撞车的概率太高，
// 替换掉会把正常输出改得面目全非，而这么短的值本来也不像令牌。
const minRedactedSecretLength = 6

// maskSecret 把凭据换成掩码，形如 ghp_****abcd。带认证方案前缀的（Bearer xxx）只遮
// 后半截，前缀本身不是秘密，留着便于认出这是哪种凭据。
func maskSecret(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if scheme, credential, ok := cutAuthScheme(value); ok {
		return scheme + " " + maskSecret(credential)
	}
	runes := []rune(value)
	switch {
	case len(runes) < 12:
		return secretMaskMarker
	case len(runes) < 20:
		return string(runes[:2]) + secretMaskMarker + string(runes[len(runes)-2:])
	default:
		return string(runes[:4]) + secretMaskMarker + string(runes[len(runes)-4:])
	}
}

// cutAuthScheme 认出「方案 凭据」这种两段式写法，例如 Bearer xxx、token xxx。
func cutAuthScheme(value string) (string, string, bool) {
	scheme, credential, ok := strings.Cut(strings.TrimSpace(value), " ")
	credential = strings.TrimSpace(credential)
	if !ok || credential == "" || strings.ContainsAny(credential, " \t") || len(scheme) > 12 {
		return "", "", false
	}
	for _, r := range scheme {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return "", "", false
		}
	}
	return scheme, credential, true
}

// looksMasked 判断一个提交值是不是掩码。
func looksMasked(value string) bool {
	return strings.Contains(value, secretMaskMarker)
}

// secretEnvironmentKeyPattern 认出「值是凭据」的环境变量名。环境变量里还有实例地址、
// 区域这类普通配置，全都当凭据替换的话，工具结果里的链接会被改坏。
var secretEnvironmentKeyPattern = regexp.MustCompile(`(?i)(token|secret|passw|pwd|auth|credential|cookie|session|private|api_?key|access_?key|(^|_)key($|_))`)

// mcpSecretValues 收集这条服务配置里所有可能被原样回显的凭据原文，按长度从长到短
// 排好，替换时长的先换，避免「Bearer xxx」只被换掉后半截又留下前半截。
func mcpSecretValues(cfg mcpServerConfig) []string {
	seen := map[string]bool{}
	add := func(value string) {
		if value = strings.TrimSpace(value); len(value) >= minRedactedSecretLength {
			seen[value] = true
		}
	}
	addURL := func(raw string) {
		if !strings.Contains(raw, "://") {
			return
		}
		parsed, err := url.Parse(strings.TrimSpace(raw))
		if err != nil {
			return
		}
		// 解码前后两种写法都收：报错和配置里出现的是原始转义形式，服务回显的可能是解码后的。
		if parsed.User != nil {
			add(parsed.User.Username())
			if password, ok := parsed.User.Password(); ok {
				add(password)
			}
			rawUser, rawPassword, _ := strings.Cut(parsed.User.String(), ":")
			add(rawUser)
			add(rawPassword)
		}
		for _, values := range parsed.Query() {
			for _, value := range values {
				add(value)
			}
		}
		for _, pair := range strings.Split(parsed.RawQuery, "&") {
			if _, value, ok := strings.Cut(pair, "="); ok {
				add(value)
			}
		}
	}
	addCredential := func(value string) {
		for _, candidate := range []string{value, os.ExpandEnv(value)} {
			add(candidate)
			if _, credential, ok := cutAuthScheme(candidate); ok {
				add(credential)
			}
			addURL(candidate)
		}
	}
	for _, value := range cfg.Headers {
		addCredential(value)
	}
	for key, value := range cfg.Env {
		if secretEnvironmentKeyPattern.MatchString(key) {
			addCredential(value)
			continue
		}
		// 普通变量只挑出里面嵌着的凭据，例如 DATABASE_URL 里的密码。
		addURL(value)
		addURL(os.ExpandEnv(value))
	}
	addURL(cfg.URL)
	values := make([]string, 0, len(seen))
	for value := range seen {
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool {
		if len(values[i]) != len(values[j]) {
			return len(values[i]) > len(values[j])
		}
		return values[i] < values[j]
	})
	return values
}

// mcpRedactor 把一条服务的凭据原文从文本里换成掩码。MCP 服务回显令牌（报错里带上
// 请求头、调试输出打出环境变量）、连接失败时 URL 连同查询参数进了报错，都走这一道。
type mcpRedactor struct {
	replacer *strings.Replacer
}

func newMCPRedactor(cfg mcpServerConfig) *mcpRedactor {
	secrets := mcpSecretValues(cfg)
	if len(secrets) == 0 {
		return &mcpRedactor{}
	}
	pairs := make([]string, 0, len(secrets)*2)
	for _, secret := range secrets {
		pairs = append(pairs, secret, maskSecret(secret))
	}
	return &mcpRedactor{replacer: strings.NewReplacer(pairs...)}
}

func (r *mcpRedactor) text(value string) string {
	if r == nil || r.replacer == nil || value == "" {
		return value
	}
	return r.replacer.Replace(value)
}

// error 换掉报错文本里的凭据，错误链仍然保留，errors.Is 照常认得出底层原因。
func (r *mcpRedactor) error(err error) error {
	if err == nil || r == nil || r.replacer == nil {
		return err
	}
	message := err.Error()
	redacted := r.replacer.Replace(message)
	if redacted == message {
		return err
	}
	return &redactedError{message: redacted, cause: err}
}

type redactedError struct {
	message string
	cause   error
}

func (e *redactedError) Error() string { return e.message }
func (e *redactedError) Unwrap() error { return e.cause }

// ExtensionCredential 是能力目录里一条凭据的样子：只有键名、配没配和掩码，没有原文。
type ExtensionCredential struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Configured bool   `json:"configured"`
	Masked     string `json:"masked,omitempty"`
}

// mcpCredentialStates 列出这条服务配了哪些请求头和环境变量。模型问「令牌配了没有」
// 时看这个就够了，不需要、也拿不到原文。
func mcpCredentialStates(cfg mcpServerConfig) []ExtensionCredential {
	var out []ExtensionCredential
	for _, key := range sortedKeys(cfg.Headers) {
		value := cfg.Headers[key]
		out = append(out, ExtensionCredential{Kind: "header", Name: key, Configured: strings.TrimSpace(value) != "", Masked: maskSecret(value)})
	}
	for _, key := range sortedKeys(cfg.Env) {
		value := cfg.Env[key]
		out = append(out, ExtensionCredential{Kind: "env", Name: key, Configured: strings.TrimSpace(value) != "", Masked: maskSecret(value)})
	}
	return out
}

// maskedStringMap 把一组凭据换成同键的掩码。WebUI 读配置时用它：键和原来一样，值从
// 空串换成掩码，旧界面照样能用。
func maskedStringMap(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = maskSecret(value)
	}
	return out
}

// keepsStoredSecret 判断提交值是不是「沿用已保存的那个」：留空（allowBlank 时），或者
// 就是那个值的掩码原样交回来。
func keepsStoredSecret(submitted, stored string, allowBlank bool) bool {
	submitted = strings.TrimSpace(submitted)
	if submitted == "" {
		return allowBlank
	}
	return looksMasked(submitted) && submitted == maskSecret(stored)
}

// maskURLCredentials 把地址里嵌着的凭据（userinfo、查询参数）换成掩码，地址其余部分
// 原样保留。按查询参数认证的服务（?access_token=…）令牌就在地址里。
func maskURLCredentials(raw string) string {
	return newMCPRedactor(mcpServerConfig{URL: raw}).text(raw)
}

// restoreMaskedURLCredentials 是 maskURLCredentials 的反向：把提交值里的掩码换回已保存
// 地址里对应的原文。两个不同的原文算出同一个掩码时认不出是哪一个，不替换，留给
// rejectUnmatchedMasks 拒绝。只有全部掩码都换回去了才算成功。
func restoreMaskedURLCredentials(stored, submitted string) (string, bool) {
	if !looksMasked(submitted) {
		return submitted, false
	}
	// 原样交回最常见，先整串比：几个短值都遮成 **** 时逐个认不出，整串却对得上。
	if strings.TrimSpace(submitted) == maskURLCredentials(stored) {
		return stored, true
	}
	originals := map[string]string{}
	ambiguous := map[string]bool{}
	for _, secret := range mcpSecretValues(mcpServerConfig{URL: stored}) {
		mask := maskSecret(secret)
		if previous, ok := originals[mask]; ok && previous != secret {
			ambiguous[mask] = true
		}
		originals[mask] = secret
	}
	masks := make([]string, 0, len(originals))
	for mask := range originals {
		if !ambiguous[mask] {
			masks = append(masks, mask)
		}
	}
	sort.Slice(masks, func(i, j int) bool {
		if len(masks[i]) != len(masks[j]) {
			return len(masks[i]) > len(masks[j])
		}
		return masks[i] < masks[j]
	})
	pairs := make([]string, 0, len(masks)*2)
	for _, mask := range masks {
		pairs = append(pairs, mask, originals[mask])
	}
	if len(pairs) == 0 {
		return submitted, false
	}
	restored := strings.NewReplacer(pairs...).Replace(submitted)
	return restored, !looksMasked(restored)
}

// restoreStoredSecret 把提交值还原成已保存的原文：整串就是它的掩码（或允许时留空），
// 或者是一个地址、里面嵌着的凭据被遮成了掩码（预设表单回填的实例地址就是这样）。
func restoreStoredSecret(submitted, stored string, allowBlank bool) (string, bool) {
	if keepsStoredSecret(submitted, stored, allowBlank) {
		return stored, true
	}
	return restoreMaskedURLCredentials(stored, submitted)
}

// rejectUnmatchedMasks 挡住没能对上已保存值的掩码：把 ghp_****abcd 当令牌存下去，
// 这条服务就再也连不上了，而且看起来还像是配过。
func rejectUnmatchedMasks(server, previous mcpServerConfig) error {
	if looksMasked(server.URL) && server.URL != previous.URL {
		return errors.New("服务地址里带着掩码，不是令牌原文；要沿用已保存的值就原样交回它的掩码，要换就填新令牌")
	}
	for _, key := range sortedKeys(server.Headers) {
		if value := server.Headers[key]; looksMasked(value) && value != previous.Headers[key] {
			return fmt.Errorf("请求头 %s 提交的是掩码，不是令牌原文；要沿用已保存的值就原样交回它的掩码，要换就填新令牌", key)
		}
	}
	for _, key := range sortedKeys(server.Env) {
		if value := server.Env[key]; looksMasked(value) && value != previous.Env[key] {
			return fmt.Errorf("环境变量 %s 提交的是掩码，不是令牌原文；要沿用已保存的值就原样交回它的掩码，要换就填新令牌", key)
		}
	}
	return nil
}

// restoreMaskedMCPSecrets 是 Agent 改配置那条路：模型只见过掩码，想保留令牌时交回的
// 就是掩码，这里换回原文。
//
// 但令牌只能跟着原来的去处走。请求头只发给配置里那个地址的同源请求，环境变量只交给
// 那条命令——模型要是把地址换成自己的服务、把命令换成 `sh -c env`，再交回掩码让我们
// 把原文填上，令牌就被送出去了。所以去处变了就不填，让它去 WebUI 让主人重新填。
func restoreMaskedMCPSecrets(previous, server mcpServerConfig) (mcpServerConfig, error) {
	headersRestorable := sameMCPHTTPOrigin(previous.URL, server.URL)
	// 地址里的凭据和请求头同一条规矩：只换回到同源的地址上。
	if looksMasked(server.URL) {
		if !headersRestorable {
			return server, errors.New("服务地址里交回的是掩码，但地址换到了别的主机：令牌只跟着原来的地址走，换地址要请主人在 WebUI 里重新填令牌")
		}
		if restored, ok := restoreMaskedURLCredentials(previous.URL, server.URL); ok {
			server.URL = restored
		}
	}
	envRestorable := strings.TrimSpace(previous.Command) != "" &&
		previous.Command == server.Command &&
		slices.Equal(previous.Args, server.Args) &&
		previous.CWD == server.CWD
	for _, key := range sortedKeys(server.Headers) {
		stored, ok := previous.Headers[key]
		if !ok {
			continue
		}
		restored, matched := restoreStoredSecret(server.Headers[key], stored, false)
		if !matched {
			continue
		}
		if !headersRestorable {
			return server, fmt.Errorf("请求头 %s 交回的是掩码，但服务地址变了：令牌只跟着原来的地址走，换地址要请主人在 WebUI 里重新填令牌", key)
		}
		server.Headers[key] = restored
	}
	for _, key := range sortedKeys(server.Env) {
		stored, ok := previous.Env[key]
		if !ok {
			continue
		}
		restored, matched := restoreStoredSecret(server.Env[key], stored, false)
		if !matched {
			continue
		}
		if !envRestorable {
			return server, fmt.Errorf("环境变量 %s 交回的是掩码，但启动命令、参数或工作目录变了：令牌只交给原来那条命令，改命令要请主人在 WebUI 里重新填令牌", key)
		}
		server.Env[key] = restored
	}
	return server, nil
}

func sameMCPHTTPOrigin(left, right string) bool {
	if strings.TrimSpace(left) == "" || strings.TrimSpace(right) == "" {
		return false
	}
	leftURL, err := url.Parse(strings.TrimSpace(left))
	if err != nil {
		return false
	}
	rightURL, err := url.Parse(strings.TrimSpace(right))
	if err != nil {
		return false
	}
	return sameHTTPOrigin(leftURL, rightURL)
}

// mcpReferencedEnvironment 列出 MCP 配置里用 ${NAME} 引用的 Diana 进程环境变量。
// 主人常把令牌放在进程环境里、配置只写引用，这些变量对 run_command 要摘掉：命令
// 白名单里有 env、printenv 时，它们就是令牌原文。
func mcpReferencedEnvironment(servers map[string]mcpServerConfig) map[string]bool {
	names := map[string]bool{}
	collect := func(value string) {
		for _, match := range environmentReferencePattern.FindAllStringSubmatch(value, -1) {
			name := match[1]
			if name == "" {
				name = match[2]
			}
			names[name] = true
		}
	}
	for _, server := range servers {
		for _, value := range server.Headers {
			collect(value)
		}
		for _, value := range server.Env {
			collect(value)
		}
	}
	return names
}

// environmentWithout 从 KEY=VALUE 列表里去掉指定的变量。
func environmentWithout(environ []string, drop map[string]bool) []string {
	if len(drop) == 0 {
		return environ
	}
	out := make([]string, 0, len(environ))
	for _, item := range environ {
		key, _, _ := strings.Cut(item, "=")
		if !drop[key] {
			out = append(out, item)
		}
	}
	return out
}
