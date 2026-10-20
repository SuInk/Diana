// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

// replyLinkPolicyContext 只在操作者明确选了非默认档时才注入规则；默认档保持沉默，
// 让人设文本自己说了算，避免两处对链接的要求互相打架。
func (r *Runtime) replyLinkPolicyContext(event MessageEvent) string {
	switch r.replyLinkPolicy(event) {
	case replyLinkPolicyAlways:
		return "【链接策略】本轮结论如果绑定了可靠来源，在回复末尾附上最相关的那一条 URL 原文；" +
			"最多一条，不要罗列全部来源，也不要为没有来源的说法编造链接。"
	case replyLinkPolicyNever:
		return "【链接策略】任何情况下都不要在回复里给出 URL，包括有人明确索取时；" +
			"只用文字点名来源是什么，并说明本群不发链接。"
	default:
		return ""
	}
}
