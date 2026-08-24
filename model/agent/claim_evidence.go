// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

type ClaimStatus string

const (
	ClaimStatusSupported    ClaimStatus = "supported"
	ClaimStatusConflicting  ClaimStatus = "conflicting"
	ClaimStatusInsufficient ClaimStatus = "insufficient"
	ClaimStatusNotSearched  ClaimStatus = "not_searched"
)

type ClaimDefinition struct {
	ID        string `json:"id"`
	Statement string `json:"statement"`
}

type ClaimEvidence struct {
	URL         string `json:"url"`
	Domain      string `json:"domain"`
	Relation    string `json:"relation"`
	SourceType  string `json:"source_type"`
	PublishedAt string `json:"published_at,omitempty"`
	Distance    string `json:"distance"`
	Strength    string `json:"strength"`
}

type ClaimUpdate struct {
	ID       string          `json:"id"`
	Status   ClaimStatus     `json:"status"`
	Summary  string          `json:"summary,omitempty"`
	Evidence []ClaimEvidence `json:"evidence,omitempty"`
}

type ClaimTrace struct {
	ID               string          `json:"id"`
	Statement        string          `json:"statement"`
	Status           ClaimStatus     `json:"status"`
	Summary          string          `json:"summary,omitempty"`
	Evidence         []ClaimEvidence `json:"evidence,omitempty"`
	CandidateSources []string        `json:"candidate_sources,omitempty"`
	Searches         int             `json:"searches"`
}

type claimEvidenceLedger struct {
	active            bool
	advisory          bool
	order             []string
	claims            map[string]*ClaimTrace
	covered           []string
	allowedSources    map[string]string
	sourceOrder       []string
	firstPartySources map[string]bool
	renderedSources   []string
	rejections        map[string][]string
	lastRejectedHash  string
	stopReason        string
}

func newClaimEvidenceLedger() *claimEvidenceLedger {
	return &claimEvidenceLedger{
		claims:            map[string]*ClaimTrace{},
		allowedSources:    map[string]string{},
		firstPartySources: map[string]bool{},
		rejections:        map[string][]string{},
	}
}

func (l *claimEvidenceLedger) prepareSearch(input map[string]any) map[string]any {
	if l == nil {
		return nil
	}
	definitions := decodeClaimDefinitions(input["claims"])
	if len(definitions) > 0 {
		l.active = true
	}
	for _, definition := range definitions {
		id := normalizeClaimID(definition.ID)
		statement := strings.TrimSpace(definition.Statement)
		if id == "" || statement == "" {
			continue
		}
		if existing := l.claims[id]; existing != nil {
			if existing.Statement == "" {
				existing.Statement = statement
			}
			continue
		}
		l.claims[id] = &ClaimTrace{ID: id, Statement: statement, Status: ClaimStatusNotSearched}
		l.order = append(l.order, id)
	}
	l.applyUpdates(decodeClaimUpdates(input["claim_updates"]))
	covered := normalizeClaimIDs(input["claim_ids"])
	if l.active {
		valid := covered[:0]
		for _, id := range covered {
			if l.claims[id] != nil {
				valid = append(valid, id)
			}
		}
		covered = valid
	}
	l.covered = append([]string(nil), covered...)
	return l.metadata()
}

func (l *claimEvidenceLedger) observeSearch(output string, runErr error) map[string]any {
	if l == nil || !l.active {
		return nil
	}
	status := "provider_error"
	stopReason := "tool_error"
	var sources []string
	if runErr == nil {
		var result webSearchResult
		if json.Unmarshal([]byte(output), &result) == nil {
			status = result.Status
			stopReason = result.StopReason
			sources = result.Sources
		}
	}
	for _, raw := range sources {
		canonical := canonicalEvidenceURL(raw)
		if canonical == "" {
			continue
		}
		if l.allowedSources[canonical] == "" {
			l.sourceOrder = append(l.sourceOrder, canonical)
		}
		l.allowedSources[canonical] = strings.TrimSpace(raw)
	}
	for _, id := range l.covered {
		claim := l.claims[id]
		if claim == nil {
			continue
		}
		claim.Searches++
		for _, raw := range sources {
			if canonicalEvidenceURL(raw) != "" {
				claim.CandidateSources = appendUniqueClaimString(claim.CandidateSources, strings.TrimSpace(raw))
			}
		}
		if claim.Status == ClaimStatusNotSearched {
			claim.Status = ClaimStatusInsufficient
		}
	}
	l.stopReason = stopReason
	metadata := l.metadata()
	metadata["search_status"] = status
	metadata["covered_claim_ids"] = append([]string(nil), l.covered...)
	metadata["candidate_source_count"] = len(sources)
	metadata["stop_reason"] = stopReason
	return metadata
}

