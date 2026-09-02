// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/SuInk/diana/model/llm"

	"github.com/google/uuid"
)

// 世界书：机器人的世界观设定库。人设正文回答「它是谁、怎么说话」，世界书回答
// 「它活在什么样的世界里」——城市叫什么、群里公认的虚构设定、它的身世背景，
// 这些东西塞进人设正文会把几百字的说话方式说明淹没在几千字的设定集里，而且
// 全部常驻等于每条消息都为整本设定集付 token。
//
// 所以设定拆成节点挂在一棵树上：父节点是章（「枝江」「机器人身世」），子节点
// 是节，路径本身就是语境。每个节点自己声明注入方式——常驻的每轮都带上（世界的
// 骨架，比如「故事发生在虚构城市枝江」），按关键词触发的只在聊到的时候进上下文
//（某条街道的细节、某段往事的展开）。
//
// 树是全局一棵，不挂在单个机器人配置上：世界观描述的是「这套部署共同生活的
// 世界」，和人设库一样是素材库。要不要用它由每台机器人的 world_book_enabled
// 决定。

// WorldBookMaxNodes 限制节点总数。这是给人整理的设定集，不是数据表。
const WorldBookMaxNodes = 200

const (
	worldBookTitleMaxRunes   = 60
	worldBookContentMaxRunes = 1200
	worldBookKeywordMaxRunes = 24
	worldBookMaxKeywords     = 16
	// worldBookMatchedNodeLimit 限制一轮最多注入几条触发式设定。命中十几条时
	// 该反省的是关键词配得太宽，不是把它们全塞进提示词。
	worldBookMatchedNodeLimit = 8
	// worldBookContextTokenBudget 是整段世界观上下文的 token 上限。常驻设定
	// 优先，触发式设定填剩下的空间。
	worldBookContextTokenBudget = 1200
)

