// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// 站点判断的扩展侧实现。控制面（model/browserctl/policy.go）已经拦过一道，
// 这里再拦一次有两个理由：用户在扩展里能看到边界是什么；控制面万一被绕过，
// 页面侧仍然只对白名单内的站点动手。两边的规则必须保持一致，改一边要改两边。

export const PROTOCOL_VERSION = 1;

/** 取出用于策略判断的主机名，只接受 http/https。 */
export function policyHost(rawURL) {
  if (!rawURL) return '';
  let parsed;
  try {
    parsed = new URL(rawURL);
  } catch {
    return '';
  }
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') return '';
  return parsed.hostname.toLowerCase().replace(/\.$/, '');
}

/** *.example.com 匹配子域但不含主域本身，与控制面同规则。 */
function hostMatches(host, pattern) {
  if (!host || !pattern) return false;
  if (pattern.startsWith('*.')) return host.endsWith(`.${pattern.slice(2)}`);
  return host === pattern;
}

/** 黑名单优先于白名单；白名单为空时一个站点都不放。 */
export function hostAllowed(policy, rawURL) {
  const host = policyHost(rawURL);
  if (!host) return false;
  const denied = policy?.denied_hosts ?? [];
  for (const pattern of denied) {
    if (hostMatches(host, pattern)) return false;
  }
  const allowed = policy?.allowed_hosts ?? [];
  for (const pattern of allowed) {
    if (hostMatches(host, pattern)) return true;
  }
  return false;
}

/** 写操作（导航、点击、输入）在只读授权下一律拒绝。 */
export function writeOp(op) {
  return op === 'page.open' || op === 'page.click' || op === 'page.type';
}

/**
 * 把站点白名单翻成 chrome.permissions 用的 origin 模式。
 * 只申请白名单里的站点，不申请 <all_urls>：扩展拿到的权限不该比授权范围更大。
 *
 * 每个站点要拆成 http 和 https 两条，不能用 scheme 通配：Chrome 要求申请的
 * 模式落在 manifest 的 optional_host_permissions 里，而那里写的是 http 和
 * https 两条全站模式；scheme 通配的模式不是其中任何一条的子集，申请时会直接
 * 抛 "Only permissions specified in the manifest may be requested."，
 * 用户点「授权这些站点」永远拿不到权限。
 */
export function originPatterns(policy) {
  const patterns = new Set();
  for (const pattern of policy?.allowed_hosts ?? []) {
    const host = pattern.startsWith('*.') ? `*.${pattern.slice(2)}` : pattern;
    patterns.add(`http://${host}/*`);
    patterns.add(`https://${host}/*`);
  }
  return [...patterns];
}
