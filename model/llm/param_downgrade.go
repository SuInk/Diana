// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// DowngradeMemoTTL 是一条降级结论的保质期。结论不能永久钉死：网关升级、模型
// 换代之后原本被拒的字段可能又能用了，过期即视为未知，由下一次真实请求重新学。
const DowngradeMemoTTL = 7 * 24 * time.Hour

// downgradeMemo 记住「某个端点的某个模型拒过哪些字段」。它必须活在进程级：
// 每回复一条消息都会新建一遍 provider 和 client，记在 client 上等于没记，
// 每条消息都要先吃一个 400 才降级。
//
// 键带上模型，因为限制的粒度不一样：严格 schema 通常是整个网关不支持，而
// 「不接受强制工具」是模型级的（DeepSeek 只有思考模型这样）。按模型记住的
// 代价是换模型要重新学一次，好过把结论错扣到同端点的其他模型头上。
type downgradeMemo struct {
	mu       sync.RWMutex
	rejected map[string]map[string]time.Time
	// now 供测试拨表，生产恒为 time.Now。
	now func() time.Time
}

var rememberedDowngrades = &downgradeMemo{}

func (m *downgradeMemo) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

func (m *downgradeMemo) seen(key, field string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	learnedAt, ok := m.rejected[key][field]
	return ok && m.clock().Sub(learnedAt) < DowngradeMemoTTL
}

func (m *downgradeMemo) remember(key string, fields map[string]bool) {
	if len(fields) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rejected == nil {
		m.rejected = map[string]map[string]time.Time{}
	}
	if m.rejected[key] == nil {
		m.rejected[key] = map[string]time.Time{}
	}
	for field := range fields {
		m.rejected[key][field] = m.clock()
	}
}

// DowngradeRecord 是一条可持久化的降级结论。
type DowngradeRecord struct {
	Key       string    `json:"key"`
	Field     string    `json:"field"`
	LearnedAt time.Time `json:"learnedAt"`
}

// DowngradeRecords 导出还没过期的结论，供上层落盘。
func DowngradeRecords() []DowngradeRecord {
	m := rememberedDowngrades
	m.mu.RLock()
	defer m.mu.RUnlock()
	now := m.clock()
	records := make([]DowngradeRecord, 0, len(m.rejected))
	for key, fields := range m.rejected {
		for field, learnedAt := range fields {
			if now.Sub(learnedAt) >= DowngradeMemoTTL {
				continue
			}
			records = append(records, DowngradeRecord{Key: key, Field: field, LearnedAt: learnedAt})
		}
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Key != records[j].Key {
			return records[i].Key < records[j].Key
		}
		return records[i].Field < records[j].Field
	})
	return records
}

// RestoreDowngradeRecords 装回上次落盘的结论，过期的直接丢掉。进程刚起来时调用
// 一次，免得重启后每个端点都要重新吃一次 400。
func RestoreDowngradeRecords(records []DowngradeRecord) {
	m := rememberedDowngrades
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock()
	for _, record := range records {
		if record.Key == "" || record.Field == "" || now.Sub(record.LearnedAt) >= DowngradeMemoTTL {
			continue
		}
		if m.rejected == nil {
			m.rejected = map[string]map[string]time.Time{}
		}
		if m.rejected[record.Key] == nil {
			m.rejected[record.Key] = map[string]time.Time{}
		}
		if existing, ok := m.rejected[record.Key][record.Field]; !ok || existing.Before(record.LearnedAt) {
			m.rejected[record.Key][record.Field] = record.LearnedAt
		}
	}
}

// 降级字段名，同时是落盘记录里的字段标识，改名会让旧记录失配（退化成重新学一次）。
const (
	downgradeFieldToolChoice  = "tool_choice"
	downgradeFieldStrictTools = "strict_tools"
)

// paramDowngrade 描述一次「上游拒了某个请求字段 → 去掉它重发」的降级。
// strip 返回 false 表示这次请求本来就没带这个字段，于是不会重发一个没变过的
// 请求。
type paramDowngrade struct {
	name     string
	rejected func(error) bool
	strip    func(GenerateRequest) (GenerateRequest, bool)
}

