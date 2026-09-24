// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Package secretmask 把凭据原文从要交给模型或聊天对象的文本里换成掩码。
//
// 凭据进入模型上下文的路子不止「有人把配置读出来」：Go 的 net/http 报错会把整个
// 请求地址连同查询参数和 userinfo 带出来（Get "https://api/…?key=…": dial tcp …），
// 服务端报错会回显请求头，子进程会打印继承下来的环境变量。这些文本一旦拼进工具
// 结果，就经过模型提供商、可能被记日志，提示词被套出来时也跟着走。
//
// 这里分两层：
//
//   - URLs / Text 按形态认：地址里的 userinfo、名字像凭据的查询参数、Telegram 的
//     /bot<token>/ 路径段，以及 Authorization 这类请求头。不需要知道凭据是什么。
//   - Known 按原文认：运行时把已配置的凭据登记进来（Register），文本里出现原文就
//     换掉。形态认不出的（服务把令牌写进正文、Cookie 串在报错里）靠这一层。
//
// 掩码保留头尾几个字符，够人和模型认出「是不是那一个」，又远不够还原。
package secretmask

import (
	"errors"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Marker 是掩码中间那段。提交回来的值里带着它，就是有人把掩码原样交了回来。
const Marker = "****"

// MinKnownLength 以下的值不登记：太短的值在正文里撞车的概率太高，替换掉会把正常
// 输出改得面目全非，而这么短的值本来也不像凭据。
const MinKnownLength = 8

// Mask 把凭据换成掩码，形如 ghp_****abcd。带认证方案前缀的（Bearer xxx）只遮后
// 半截，前缀本身不是秘密，留着便于认出这是哪种凭据。
func Mask(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if scheme, credential, ok := CutAuthScheme(value); ok {
		return scheme + " " + Mask(credential)
	}
	runes := []rune(value)
	switch {
	case len(runes) < 12:
		return Marker
	case len(runes) < 20:
		return string(runes[:2]) + Marker + string(runes[len(runes)-2:])
	default:
		return string(runes[:4]) + Marker + string(runes[len(runes)-4:])
	}
}

// CutAuthScheme 认出「方案 凭据」这种两段式写法，例如 Bearer xxx、token xxx。
func CutAuthScheme(value string) (string, string, bool) {
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

// SensitiveName 判断一个名字（查询参数、环境变量、请求头）的值是不是凭据。
// 地址、区域、实例名这类普通配置不能算进来，否则工具结果里的链接会被改坏。
func SensitiveName(name string) bool {
	name = strings.TrimSpace(name)
	return sensitiveNamePattern.MatchString(name) && !plainNameSuffix.MatchString(name)
}

var sensitiveNamePattern = regexp.MustCompile(`(?i)(token|secret|passw|[_-]pwd$|credential|cookie|session([_-]?(id|key|token|data))?$|sessdata|signature|^sig$|^sign$|^auth$|auth[_-]?key|authorization|api[_-]?key|access[_-]?key|private[_-]?key|(^|[_-])key$|^ck$|[_-]ck$)`)

// plainNameSuffix 排除「名字里带凭据字样、值却是位置或开关」的写法：
// DIANA_SECRETS_FILE 是路径，TOKEN_URL 是地址，它们本身不是秘密。
var plainNameSuffix = regexp.MustCompile(`(?i)[_-](path|dir|file|sock|socket|type|mode|url|endpoint|host|port|name|enabled|env)$`)

// urlPattern 从正文里找地址。url.Error 的格式是 Get "https://…": …，引号、尖括号和
// 空白都算地址的边界。
var urlPattern = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]{1,15}://[^\s"'<>` + "`" + `]+`)

// telegramBotPathPattern 认出 Telegram Bot API 放在路径里的令牌：/bot123:AA…/。
var telegramBotPathPattern = regexp.MustCompile(`(/bot[0-9]{3,}:)([A-Za-z0-9_-]{20,})`)

// headerPattern 认出报错和回显里的请求头写法：Authorization: Bearer xxx、
// X-Api-Key=xxx。值取到行尾、引号或逗号前。
var headerPattern = regexp.MustCompile(`(?i)\b((?:proxy-)?authorization|x-api-key|api-key|x-goog-api-key|x-auth-token|private-token)(\s*[:=]\s*)([^\r\n"',;]+)`)

// cookieHeaderPattern 单独处理 Cookie：一条 Cookie 头里有好几对，分号不是边界。
var cookieHeaderPattern = regexp.MustCompile(`(?i)\b(cookie|set-cookie)(\s*:\s*)([^\r\n"']+)`)

// URLs 把正文里每个地址嵌着的凭据换成掩码：userinfo、名字像凭据的查询参数、
// Telegram 路径里的令牌。地址其余部分原样保留，模型仍然知道请求发去了哪里。
func URLs(text string) string {
	if !strings.Contains(text, "://") {
		return text
	}
	return urlPattern.ReplaceAllStringFunc(text, maskURL)
}

// Userinfo 只遮地址里的 userinfo，查询参数原样保留。工具的正常输出用它：网页里的
// 签名链接（?sign=、?signature=）是给模型接着用的，遮掉就打不开了；而地址里写着
// 账号密码的，不管出现在哪都只能是凭据。
func Userinfo(text string) string {
	if !strings.Contains(text, "://") || !strings.Contains(text, "@") {
		return text
	}
	return urlPattern.ReplaceAllStringFunc(text, func(raw string) string {
		schemeEnd := strings.Index(raw, "://")
		return raw[:schemeEnd+3] + maskUserinfo(raw[schemeEnd+3:])
	})
}

// maskUserinfo 遮掉「://」之后那段里的 userinfo。
func maskUserinfo(rest string) string {
	// userinfo 在第一个 / ? # 之前的最后一个 @ 前面。
	authorityEnd := strings.IndexAny(rest, "/?#")
	if authorityEnd < 0 {
		authorityEnd = len(rest)
	}
	at := strings.LastIndex(rest[:authorityEnd], "@")
	if at < 0 {
		return rest
	}
	// 只有用户名时它多半就是令牌（https://<token>@github.com），有密码时用户名
	// 也可能是令牌（<token>:x-oauth-basic），两段都遮。
	user, password, hasPassword := strings.Cut(rest[:at], ":")
	masked := maskOnce(user)
	if hasPassword {
		masked += ":" + maskOnce(password)
	}
	return masked + rest[at:]
}

func maskURL(raw string) string {
	schemeEnd := strings.Index(raw, "://")
	if schemeEnd < 0 {
		return raw
	}
	prefix := raw[:schemeEnd+3]
	rest := maskUserinfo(raw[schemeEnd+3:])
	rest = telegramBotPathPattern.ReplaceAllStringFunc(rest, func(match string) string {
		parts := telegramBotPathPattern.FindStringSubmatch(match)
		return parts[1] + Mask(parts[2])
	})
	query := strings.Index(rest, "?")
	if query < 0 {
		return prefix + rest
	}
	fragment := ""
	body := rest[query+1:]
	if hash := strings.Index(body, "#"); hash >= 0 {
		fragment = body[hash:]
		body = body[:hash]
	}
	pairs := strings.Split(body, "&")
	for i, pair := range pairs {
		name, value, ok := strings.Cut(pair, "=")
		if !ok || value == "" {
			continue
		}
		decoded, err := url.QueryUnescape(name)
		if err != nil {
			decoded = name
		}
		if SensitiveName(decoded) && !strings.Contains(value, Marker) {
			pairs[i] = name + "=" + Mask(value)
		}
	}
	return prefix + rest[:query+1] + strings.Join(pairs, "&") + fragment
}

// maskOnce 遮一段还没遮过的值：已经被 Known 换成掩码的不再遮第二遍。
func maskOnce(value string) string {
	if value == "" || strings.Contains(value, Marker) {
		return value
	}
	return Mask(value)
}

// Headers 把正文里的请求头写法（Authorization: Bearer xxx 等）换成掩码。
func Headers(text string) string {
	for _, pattern := range []*regexp.Regexp{headerPattern, cookieHeaderPattern} {
		text = pattern.ReplaceAllStringFunc(text, func(match string) string {
			parts := pattern.FindStringSubmatch(match)
			value := strings.TrimSpace(parts[3])
			if value == "" || strings.Contains(value, Marker) {
				return match
			}
			return parts[1] + parts[2] + Mask(value)
		})
	}
	return text
}

// Text 是给模型或聊天对象看的文本的统一出口：已登记的凭据原文、地址里的凭据、
// 请求头写法，一并换成掩码。先换原文：地址里的令牌被换成掩码之后，再按形态
// 处理时认出的是掩码，不会重复遮。
func Text(text string) string {
	if text == "" {
		return text
	}
	return Headers(URLs(Known(text)))
}

// Output 是工具正常输出的出口：已登记的凭据原文和地址里的 userinfo 换成掩码。
// 比 Text 保守——正文里的请求头写法、名字像凭据的查询参数可能是网页本身的内容，
// 模型要拿它们接着干活；报错才走 Text。
func Output(text string) string {
	if text == "" {
		return text
	}
	return Userinfo(Known(text))
}

// Error 换掉报错文本里的凭据，错误链仍然保留，errors.Is / errors.As 照常认得出
// 底层原因。
func Error(err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	redacted := Text(message)
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

// IsRedacted 判断这个错误是否已经过 Error 处理。
func IsRedacted(err error) bool {
	var target *redactedError
	return errors.As(err, &target)
}

// —— 已登记凭据 ——

var known = struct {
	sync.RWMutex
	values   map[string]bool
	replacer *strings.Replacer
}{values: map[string]bool{}}

// Register 登记一批凭据原文。运行时读到或保存配置时调用：LLM API Key、平台令牌、
// 插件的凭据设置、进程环境里的凭据变量。登记是只增不减的——换掉的旧凭据在旧日志、
// 旧报错里照样是原文，也照样要遮。
//
// 同一个值的常见变体一起登记：认证方案后面那段、查询参数转义后的写法。
func Register(values ...string) {
	var fresh []string
	add := func(value string) {
		value = strings.TrimSpace(value)
		if len([]rune(value)) < MinKnownLength || shortNumeric(value) {
			return
		}
		fresh = append(fresh, value)
	}
	for _, value := range values {
		add(value)
		if _, credential, ok := CutAuthScheme(value); ok {
			add(credential)
		}
		// Cookie 串：报错里常只带出其中一对，每一对的值也单独登记。
		if strings.Contains(value, ";") {
			for _, pair := range strings.Split(value, ";") {
				if _, cookie, ok := strings.Cut(pair, "="); ok {
					add(cookie)
				}
			}
		}
		add(url.QueryEscape(strings.TrimSpace(value)))
		add(url.PathEscape(strings.TrimSpace(value)))
	}
	if len(fresh) == 0 {
		return
	}
	known.Lock()
	defer known.Unlock()
	changed := false
	for _, value := range fresh {
		if !known.values[value] {
			known.values[value] = true
			changed = true
		}
	}
	if changed {
		known.replacer = nil
	}
}

// RegisterURL 登记一个配置地址里嵌着的凭据：userinfo、名字像凭据的查询参数、
// Telegram 路径里的令牌。地址本身不是秘密，不登记。
func RegisterURL(values ...string) {
	for _, raw := range values {
		raw = strings.TrimSpace(raw)
		if !strings.Contains(raw, "://") {
			continue
		}
		parsed, err := url.Parse(raw)
		if err != nil {
			continue
		}
		var secrets []string
		if parsed.User != nil {
			secrets = append(secrets, parsed.User.Username())
			if password, ok := parsed.User.Password(); ok {
				secrets = append(secrets, password)
			}
			// 报错里出现的是原始转义形式。
			rawUser, rawPassword, _ := strings.Cut(parsed.User.String(), ":")
			secrets = append(secrets, rawUser, rawPassword)
		}
		for name, items := range parsed.Query() {
			if SensitiveName(name) {
				secrets = append(secrets, items...)
			}
		}
		for _, pair := range strings.Split(parsed.RawQuery, "&") {
			if name, value, ok := strings.Cut(pair, "="); ok && SensitiveName(name) {
				secrets = append(secrets, value)
			}
		}
		for _, match := range telegramBotPathPattern.FindAllStringSubmatch(parsed.EscapedPath(), -1) {
			secrets = append(secrets, match[2])
		}
		Register(secrets...)
	}
}

// shortNumeric 认出纯数字的短值。QQ 号、群号、消息 ID 都是这个样子，主人要是拿一串
// 数字当访问令牌，登记它就会把工具结果里的群号一起遮掉，模型再也点不中那个群。
func shortNumeric(value string) bool {
	if len(value) >= 16 {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Known 把文本里出现的已登记凭据原文换成掩码。
func Known(text string) string {
	if text == "" {
		return text
	}
	replacer := knownReplacer()
	if replacer == nil {
		return text
	}
	return replacer.Replace(text)
}

func knownReplacer() *strings.Replacer {
	known.RLock()
	replacer := known.replacer
	empty := len(known.values) == 0
	known.RUnlock()
	if replacer != nil || empty {
		return replacer
	}
	known.Lock()
	defer known.Unlock()
	if known.replacer != nil {
		return known.replacer
	}
	values := make([]string, 0, len(known.values))
	for value := range known.values {
		values = append(values, value)
	}
	// 长的先换：Bearer xxx 整串和它的后半截都登记了，先换短的会留下前半截。
	sort.Slice(values, func(i, j int) bool {
		if len(values[i]) != len(values[j]) {
			return len(values[i]) > len(values[j])
		}
		return values[i] < values[j]
	})
	pairs := make([]string, 0, len(values)*2)
	for _, value := range values {
		pairs = append(pairs, value, Mask(value))
	}
	known.replacer = strings.NewReplacer(pairs...)
	return known.replacer
}

// RegisterEnvironment 登记进程环境里名字像凭据的变量值（TAVILY_API_KEY、
// GITHUB_TOKEN、DIANA_BILI_SESSDATA 之类）。environ 是 os.Environ() 的格式。
func RegisterEnvironment(environ []string) {
	var values []string
	for _, item := range environ {
		name, value, ok := strings.Cut(item, "=")
		if ok && SensitiveName(name) {
			values = append(values, value)
		}
	}
	Register(values...)
}

// SensitiveEnvironmentNames 列出环境里名字像凭据的变量名。
func SensitiveEnvironmentNames(environ []string) map[string]bool {
	names := map[string]bool{}
	for _, item := range environ {
		name, _, _ := strings.Cut(item, "=")
		if SensitiveName(name) {
			names[name] = true
		}
	}
	return names
}