// observeRenderedPage 把 browser_render 成功读取的页面登记为可引用来源。
// 沙盒浏览器直接读到的页面属于第一方直接证据，比搜索摘要更强，
// 因此不能因为它没有出现在搜索候选里就被证据校验拒绝。
func (l *claimEvidenceLedger) observeRenderedPage(output string, runErr error) map[string]any {
	if l == nil || !l.active || runErr != nil {
		return nil
	}
	var page RenderedPage
	if json.Unmarshal([]byte(output), &page) != nil {
		return nil
	}
	if strings.TrimSpace(page.Text) == "" && strings.TrimSpace(page.Title) == "" {
		return nil
	}
	added := 0
	for _, raw := range []string{page.URL, page.RequestedURL} {
		canonical := canonicalEvidenceURL(raw)
		if canonical == "" {
			continue
		}
		if l.allowedSources[canonical] == "" {
			added++
		}
		l.allowedSources[canonical] = strings.TrimSpace(raw)
		l.firstPartySources[canonical] = true
		l.renderedSources = appendUniqueClaimString(l.renderedSources, strings.TrimSpace(raw))
	}
	if added == 0 {
		return nil
	}
	metadata := l.metadata()
	metadata["rendered_source_count"] = len(l.renderedSources)
	return metadata
}

func (l *claimEvidenceLedger) applyUpdates(updates []ClaimUpdate) {
	if l == nil || !l.active {
		return
	}
	for _, update := range updates {
		id := normalizeClaimID(update.ID)
		claim := l.claims[id]
		if claim == nil || !validClaimStatus(update.Status) {
			continue
		}
		validEvidence := make([]ClaimEvidence, 0, len(update.Evidence))
		delete(l.rejections, id)
		for _, evidence := range update.Evidence {
			canonical := canonicalEvidenceURL(evidence.URL)
			if canonical == "" {
				l.reject(id, "证据 URL "+strings.TrimSpace(evidence.URL)+" 不是合法的 http(s) 地址")
				continue
			}
			if l.allowedSources[canonical] == "" {
				l.reject(id, "证据 URL "+strings.TrimSpace(evidence.URL)+" 不在本轮已检索或已渲染的来源里")
				continue
			}
			firstParty := l.firstPartySources[canonical]
			evidence.URL = l.allowedSources[canonical]
			if parsed, err := url.Parse(evidence.URL); err == nil {
				evidence.Domain = strings.ToLower(parsed.Hostname())
			}
			evidence.Relation = normalizeEnum(evidence.Relation, "supports", "refutes")
			if evidence.Relation == "" {
				if update.Status != ClaimStatusSupported {
					l.reject(id, "证据 "+evidence.URL+" 缺少 relation，只能填 supports 或 refutes")
					continue
				}
				evidence.Relation = "supports"
			}
			evidence.SourceType = normalizeEnum(evidence.SourceType, "first_party", "official_record", "primary_reporting", "secondary", "unknown")
			if evidence.SourceType == "" {
				if firstParty {
					evidence.SourceType = "first_party"
				} else {
					evidence.SourceType = "unknown"
				}
			}
			evidence.Distance = normalizeEnum(evidence.Distance, "direct", "near", "secondary")
			if evidence.Distance == "" {
				if firstParty {
					evidence.Distance = "direct"
				} else {
					evidence.Distance = "secondary"
				}
			}
			evidence.Strength = normalizeEnum(evidence.Strength, "high", "medium", "low")
			if evidence.Strength == "" {
				evidence.Strength = "low"
			}
			validEvidence = append(validEvidence, evidence)
		}
		status := update.Status
		if (status == ClaimStatusSupported || status == ClaimStatusConflicting) && len(validEvidence) == 0 {
			status = ClaimStatusInsufficient
			if len(update.Evidence) == 0 {
				l.reject(id, "申报 "+string(update.Status)+" 但没有给出任何证据")
			}
		}
		claim.Status = status
		claim.Summary = strings.TrimSpace(update.Summary)
		claim.Evidence = validEvidence
	}
}

