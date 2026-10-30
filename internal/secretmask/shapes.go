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

	// IDCardPattern 认出 18 位居民身份证号的形态：地区码、出生日期、顺序码和校验位。
	// 只看形态会把同样长的订单号也认进来，用的时候再过一遍 ValidIDCard。
	//
	// 下面两条都不带边界：前后紧挨字母数字的是更长编号里的一段，由调用方核对。
	// 把边界字符写进正则会吃掉它，逗号隔开的第二个号就认不出来了。
	IDCardPattern = regexp.MustCompile(`[1-9][0-9]{5}(?:18|19|20)[0-9]{2}(?:0[1-9]|1[0-2])(?:0[1-9]|[12][0-9]|3[01])[0-9]{3}[0-9Xx]`)

	// BankCardPattern 认出 16 到 19 位的银行卡号，允许每四位一组用空格或横杠隔开。
	// 随便一串长数字也长这样，用的时候再过一遍 ValidLuhn。末尾不足四位的那截不许再
	// 隔开：「6222 … 0005 12 元」会把金额并进卡号，校验不过，整张卡就漏了。
	BankCardPattern = regexp.MustCompile(`[3-6][0-9]{3}(?:[ -]?[0-9]{4}){2,3}[0-9]{0,3}`)

	// ConnectionPasswordPattern 认出连接串里的密码：postgres://user:pass@host。
	// 第 1 组是密码。用户名不算秘密，留着便于认出连的是哪个账号。
	ConnectionPasswordPattern = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]{1,15}://[^\s:@/?#"'<>]*:([^\s@/?#"'<>]{3,})@`)
)

// ValidIDCard 按 GB 11643 的加权校验位核对身份证号，顺带确认出生日期在日历上存在。
func ValidIDCard(value string) bool {
	if len(value) != 18 {
		return false
	}
	weights := [17]int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	sum := 0
	for i := 0; i < 17; i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
		sum += int(value[i]-'0') * weights[i]
	}
	if !validDate(value[6:14]) {
		return false
	}
	check := "10X98765432"[sum%11]
	last := value[17]
	if last == 'x' {
		last = 'X'
	}
	return last == check
}

// validDate 核对 YYYYMMDD 是不是真实存在的日期（2 月 30 日这种不算）。
func validDate(value string) bool {
	year, month, day := atoi(value[0:4]), atoi(value[4:6]), atoi(value[6:8])
	if month < 1 || month > 12 || day < 1 {
		return false
	}
	days := [13]int{0, 31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
	if year%4 == 0 && (year%100 != 0 || year%400 == 0) {
		days[2] = 29
	}
	return day <= days[month]
}

func atoi(value string) int {
	n := 0
	for i := 0; i < len(value); i++ {
		n = n*10 + int(value[i]-'0')
	}
	return n
}

// ValidLuhn 用 Luhn 校验核对银行卡号，分隔用的空格和横杠先去掉。
func ValidLuhn(value string) bool {
	digits := make([]byte, 0, len(value))
	for i := 0; i < len(value); i++ {
		switch c := value[i]; {
		case c >= '0' && c <= '9':
			digits = append(digits, c)
		case c == ' ' || c == '-':
		default:
			return false
		}
	}
	if len(digits) < 16 || len(digits) > 19 {
		return false
	}
	sum := 0
	for i := len(digits) - 1; i >= 0; i-- {
		d := int(digits[i] - '0')
		if (len(digits)-1-i)%2 == 1 {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	return sum%10 == 0
}
