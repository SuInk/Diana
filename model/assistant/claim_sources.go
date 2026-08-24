// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
)

const (
	recentClaimSourceLimit = 8
	recentClaimSourceTTL   = 30 * time.Minute
	claimSourceContextHint = "【上一轮结论引用的来源，仅在有人索取链接时使用】\n" +
		"这些 URL 是机器人自己在前面几轮里实际检索或渲染过的页面。有人问「链接呢」「来源是什么」" +
		"「发一下原文」时，原样给出对应的 URL，不要改写、不要凭印象另造一个地址；没有对应条目就直说没有存下来。" +
		"没人索取时不要主动罗列这些链接。\n"
)

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

type claimSourceRecord struct {
	Statement string
	URL       string
	SavedAt   time.Time
}

// rememberClaimSources 把本轮结论真正绑定的证据来源留到会话里。
func (r *Runtime) rememberClaimSources(event MessageEvent, claims []agent.ClaimTrace) {
	if r == nil || len(claims) == 0 || !r.claimSourceRecallEnabled(event) {
		return
	}
	fresh := make([]claimSourceRecord, 0, recentClaimSourceLimit)
	now := time.Now()
	for _, claim := range claims {
		statement := strings.TrimSpace(claim.Summary)
		if statement == "" {
			statement = strings.TrimSpace(claim.Statement)
		}
		for _, evidence := range claim.Evidence {
			url := strings.TrimSpace(evidence.URL)
			if url == "" {
				continue
			}
			fresh = append(fresh, claimSourceRecord{Statement: statement, URL: url, SavedAt: now})
		}
	}
	if len(fresh) == 0 {
		return
	}
	session := sessionKey(event)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.recentClaimSources == nil {
		r.recentClaimSources = map[string][]claimSourceRecord{}
	}
	merged := append(fresh, r.recentClaimSources[session]...)
	r.recentClaimSources[session] = dedupeClaimSources(merged, now)
}

// claimSourceContext 生成注入下一轮的来源清单；没有可用来源时返回空串。
func (r *Runtime) claimSourceContext(event MessageEvent) string {
	if r == nil {
		return ""
	}
	session := sessionKey(event)
	// 中途关掉开关时把已经存下的来源一并清掉，不要让它继续留在内存里。
	if !r.claimSourceRecallEnabled(event) {
		r.mu.Lock()
		delete(r.recentClaimSources, session)
		r.mu.Unlock()
		return ""
	}
	// 从不给链接时没必要把 URL 送进上下文；记录仍然留着，改回其它档立刻可用。
	if r.replyLinkPolicy(event) == replyLinkPolicyNever {
		return ""
	}
	now := time.Now()
	r.mu.Lock()
	records := dedupeClaimSources(r.recentClaimSources[session], now)
	if len(records) == 0 {
		delete(r.recentClaimSources, session)
	} else if r.recentClaimSources != nil {
		r.recentClaimSources[session] = records
	}
	r.mu.Unlock()
	if len(records) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString(claimSourceContextHint)
	for _, record := range records {
		builder.WriteString("- ")
		if record.Statement != "" {
			builder.WriteString(record.Statement)
			builder.WriteString("：")
		}
		builder.WriteString(record.URL)
		builder.WriteString("\n")
	}
	return strings.TrimRight(builder.String(), "\n")
}

// dedupeClaimSources 按 URL 去重、丢掉过期条目，并保留最近的若干条。
func dedupeClaimSources(records []claimSourceRecord, now time.Time) []claimSourceRecord {
	seen := make(map[string]bool, len(records))
	out := make([]claimSourceRecord, 0, len(records))
	for _, record := range records {
		url := strings.TrimSpace(record.URL)
		if url == "" || seen[url] {
			continue
		}
		if !record.SavedAt.IsZero() && now.Sub(record.SavedAt) > recentClaimSourceTTL {
			continue
		}
		seen[url] = true
		out = append(out, record)
		if len(out) >= recentClaimSourceLimit {
			break
		}
	}
	return out
}