// validateFinal 结算提交上来的 claims，并一次报出全部不合格字段。逐个报会让每
// 修一个问题就多花一轮重试，而每轮重试都要重发整个上下文。
func (l *claimEvidenceLedger) reject(id, reason string) {
	if l == nil || strings.TrimSpace(reason) == "" {
		return
	}
	if l.rejections == nil {
		l.rejections = map[string][]string{}
	}
	l.rejections[id] = appendUniqueClaimString(l.rejections[id], reason)
}

// availableSources 列出模型现在真正可以引用的 URL，避免它只被告知“绑定失败”而无从修正。
func (l *claimEvidenceLedger) availableSources(limit int) []string {
	if l == nil {
		return nil
	}
	if limit <= 0 {
		limit = 6
	}
	sources := make([]string, 0, limit)
	for _, raw := range l.renderedSources {
		if len(sources) >= limit {
			return sources
		}
		sources = appendUniqueClaimString(sources, raw)
	}
	for _, id := range l.order {
		claim := l.claims[id]
		if claim == nil {
			continue
		}
		for _, raw := range claim.CandidateSources {
			if len(sources) >= limit {
				return sources
			}
			sources = appendUniqueClaimString(sources, raw)
		}
	}
	return sources
}

func (l *claimEvidenceLedger) validateFinal(updates []ClaimUpdate) (string, bool) {
	if l == nil || !l.active {
		return "", true
	}
	// 关掉强制校验后仍然结算并留痕，只是不再拦截 final。
	if l.advisory {
		l.applyUpdates(updates)
		return "", true
	}
	if len(updates) == 0 {
		return "最终动作缺少 claims 证据结算", false
	}
	l.applyUpdates(updates)
	seen := map[string]bool{}
	requestedStatus := map[string]ClaimStatus{}
	for _, update := range updates {
		id := normalizeClaimID(update.ID)
		seen[id] = true
		requestedStatus[id] = update.Status
	}
	var issues []string
	for _, id := range l.order {
		claim := l.claims[id]
		switch {
		case claim == nil || !seen[id]:
			issues = append(issues, id+": 未结算")
		case !validClaimStatus(claim.Status):
			issues = append(issues, id+": status 无效")
		case requestedStatus[id] != claim.Status:
			issues = append(issues, l.bindingFailure(id, requestedStatus[id])+"，已降级为 "+string(claim.Status))
		case (claim.Status == ClaimStatusSupported || claim.Status == ClaimStatusConflicting) && len(claim.Evidence) == 0:
			issues = append(issues, l.bindingFailure(id, claim.Status))
		}
	}
	if len(issues) == 0 {
		return "", true
	}
	return "claims 结算不合格：" + strings.Join(issues, "；"), false
}

// isActive 报告本轮是否启用了逐主张证据账本。
func (l *claimEvidenceLedger) isActive() bool {
	return l != nil && l.active
}

// bindingFailure 说清楚是哪条证据、因为什么被拒，并给出现在可以引用的来源，
// 让模型能补齐绑定，而不是只能把结论降级、连带推翻已经写对的正文。
func (l *claimEvidenceLedger) bindingFailure(id string, requested ClaimStatus) string {
	message := "claim " + id + " 申报 " + string(requested) + " 但证据没有通过校验"
	if reasons := l.rejections[id]; len(reasons) > 0 {
		message += "：" + strings.Join(reasons, "；")
	}
	if sources := l.availableSources(6); len(sources) > 0 {
		message += "。现在可以引用的来源：" + strings.Join(sources, " ")
	}
	return message
}

func (l *claimEvidenceLedger) recordRejectedSearch(input map[string]any, reason string) {
	if l == nil {
		return
	}
	candidates, err := webSearchCandidates(input, 1)
	if err == nil && len(candidates) > 0 {
		l.lastRejectedHash = candidates[0].Hash
	}
	l.stopReason = reason
}