// WorldBookNode 是一条世界观设定。
type WorldBookNode struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id,omitempty"`
	Title    string `json:"title"`
	Content  string `json:"content,omitempty"`
	// Keywords 是触发词：最近对话里出现任意一个就注入本条。AlwaysOn 的节点
	// 不看触发词，每轮都注入。两者都没有的节点只当目录用，自身不进提示词。
	Keywords []string `json:"keywords,omitempty"`
	// SecondaryKeywords 是副触发词（酒馆世界书的 keysecondary，AND ANY 逻辑）：
	// 填了之后，除了主触发词命中，还要求任意一个副词也在场才注入。用来收窄
	// 太宽的主词，比如主词「龙」配副词「枝江」，聊别处的龙就不触发。
	SecondaryKeywords []string `json:"secondary_keywords,omitempty"`
	AlwaysOn          bool     `json:"always_on,omitempty"`
	// Enabled 为 false 时整个子树都不注入：关掉一章就是关掉底下所有节。
	Enabled   *bool     `json:"enabled,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// WorldBook 是整棵世界观设定树。节点顺序就是同级之间的展示与注入顺序。
type WorldBook struct {
	Nodes []WorldBookNode `json:"nodes"`
}

// Normalized 清洗单个节点：补 ID、裁长度、去掉空触发词。
func (node WorldBookNode) Normalized() WorldBookNode {
	node.ID = strings.TrimSpace(node.ID)
	if node.ID == "" {
		node.ID = uuid.NewString()
	}
	node.ParentID = strings.TrimSpace(node.ParentID)
	node.Title = truncateRunesPlain(strings.TrimSpace(node.Title), worldBookTitleMaxRunes)
	node.Content = truncateRunesPlain(strings.TrimSpace(node.Content), worldBookContentMaxRunes)
	node.Keywords = normalizeWorldBookKeywords(node.Keywords)
	node.SecondaryKeywords = normalizeWorldBookKeywords(node.SecondaryKeywords)
	// 副词只在有主词时才有意义：没有主词的「AND」左边是空集，永远不触发，
	// 留着只会让条目看起来配了触发却永远沉默。
	if len(node.Keywords) == 0 {
		node.SecondaryKeywords = nil
	}
	return node
}

// normalizeWorldBookKeywords 清洗一组触发词：裁长度、去空白、按小写去重、封顶。
func normalizeWorldBookKeywords(raw []string) []string {
	keywords := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, keyword := range raw {
		keyword = truncateRunesPlain(strings.TrimSpace(keyword), worldBookKeywordMaxRunes)
		lower := strings.ToLower(keyword)
		if keyword == "" || seen[lower] {
			continue
		}
		seen[lower] = true
		keywords = append(keywords, keyword)
		if len(keywords) >= worldBookMaxKeywords {
			break
		}
	}
	if len(keywords) == 0 {
		return nil
	}
	return keywords
}

// enabled 报告节点自身是否启用；祖先是否启用由遍历负责。
func (node WorldBookNode) enabled() bool {
	return boolValue(node.Enabled, true)
}

// injectable 报告节点自身有没有可注入的内容。只有标题的节点是目录，不算。
func (node WorldBookNode) injectable() bool {
	return strings.TrimSpace(node.Content) != "" && (node.AlwaysOn || len(node.Keywords) > 0)
}

// WithDefaults 清洗整棵树：去掉没标题的节点和重复 ID，斩断悬空父指针和环。
//
// 不按更新时间排序：树的同级顺序是用户摆出来的章节顺序，注入和展示都按它来，
// 「刚改过的跳到最前面」在设定集里只会打乱叙事。
func (tree WorldBook) WithDefaults() WorldBook {
	seen := make(map[string]struct{}, len(tree.Nodes))
	nodes := make([]WorldBookNode, 0, len(tree.Nodes))
	for _, node := range tree.Nodes {
		node = node.Normalized()
		if node.Title == "" {
			continue
		}
		if _, ok := seen[node.ID]; ok {
			continue
		}
		seen[node.ID] = struct{}{}
		nodes = append(nodes, node)
	}
	if len(nodes) > WorldBookMaxNodes {
		nodes = nodes[:WorldBookMaxNodes]
	}
	byID := make(map[string]int, len(nodes))
	for index, node := range nodes {
		byID[node.ID] = index
	}
	for index, node := range nodes {
		if node.ParentID == "" {
			continue
		}
		if _, ok := byID[node.ParentID]; !ok || node.ParentID == node.ID {
			nodes[index].ParentID = ""
			continue
		}
		// 顺着父链走，撞回自己说明成了环；导入或手写的文件都可能带出这种数据，
		// 静默留着会让遍历死循环。断在当前节点，把它提回根。
		visited := map[string]bool{node.ID: true}
		parent := node.ParentID
		for parent != "" {
			if visited[parent] {
				nodes[index].ParentID = ""
				break
			}
			visited[parent] = true
			parentIndex, ok := byID[parent]
			if !ok {
				break
			}
			parent = nodes[parentIndex].ParentID
		}
	}
	return WorldBook{Nodes: nodes}
}

// Save 新增或更新一个节点，返回落库后的那一份。
func (tree WorldBook) Save(node WorldBookNode, now time.Time) (WorldBook, WorldBookNode, error) {
	node = node.Normalized()
	if node.Title == "" {
		return tree, WorldBookNode{}, errWorldBookTitleRequired
	}
	node.UpdatedAt = now
	for index := range tree.Nodes {
		if tree.Nodes[index].ID == node.ID {
			tree.Nodes[index] = node
			normalized := tree.WithDefaults()
			saved, _ := normalized.Find(node.ID)
			return normalized, saved, nil
		}
	}
	if len(tree.Nodes) >= WorldBookMaxNodes {
		return tree, WorldBookNode{}, errWorldBookFull
	}
	tree.Nodes = append(tree.Nodes, node)
	normalized := tree.WithDefaults()
	saved, _ := normalized.Find(node.ID)
	return normalized, saved, nil
}

// Delete 删掉一个节点，把它的子节点接到它的父节点上。级联删除整个子树太容易
// 一下清掉半本设定集；提级保留内容，用户真想删一章就逐个删。找不到不算错。
func (tree WorldBook) Delete(id string) WorldBook {
	id = strings.TrimSpace(id)
	if id == "" {
		return tree.WithDefaults()
	}
	parentID := ""
	if node, ok := tree.Find(id); ok {
		parentID = node.ParentID
	}
	nodes := make([]WorldBookNode, 0, len(tree.Nodes))
	for _, node := range tree.Nodes {
		if node.ID == id {
			continue
		}
		if node.ParentID == id {
			node.ParentID = parentID
		}
		nodes = append(nodes, node)
	}
	return WorldBook{Nodes: nodes}.WithDefaults()
}

// Find 按 ID 取一个节点。
func (tree WorldBook) Find(id string) (WorldBookNode, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return WorldBookNode{}, false
	}
	for _, node := range tree.Nodes {
		if node.ID == id {
			return node, true
		}
	}
	return WorldBookNode{}, false
}

// WorldBookRow 是深度优先展开后的一行，带层级和标题路径，给界面和提示词共用。
type WorldBookRow struct {
	Node  WorldBookNode
	Depth int
	// Path 是从根到本节点的标题链，不含本节点自己。
	Path []string
}

// OrderedRows 按深度优先展开整棵树，同级保持节点的原始顺序。
func (tree WorldBook) OrderedRows() []WorldBookRow {
	children := map[string][]WorldBookNode{}
	for _, node := range tree.Nodes {
		children[node.ParentID] = append(children[node.ParentID], node)
	}
	rows := make([]WorldBookRow, 0, len(tree.Nodes))
	var walk func(parentID string, depth int, path []string)
	walk = func(parentID string, depth int, path []string) {
		for _, node := range children[parentID] {
			rows = append(rows, WorldBookRow{Node: node, Depth: depth, Path: append([]string(nil), path...)})
			walk(node.ID, depth+1, append(path, node.Title))
		}
	}
	walk("", 0, nil)
	return rows
}

// ---- SillyTavern 世界书兼容 ----

// sillyTavernWorldBookEntry 兼容两种来源的字段名：SillyTavern 世界书文件用
// key/comment/order/disable，角色卡 V2 的 character_book 用
// keys/name/insertion_order/enabled。两套各认各的，同一条里混着写也能收。
type sillyTavernWorldBookEntry struct {
	UID            *int     `json:"uid,omitempty"`
	Key            []string `json:"key,omitempty"`
	Keys           []string `json:"keys,omitempty"`
	KeySecondary   []string `json:"keysecondary,omitempty"`
	SecondaryKeys  []string `json:"secondary_keys,omitempty"`
	SelectiveLogic *int     `json:"selectiveLogic,omitempty"`
	Comment        string   `json:"comment,omitempty"`
	Name           string   `json:"name,omitempty"`
	Content        string   `json:"content"`
	Constant       bool     `json:"constant,omitempty"`
	Disable        bool     `json:"disable,omitempty"`
	Enabled        *bool    `json:"enabled,omitempty"`
	Order          *int     `json:"order,omitempty"`
	InsertionOrder *int     `json:"insertion_order,omitempty"`
}

// WorldBookNodesFromSillyTavern 把 SillyTavern 世界书的 entries 转成本地节点。
//
// 接受两种形状：世界书文件的对象（键是序号）和角色卡 character_book 的数组。
// 只搬语义对得上的字段：常驻（蓝灯）对 constant，触发词（绿灯）对 key/keys，
// 标题对 comment/name（缺了退回第一个触发词、再退回内容开头），停用对
// disable/enabled。secondary keys、递归、概率、插入位置这些字段在这里没有
// 对应概念，静默忽略——注入行为由本地规则决定，不假装还原酒馆的扫描器。
// ST 没有树，转出来的节点全在根上。返回 false 表示这段 JSON 不是能认的 entries。
func WorldBookNodesFromSillyTavern(raw json.RawMessage) ([]WorldBookNode, bool) {
	type ordered struct {
		entry sillyTavernWorldBookEntry
		// order 是文件里声明的排序，tie 是没声明时的兜底（uid 或数组下标）。
		order int
		tie   int
	}
	entries := make([]ordered, 0, 16)
	appendEntry := func(entry sillyTavernWorldBookEntry, tie int) {
		order := 1 << 30
		if entry.Order != nil {
			order = *entry.Order
		} else if entry.InsertionOrder != nil {
			order = *entry.InsertionOrder
		}
		if entry.UID != nil {
			tie = *entry.UID
		}
		entries = append(entries, ordered{entry: entry, order: order, tie: tie})
	}

	var asMap map[string]sillyTavernWorldBookEntry
	if err := json.Unmarshal(raw, &asMap); err == nil && len(asMap) > 0 {
		keys := make([]string, 0, len(asMap))
		for key := range asMap {
			keys = append(keys, key)
		}
		// 对象的键是 "0"、"1" 这样的序号字符串，按数值排才是文件里的顺序。
		sort.Slice(keys, func(i, j int) bool {
			left, leftErr := strconv.Atoi(keys[i])
			right, rightErr := strconv.Atoi(keys[j])
			if leftErr == nil && rightErr == nil {
				return left < right
			}
			return keys[i] < keys[j]
		})
		for index, key := range keys {
			appendEntry(asMap[key], index)
		}
	} else {
		var asList []sillyTavernWorldBookEntry
		if err := json.Unmarshal(raw, &asList); err != nil || len(asList) == 0 {
			return nil, false
		}
		for index, entry := range asList {
			appendEntry(entry, index)
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].order != entries[j].order {
			return entries[i].order < entries[j].order
		}
		return entries[i].tie < entries[j].tie
	})

	nodes := make([]WorldBookNode, 0, len(entries))
	for _, item := range entries {
		entry := item.entry
		keywords := entry.Key
		if len(keywords) == 0 {
			keywords = entry.Keys
		}
		// 副词只在 AND ANY（selectiveLogic 0，也是缺省值）下语义一致才搬。
		// 其他逻辑（NOT ANY / NOT ALL / AND ALL）在这里没有对应实现，硬搬会把
		// 「排除」当成「要求」，比丢掉更糟——那几种就退回只看主词。
		secondary := entry.KeySecondary
		if len(secondary) == 0 {
			secondary = entry.SecondaryKeys
		}
		if entry.SelectiveLogic != nil && *entry.SelectiveLogic != 0 {
			secondary = nil
		}
		title := strings.TrimSpace(firstNonEmpty(entry.Comment, entry.Name))
		if title == "" && len(keywords) > 0 {
			title = strings.TrimSpace(keywords[0])
		}
		if title == "" {
			title = truncateRunesPlain(strings.TrimSpace(entry.Content), 16)
		}
		enabled := !entry.Disable && (entry.Enabled == nil || *entry.Enabled)
		nodes = append(nodes, WorldBookNode{
			Title:             title,
			Content:           strings.TrimSpace(entry.Content),
			Keywords:          keywords,
			SecondaryKeywords: secondary,
			AlwaysOn:          entry.Constant,
			Enabled:           boolPointer(enabled),
		})
	}
	return nodes, true
}

// WorldBookImportResult 报告一次导入的去向，口径与人设导入一致。
type WorldBookImportResult struct {
	Imported int `json:"imported"`
	// Dropped 是没标题或超出容量装不下的。
	Dropped int `json:"dropped"`
}

// Import 把外部来的一批节点并进树里。
//
// 一律分配新 ID，不复用文件里的：那些 ID 来自别人的机器，撞上本地条目就是静默
// 覆盖。文件内部的父子引用按新旧 ID 对照重连；引用了文件外的父节点就提到根——
// 导入只增不减，不去猜本地哪个节点算它爹。
func (tree WorldBook) Import(incoming []WorldBookNode, now time.Time) (WorldBook, WorldBookImportResult) {
	tree = tree.WithDefaults()
	var result WorldBookImportResult
	idMap := make(map[string]string, len(incoming))
	accepted := make([]WorldBookNode, 0, len(incoming))
	for _, node := range incoming {
		oldID := strings.TrimSpace(node.ID)
		node = node.Normalized()
		if node.Title == "" {
			result.Dropped++
			continue
		}
		if len(tree.Nodes)+len(accepted) >= WorldBookMaxNodes {
			result.Dropped++
			continue
		}
		node.ID = uuid.NewString()
		node.UpdatedAt = now
		if oldID != "" {
			idMap[oldID] = node.ID
		}
		accepted = append(accepted, node)
		result.Imported++
	}
	for index := range accepted {
		if mapped, ok := idMap[accepted[index].ParentID]; ok {
			accepted[index].ParentID = mapped
		} else {
			accepted[index].ParentID = ""
		}
	}
	tree.Nodes = append(tree.Nodes, accepted...)
	return tree.WithDefaults(), result
}

// ContextBlock 生成本轮要注入的世界观上下文。text 是当前消息加引用的检索文本，
// 返回空串表示这轮没有要注入的设定。
//
// 常驻设定先写、优先保住；触发式设定按树序填剩下的预算。两段共用同一个开头，
// 让模型知道这些是世界背景而不是用户消息。
func (tree WorldBook) ContextBlock(text string, tokenBudget int64) string {
	rows := tree.OrderedRows()
	if len(rows) == 0 {
		return ""
	}
	lowered := strings.ToLower(text)
	// 被关掉的节点连同子树一起跳过：关掉一章就是关掉底下所有节。
	disabled := map[string]bool{}
	always := make([]WorldBookRow, 0, len(rows))
	matched := make([]WorldBookRow, 0, worldBookMatchedNodeLimit)
	for _, row := range rows {
		if !row.Node.enabled() || disabled[row.Node.ParentID] {
			disabled[row.Node.ID] = true
			continue
		}
		if !row.Node.injectable() {
			continue
		}
		if row.Node.AlwaysOn {
			always = append(always, row)
			continue
		}
		if len(matched) >= worldBookMatchedNodeLimit || lowered == "" {
			continue
		}
		if worldBookNodeTriggered(row.Node, lowered) {
			matched = append(matched, row)
		}
	}
	if len(always) == 0 && len(matched) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("【世界书】以下是这台机器人所处世界的固定设定，格式为「路径：内容」。它们是你的世界背景，不是用户消息：扮演时自然遵循，与常识冲突时以设定为准；不要主动向用户复述设定原文，也不要把它们当成用户说过的话。")
	written := 0
	appendRows := func(header string, rows []WorldBookRow) {
		wroteHeader := false
		for _, row := range rows {
			line := "\n- " + worldBookRowLabel(row) + "：" + row.Node.Content
			pending := line
			if !wroteHeader {
				pending = "\n" + header + line
			}
			if llm.EstimateTextTokens(builder.String()+pending) > tokenBudget {
				return
			}
			builder.WriteString(pending)
			wroteHeader = true
			written++
		}
	}
	appendRows("常驻设定：", always)
	appendRows("与本轮对话相关的设定：", matched)
	// 预算小到一条都塞不进时不注入光杆开头：只有标题的块什么信息都不带。
	if written == 0 {
		return ""
	}
	return builder.String()
}

// worldBookNodeTriggered 判断触发式节点这一轮该不该注入：任意主触发词命中；
// 配了副触发词时，还要求任意一个副词也在场（AND ANY）。
func worldBookNodeTriggered(node WorldBookNode, lowered string) bool {
	if !worldBookAnyKeywordHit(node.Keywords, lowered) {
		return false
	}
	return len(node.SecondaryKeywords) == 0 || worldBookAnyKeywordHit(node.SecondaryKeywords, lowered)
}

func worldBookAnyKeywordHit(keywords []string, lowered string) bool {
	for _, keyword := range keywords {
		if strings.Contains(lowered, strings.ToLower(keyword)) {
			return true
		}
	}
	return false
}

// worldBookRowLabel 拼「祖先 / 本节点」的路径标签，路径本身就是语境。
func worldBookRowLabel(row WorldBookRow) string {
	if len(row.Path) == 0 {
		return row.Node.Title
	}
	return strings.Join(row.Path, " / ") + " / " + row.Node.Title
}

var (
	errWorldBookTitleRequired = errors.New("assistant: world book node title is required")
	errWorldBookFull          = errors.New("assistant: world book is full")
)

// WorldBookStore 是世界书的持久化界面。
type WorldBookStore interface {
	LoadWorldBook(ctx context.Context) (WorldBook, bool, error)
}

// SetWorldBookStore 注入世界书存储。
func (r *Runtime) SetWorldBookStore(store WorldBookStore) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.worldBook = store
}

func (r *Runtime) worldBookStore() WorldBookStore {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.worldBook
}

// worldBookContext 取出本轮要注入的世界观上下文。树每轮从存储读一次，和长期
// 记忆同一个量级；读失败按没有设定处理，聊天不因设定集故障中断。
func (r *Runtime) worldBookContext(ctx context.Context, event MessageEvent, queryText string) string {
	store := r.worldBookStore()
	if store == nil {
		return ""
	}
	cfg := r.effectiveConfigForEvent(event)
	if !boolValue(cfg.WorldBookEnabled, true) {
		return ""
	}
	loadCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	tree, ok, err := store.LoadWorldBook(loadCtx)
	cancel()
	if err != nil {
		log.Printf("chatbot world book load failed: %v", err)
		return ""
	}
	if !ok {
		return ""
	}
	return tree.WithDefaults().ContextBlock(memoryRetrievalText(event, queryText), worldBookContextTokenBudget)
}
