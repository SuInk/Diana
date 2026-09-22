// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"encoding/json"
	"net/url"
	"regexp"
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
	active bool
	// searched 记录本轮是否真的发起过检索。它和 active 不是一回事：active 要求
	// 模型同时声明了 claims，而模型完全可以只调 web_search 不带 claims——用
	// active 当门控判据会把这种模型反复打回，直到撞上修复上限才放行。
	searched bool
	// citable 是本轮正文里可以合法出现的链接：上下文里本来就有的、工具输出
	// 带回来的。它比 allowedSources 宽，因为复述用户贴的链接不算编造来源。
	citable map[string]bool
	// required 表示本轮必须拿证据才能收口，由调用方在进入循环前置位。
	// active 只有在模型自己调过 web_search 并声明 claims 之后才会为真，
	// 所以整套证据校验原本都够不着「压根没搜」这种情况——模型不搜，纯文本
	// 终稿直接放行。required 就是补这一段：该搜的轮次先立规矩，再让模型跑。
	required          bool
	order             []string
	claims            map[string]*ClaimTrace
	covered           []string
	allowedSources    map[string]string
	sourceOrder       []string
	firstPartySources map[string]bool
	renderedSources   []string
	lastRejectedHash  string
	stopReason        string
}

func newClaimEvidenceLedger() *claimEvidenceLedger {
	return &claimEvidenceLedger{
		claims:            map[string]*ClaimTrace{},
		allowedSources:    map[string]string{},
		firstPartySources: map[string]bool{},
	}
}

// missingRequiredSearch 表示这一轮要求证据、但模型还没产生任何检索。
// 只看有没有检索过，不要求声明 claims：门控管的是「不许不查就下结论」，
// 证据账本那套结构化校验是检索发生之后的事。
func (l *claimEvidenceLedger) missingRequiredSearch() bool {
	return l != nil && l.required && !l.searched
}

func (l *claimEvidenceLedger) prepareSearch(input map[string]any) map[string]any {
	if l == nil {
		return nil
	}
	l.searched = true
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
	for _, raw := range append([]string{page.URL, page.RequestedURL}, page.SourceURLs...) {
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
		for _, evidence := range update.Evidence {
			canonical := canonicalEvidenceURL(evidence.URL)
			if canonical == "" {
				continue
			}
			if l.allowedSources[canonical] == "" {
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
		}
		claim.Status = status
		claim.Summary = strings.TrimSpace(update.Summary)
		claim.Evidence = validEvidence
	}
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

// allowedSourceURLs 按发现顺序返回已检索到的来源，供需要收窄 schema 枚举的调用方使用。
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

// 本轮要求证据却一次都没检索时，退回去让模型先搜。措辞只讲「这一轮要先查」，
// 不替模型判断该查什么——查什么是它的事，我们只管住「不查不许收口」。
const (
	evidenceRequiredRepairReason = "这一轮需要外部事实支撑，但还没有进行任何检索"
	evidenceRequiredRepairPrompt = "这一轮的问题需要外部事实支撑，你还没有调用过 web_search。" +
		"聊天记录里的说法、你先前的回复和记忆摘要都只是线索，不能替代检索；" +
		"熟悉某个项目的原理也不代表知道它此刻的实现、插件、版本或生态现状。" +
		"请先调用 web_search 查证，再用 agent_finalize 收尾。" +
		"如果查完确实没有可用结果，就在正文里如实说明没查到，不要凭印象断言。"
)

// citationURLPattern 从任意文本里抓 http(s) 链接。字符集按 URL 允许的那些收窄，
// 而不是「非空白」——中文正文里「见 https://a.example/b。」这种写法很常见，
// 用非空白匹配会把后面整句中文一起吞进链接里。
var citationURLPattern = regexp.MustCompile(`https?://[A-Za-z0-9\-._~:/?#@!$&*+,;=%()\[\]]+`)

// 尾部标点交给 trim：链接结尾的句号、右括号通常属于句子而不属于链接。
const citationTrailingPunctuation = `.,;:!?)]}>*_`

// extractCitationURLs 返回文本里出现的链接，已规范化并去重。
func extractCitationURLs(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, raw := range citationURLPattern.FindAllString(text, -1) {
		raw = strings.TrimRight(raw, citationTrailingPunctuation)
		canonical := canonicalEvidenceURL(raw)
		if canonical == "" || seen[canonical] {
			continue
		}
		seen[canonical] = true
		out = append(out, raw)
	}
	return out
}

// noteCitableText 把文本里出现过的链接登记为「正文可以引用」。对话上下文里
// 用户自己贴的链接、工具输出里带回来的链接都走这里：模型复述它们不算编造。
func (l *claimEvidenceLedger) noteCitableText(text string) {
	if l == nil || strings.TrimSpace(text) == "" {
		return
	}
	for _, raw := range extractCitationURLs(text) {
		canonical := canonicalEvidenceURL(raw)
		if canonical == "" {
			continue
		}
		if l.citable == nil {
			l.citable = map[string]bool{}
		}
		l.citable[canonical] = true
	}
}

// unboundCitations 返回正文里那些既不是本轮检索到的、也没在上下文或工具输出
// 里出现过的链接。claims 侧的证据 URL 早就按 allowedSources 过滤了，但那只
// 清洗内部账本——用户看到的是正文，正文里的链接此前完全没人管。
func (l *claimEvidenceLedger) unboundCitations(content string) []string {
	if l == nil || !l.searched {
		// 本轮没检索就不是在做考证，正文里提一句网址不该被当成伪造来源。
		return nil
	}
	var unbound []string
	for _, raw := range extractCitationURLs(content) {
		canonical := canonicalEvidenceURL(raw)
		if canonical == "" || l.allowedSources[canonical] != "" || l.citable[canonical] {
			continue
		}
		unbound = append(unbound, raw)
	}
	return unbound
}