// digest 把账本压成紧凑的状态行。允许的来源和全部枚举现在都写进了工具 schema，
// 提示词里只留模型仍然需要自己决定的部分，不再每次搜索后重发整本账本 JSON。
func (l *claimEvidenceLedger) digest() string {
	if l == nil || !l.active {
		return ""
	}
	lines := make([]string, 0, len(l.order)+3)
	lines = append(lines, "【逐主张证据账本，仅供内部校验】")
	for _, claim := range l.traces() {
		line := "- " + claim.ID + " [" + string(claim.Status) + "]"
		if count := len(claim.Evidence); count > 0 {
			line += " 已绑定证据 " + strconv.Itoa(count)
		}
		if statement := strings.TrimSpace(claim.Statement); statement != "" {
			line += " " + statement
		}
		lines = append(lines, line)
	}
	if l.stopReason != "" {
		lines = append(lines, "stop_reason: "+l.stopReason)
	}
	if len(l.renderedSources) > 0 {
		lines = append(lines, "rendered_sources: "+strings.Join(l.renderedSources, " "))
	}
	if l.lastRejectedHash != "" {
		lines = append(lines, "last_rejected_query_hash: "+l.lastRejectedHash)
	}
	lines = append(lines, "候选来源不等于事实已获支持。优先检索 insufficient/not_searched 的 claim；claim ID、证据账本和内部校验过程不得出现在最终回复正文里。")
	return strings.Join(lines, "\n")
}

// allowedSourceURLs 按发现顺序返回已检索到的来源。它们会被填进工具 schema 的
// enum，于是编造出来的来源在解码层就不可能出现，而不是事后拦截再重试。
func (l *claimEvidenceLedger) allowedSourceURLs() []string {
	if l == nil {
		return nil
	}
	out := make([]string, 0, len(l.sourceOrder))
	for _, canonical := range l.sourceOrder {
		if raw := l.allowedSources[canonical]; raw != "" {
			out = append(out, raw)
		}
	}
	return out
}

// declaredClaimIDs 按声明顺序返回模型已经声明过的 claim id。
func (l *claimEvidenceLedger) declaredClaimIDs() []string {
	if l == nil {
		return nil
	}
	return append([]string(nil), l.order...)
}

// claimEvidenceSchema 描述一条证据。allowedSources 非空时，URL 收窄成检索工具
// 真实返回过的来源枚举。
func claimEvidenceSchema(allowedSources []string) map[string]any {
	return toolObjectSchema([]string{"url", "relation", "source_type", "distance", "strength"}, map[string]any{
		"url":          toolEnumParam("证据 URL，必须原样取自工具返回的候选来源", allowedSources...),
		"relation":     toolEnumParam("该来源支持还是反驳这条 claim", "supports", "refutes"),
		"source_type":  toolEnumParam("来源类型", "first_party", "official_record", "primary_reporting", "secondary", "unknown"),
		"published_at": toolStringParam("来源发布日期，可选"),
		"distance":     toolEnumParam("来源与结论的距离", "direct", "near", "secondary"),
		"strength":     toolEnumParam("证据强度", "high", "medium", "low"),
	})
}

// claimDefinitionSchema 描述一条新声明的 claim。
func claimDefinitionSchema() map[string]any {
	return toolObjectSchema([]string{"id", "statement"}, map[string]any{
		"id":        toolStringParam("claim 标识，小写字母、数字、下划线或连字符"),
		"statement": toolStringParam("待验证的通用主张，不得按品牌或站点硬编码"),
	})
}

// claimUpdateSchema 描述一次 claim 结算。已声明的 claim id 和已检索到的来源在
// 已知时都会被填成枚举。
func claimUpdateSchema(claimIDs, allowedSources []string) map[string]any {
	return toolObjectSchema([]string{"id", "status"}, map[string]any{
		"id":       toolEnumParam("已声明的 claim id", claimIDs...),
		"status":   toolEnumParam("结算状态；没有检索到证据只能用 insufficient", string(ClaimStatusSupported), string(ClaimStatusConflicting), string(ClaimStatusInsufficient), string(ClaimStatusNotSearched)),
		"summary":  toolStringParam("该 claim 的结论摘要"),
		"evidence": toolArrayParam("supported/conflicting 必须给出已检索来源证据", claimEvidenceSchema(allowedSources)),
	})
}