// paramDowngrades 按先精确后兜底排序：tool_choice 按字段名匹配，严格模式只能按
// 状态码判定，放在后面，否则它会把所有 400 都收走。
func paramDowngrades() []paramDowngrade {
	return []paramDowngrade{
		{name: downgradeFieldToolChoice, rejected: forcedToolChoiceRejected, strip: stripForcedToolChoice},
		{name: downgradeFieldStrictTools, rejected: strictToolsRejected, strip: stripStrictTools},
	}
}

// downgradeMemoKey 标识一条「端点 + 模型」。BaseURL 为空是官方 OpenAI 端点。
func downgradeMemoKey(cfg ProviderConfig, model string) string {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	return string(cfg.Provider) + "|" + baseURL + "|" + strings.TrimSpace(model)
}

// withRememberedDowngrades 按已经学到的结论先把字段摘掉。流式无法透明重试，
// 因此也靠这里沿用非流式学到的结论。
func (c *openAICompatibleClient) withRememberedDowngrades(req GenerateRequest) GenerateRequest {
	key := downgradeMemoKey(c.cfg, req.Model)
	for _, downgrade := range paramDowngrades() {
		if rememberedDowngrades.seen(key, downgrade.name) {
			req, _ = downgrade.strip(req)
		}
	}
	return req
}

// downgradeFor 找出能解释这次失败的降级并摘掉对应字段，摘掉的字段记进 applied。
// 每个字段最多摘一次，因此一个被拒 N 个字段的请求最多重试 N 次，不会绕圈。
func downgradeFor(req GenerateRequest, err error, applied map[string]bool) (GenerateRequest, bool) {
	for _, downgrade := range paramDowngrades() {
		if applied[downgrade.name] || !downgrade.rejected(err) {
			continue
		}
		stripped, changed := downgrade.strip(req)
		if !changed {
			continue
		}
		applied[downgrade.name] = true
		return stripped, true
	}
	return req, false
}

// rememberSuccessfulDowngrades 只在降级真的把请求救回来之后才记住结论。严格模式
// 那条只能按状态码判定，会把无关的 400（例如上下文超限）也认领走；等重试成功再
// 记，才不会凭一次误判永久停掉严格模式。
func (c *openAICompatibleClient) rememberSuccessfulDowngrades(req GenerateRequest, applied map[string]bool) {
	rememberedDowngrades.remember(downgradeMemoKey(c.cfg, req.Model), applied)
}

// strictToolsRejected 判断这次失败是否可能是网关拒绝严格模式。各家网关只会报成
// 普通的 schema 校验错误，文案无法可靠匹配，因此按状态码判定；降级只发生一次。
func strictToolsRejected(err error) bool {
	return paramRejectionStatus(err) != 0
}

func stripStrictTools(req GenerateRequest) (GenerateRequest, bool) {
	if !requestHasStrictTools(req) {
		return req, false
	}
	return withoutStrictTools(req), true
}

// forcedToolChoiceRejected 判断这次失败是不是网关拒绝「强制调用指定工具」。
// 这类错误的文案里一定带 tool_choice（DeepSeek 报 "Thinking mode does not support
// this tool_choice"），因此按字段名匹配，不把普通 400 也拖去降级。
func forcedToolChoiceRejected(err error) bool {
	status := paramRejectionStatus(err)
	if status == 0 {
		return false
	}
	var reqErr *openAIRequestError
	errors.As(err, &reqErr)
	return strings.Contains(strings.ToLower(reqErr.detail), "tool_choice")
}

// stripForcedToolChoice 退回 auto，工具本身照发，模型仍然可以调用。
func stripForcedToolChoice(req GenerateRequest) (GenerateRequest, bool) {
	if len(req.Tools) == 0 || strings.TrimSpace(req.ToolChoice) == "" {
		return req, false
	}
	req.ToolChoice = ""
	return req, true
}

// paramRejectionStatus 返回可能由请求字段引起的状态码，其余情况返回 0。
func paramRejectionStatus(err error) int {
	var reqErr *openAIRequestError
	if !errors.As(err, &reqErr) {
		return 0
	}
	if reqErr.statusCode != http.StatusBadRequest && reqErr.statusCode != http.StatusUnprocessableEntity {
		return 0
	}
	return reqErr.statusCode
}
