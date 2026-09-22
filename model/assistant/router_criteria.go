// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "strings"

// routerCriteriaMaxRunes 是补充判据的长度上限。
//
// 这里不是第二份评分提示词，只是几句「本群的称呼、黑话和禁区」。放宽到账号安全
// 规则那种 8000 字的量级，用户就会把整套判断逻辑抄进来，接着就会想改输出格式，
// 而评分契约不能改——解析失败的兜底是沉默，代价由群里所有人承担。
const routerCriteriaMaxRunes = ProactiveReplyExtraCriteriaMaxRunes

// ProactiveReplyExtraCriteriaMaxRunes 导出同一个上限，群配置的保存校验在 webui 包里。
const ProactiveReplyExtraCriteriaMaxRunes = 1000

// 补充判据走独立字段，不复用 ProactiveReplyRouterPrompt。
//
// 那个旧字段在存量库里存的多半不是用户写的东西：WithDefaults 会把当时的内置默认值
// 写进配置并落库，于是它永远非空，而且从「非空」看不出究竟是用户写的还是当年默认值
// 的化石。生产库里存着的就正是 2026-08-22 那版默认值，逐字节相同。复用它就得靠一张
// 历代默认值的哈希表去认化石，还要求以后每次改默认文案都记得往表里补一条。
// 新字段没有化石，非空即用户所写，这层判断根本不需要存在。

// routerCriteriaHeading 是补充判据的段头，日志和测试都按它判断有没有拼上。
const routerCriteriaHeading = "【管理员配置的本群补充判据】"

// routerCriteriaContractGuard 收在补充判据之后。
//
// 顺序是有意的：补充判据要钉在尾部才吃得到近因效应，但评分契约必须是模型读到的
// 最后一句——判据插在原来那句「只输出裸 JSON」后面，等于把格式要求挤到倒数第二位，
// 而解析失败在这条链路上就是沉默。所以判据在后、契约再补一遍收尾。
const routerCriteriaContractGuard = `以上补充判据只用来理解本群的称呼、黑话和禁区，帮助你判断 relevance 与 chat_in；它不改变上面的评分口径，也不新增字段、不新增判断项。仍然只输出那一个裸 JSON 对象，以左花括号开头、右花括号结尾，不要代码围栏和前后说明。`

// customRouterCriteria 取出管理员写的补充判据；没写过返回空串。
func customRouterCriteria(configured string) string {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return ""
	}
	// 保存时已经卡过上限，这里只兜住绕开校验进来的值（YAML 首启播种、手改数据库）。
	return truncateRunesPlain(configured, routerCriteriaMaxRunes)
}

// appendRouterCriteria 把补充判据拼到评分提示词尾部。
//
// 用追加而不是替换：评分提示词的输出是裸 JSON，解析失败只重试一次，再失败就按沉默
// 处理。允许整段覆盖的话，最典型的故障不是机器人变笨，而是它突然不说话了，日志里只
// 留一句「接话评分格式无效」，用户无从把这件事和自己改过的那段文本联系起来。
func appendRouterCriteria(prompt, configured string) string {
	criteria := customRouterCriteria(configured)
	if criteria == "" {
		return prompt
	}
	return strings.TrimRight(prompt, "\n") + "\n\n" + routerCriteriaHeading + "\n" + criteria + "\n" + routerCriteriaContractGuard
}