func (l *claimEvidenceLedger) metadata() map[string]any {
	if l == nil || !l.active {
		return nil
	}
	statuses := map[string]int{}
	sourceTypes := map[string]int{}
	strengths := map[string]int{}
	for _, trace := range l.traces() {
		statuses[string(trace.Status)]++
		for _, evidence := range trace.Evidence {
			sourceTypes[evidence.SourceType]++
			strengths[evidence.Strength]++
		}
	}
	return map[string]any{"claim_count": len(l.order), "claim_statuses": statuses, "source_types": sourceTypes, "evidence_strengths": strengths}
}

func (l *claimEvidenceLedger) groundedFallback() string {
	if l == nil || !l.active {
		return "现有工具结果不足以生成可靠答复。"
	}
	var confirmed, unresolved, conflicts []string
	for _, claim := range l.traces() {
		line := strings.TrimSpace(claim.Statement)
		if summary := strings.TrimSpace(claim.Summary); summary != "" {
			line = summary
		}
		for _, evidence := range claim.Evidence {
			line += "\n来源：" + evidence.URL
		}
		switch claim.Status {
		case ClaimStatusSupported:
			confirmed = append(confirmed, line)
		case ClaimStatusConflicting:
			conflicts = append(conflicts, line)
		default:
			unresolved = append(unresolved, strings.TrimSpace(claim.Statement))
		}
	}
	var sections []string
	if len(confirmed) > 0 {
		sections = append(sections, "已确认：\n- "+strings.Join(confirmed, "\n- "))
	}
	if len(conflicts) > 0 {
		sections = append(sections, "存在冲突：\n- "+strings.Join(conflicts, "\n- "))
	}
	if len(unresolved) > 0 {
		sections = append(sections, "尚未确认：\n- "+strings.Join(unresolved, "\n- "))
	}
	return strings.Join(sections, "\n\n")
}

func (l *claimEvidenceLedger) traces() []ClaimTrace {
	if l == nil {
		return nil
	}
	traces := make([]ClaimTrace, 0, len(l.order))
	for _, id := range l.order {
		if claim := l.claims[id]; claim != nil {
			copy := *claim
			copy.Evidence = append([]ClaimEvidence(nil), claim.Evidence...)
			copy.CandidateSources = append([]string(nil), claim.CandidateSources...)
			traces = append(traces, copy)
		}
	}
	return traces
}

func decodeClaimDefinitions(value any) []ClaimDefinition {
	var out []ClaimDefinition
	raw, err := json.Marshal(value)
	if err == nil {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

func decodeClaimUpdates(value any) []ClaimUpdate {
	var out []ClaimUpdate
	raw, err := json.Marshal(value)
	if err == nil {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

func normalizeClaimIDs(value any) []string {
	var raw []string
	encoded, err := json.Marshal(value)
	if err == nil {
		_ = json.Unmarshal(encoded, &raw)
	}
	out := make([]string, 0, len(raw))
	for _, value := range raw {
		if id := normalizeClaimID(value); id != "" {
			out = appendUniqueClaimString(out, id)
		}
	}
	return out
}

func normalizeClaimID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) == 0 || len(value) > 48 {
		return ""
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '_' && char != '-' {
			return ""
		}
	}
	return value
}

func validClaimStatus(status ClaimStatus) bool {
	switch status {
	case ClaimStatusSupported, ClaimStatusConflicting, ClaimStatusInsufficient, ClaimStatusNotSearched:
		return true
	default:
		return false
	}
}

func canonicalEvidenceURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return ""
	}
	parsed.Fragment = ""
	canonical := strings.ToLower(parsed.Scheme + "://" + parsed.Host + parsed.EscapedPath())
	if query := parsed.Query().Encode(); query != "" {
		canonical += "?" + query
	}
	return canonical
}

func normalizeEnum(value string, allowed ...string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, item := range allowed {
		if value == item {
			return value
		}
	}
	return ""
}

func appendUniqueClaimString(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
