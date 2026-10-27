// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package secretmask

import "regexp"

// 下面是「看长相就认得出」的凭据和隐私形态，供各处按自己的场景脱敏：发 Issue、
// 报错发进群、查询词交给外部搜索引擎、管理员对话里的诊断证据。各处替换成什么标签
// 由调用方定（[REDACTED_TOKEN]、[secret omitted]……），认不认得出来只在这里定，
// 免得同一个令牌在一条链路上被遮、在另一条链路上原样漏出去。
//
// 这些规则宁可多遮：误遮一段普通文本的代价是一句话不好读，漏遮一个令牌的代价
// 是不可撤回的泄露。

// credentialKey 是值为凭据的键名，允许带前后缀（github_token、x-api-key-id）。
const credentialKey = `(?:[a-z0-9]+[_-])*(?:authorization|api[_-]?key|access[_-]?key|access[_-]?token|refresh[_-]?token|client[_-]?secret|private[_-]?key|token|secret|password|passwd|signature|credential|cookie)(?:[_-][a-z0-9]+)*`

var (
	// TokenPattern 认出带固定前缀的令牌：OpenAI 系 sk-、GitHub、AWS 访问密钥、
	// Slack、Google API Key、npm、JWT，以及 WebUI 的管理员令牌。
	TokenPattern = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{12,}|(?:gh[pousr]_|github_pat_)[A-Za-z0-9_]{12,}|(?:AKIA|ASIA)[A-Z0-9]{16}|xox[baprs]-[A-Za-z0-9-]{10,}|AIza[0-9A-Za-z_-]{30,}|npm_[A-Za-z0-9]{30,}|admin-[a-f0-9]{24,}|eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,})`)

	// BearerPattern 认出「认证方案 凭据」：Bearer 不分大小写；Basic 只认首字母大写
	// 的请求头写法，免得「basic authentication」这类普通英文被当成凭据。
	// 第 1 组是方案名，替换时可以留着。
	BearerPattern = regexp.MustCompile(`\b((?i:bearer)|Basic)\s+[A-Za-z0-9._~+/=-]{8,}`)

	// AuthorizationPattern 认出 Authorization 头连同可选的方案前缀，整段一起替换。
	AuthorizationPattern = regexp.MustCompile(`(?i)\b(?:proxy-)?authorization\s*[:=]\s*(?:(?:bearer|token|basic)\s+)?[^\s,;)\]}'"]+`)

	// QuotedCredentialPattern 认出 JSON 或引号写法的凭据字段："api_key": "xxx"。
	// 第 1 组是字段名连同冒号，第 2 组是字段名，第 3 组是带引号的值（认得转义的引号）。
	QuotedCredentialPattern = regexp.MustCompile(`(?i)(["'](` + credentialKey + `)["']\s*:\s*)("(?:[^"\\\r\n]|\\.)*"|'[^'\r\n]*')`)

	// CredentialPattern 认出「键 = 值」「键: 值」写法的凭据。第 1 组是键名，第 2 组
	// 是分隔符，第 3 组是值：带引号的取到引号为止，不带的取到空白或标点前。值不以
	// 方括号开头，已经换成 [REDACTED] 的不会被再认一遍。
	CredentialPattern = regexp.MustCompile(`(?i)\b(` + credentialKey + `)(\s*[:=]\s*)("[^"\r\n]*"|'[^'\r\n]*'|[^\s,;&"'<>()\[\]{}][^\s,;&"'<>)\]}]*)`)

	// PrivateKeyPattern 认出 PEM 格式的私钥块。
	PrivateKeyPattern = regexp.MustCompile(`(?s)-----BEGIN [^-\r\n]{0,40}PRIVATE KEY-----.*?-----END [^-\r\n]{0,40}PRIVATE KEY-----`)

	// EmailPattern 认出邮箱地址。
	EmailPattern = regexp.MustCompile(`(?i)\b[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}\b`)

	// PhonePattern 认出中国大陆手机号，允许 +86 前缀和数字间的空格或横杠。前后必须
	// 是边界，长串数字（消息 ID、QQ 号）中间恰好有一段 1[3-9] 开头的不算。
	PhonePattern = regexp.MustCompile(`(?:\+86[\s-]?|\b86[\s-]?|\b)1[3-9](?:[\s-]?[0-9]){9}\b`)
)
