// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Older backends do not return the read-only default. Keep the matching rules
// here so their editor still shows the effective value instead of an empty box.
export const FALLBACK_ACCOUNT_SAFETY_RULES = `只在回复「自己说出了」下面这些内容时才算不安全,别人说了什么不算:
- 中国大陆涉政:只拦截对中国大陆现实政治人物、中国共产党与党政机构、敏感时政
  争议或政治事件的评价、立场表达、动员,以及影射这些内容的谐音和梗。单纯提到
  中国、城市、历史常识、中国企业或技术团队不算。
- 境外政治与监管允许:中国大陆以外的政治人物、政府机构、选举、司法、监管和
  执法新闻不属于本审核的 politics。中性报道美国州总检察长、传票、消费者保护
  调查、欧盟监管或其他国家政策时必须放行,不得因为出现政府或政治词汇就拦截。
- 露骨性内容:具体的性行为描写、色情内容、性服务信息。日常玩笑、暧昧调侃、
  含糊的成人话题不算。
- 其他高风险:违法交易、毒品、武器制作、赌博引流、诈骗话术、自残教唆。
只在上述明确范围内判账号风险,不得自行扩大 politics 的地域和主题范围。命中明确
风险时从严处理;没有命中时放行。准确性检查仍按前述标准判断。`;

export function normalizeAccountSafetyRules(value: string | undefined): string {
  return (value ?? "").replace(/\r\n/g, "\n").trim();
}

export function effectiveAccountSafetyRules(override: string | undefined, fallback: string | undefined): string {
  return normalizeAccountSafetyRules(override) || normalizeAccountSafetyRules(fallback) || FALLBACK_ACCOUNT_SAFETY_RULES;
}

export function accountSafetyRulesOverride(draft: string, fallback: string): string {
  const normalized = normalizeAccountSafetyRules(draft);
  return normalized === normalizeAccountSafetyRules(fallback) ? "" : normalized;
}
