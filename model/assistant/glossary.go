// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"log"
	"sort"
	"strings"
	"time"
)

// 词典：Diana 自己维护的一本「群黑话本」。
//
// 结构化记忆记的是「关于某个人我知道哪些事」，答不了「他们刚才那个词是什么意思」。
// 梗、内部称呼、缩写、外号这类东西不属于任何一个人，而属于这个群，而且会变：今天
// 是夸人的，下个月可能就成了反话。所以词典和记忆分开存，并且从一开始就按「会被
// 反复修订」来设计——每条词条带版本号和修订记录，改了能看出改了什么、谁改的、
// 为什么改；删除是软删除，删错了还能翻出来。
//
// 检索走两条路，缺一不可：
//   - 自动命中：每轮回复前拿当前消息去撞词典，命中的词条注进提示词。用户不会为了
//     一个梗特意说「查一下词典」，模型也不会主动去查一个它以为自己认识的词。
//   - 工具检索：diana.glossary 让模型主动查、写、改、删。
//
// 命中会回写使用次数和最后命中时间，长期没人用的词条自然沉底——一本没人维护的
// 词典和没有词典没区别。
const (
	// GlossaryScopeGlobal 是升级前那本所有机器人共用的词典。现在每台机器人各有
	// 一本（见 glossaryGlobalScope），这个键只作为读取兜底保留：迁移会把里面的
	// 词条搬到当时的当前配置档下，之后不再往这里写。
	GlossaryScopeGlobal = "global"

	// GlossaryScopeBotPrefix 前缀出来的是「这台机器人的全局词典」。同一个梗在两
	// 台机器人那里可以有不同的记法，共用一本会让它们互相改对方的释义。
	GlossaryScopeBotPrefix = "bot:"

	// GlossaryTermMaxRunes 限制词条本身的长度。词典收的是词，不是句子；放开了
	// 模型会把整段对话当成「词」塞进来。
	GlossaryTermMaxRunes = 32
	// GlossaryMeaningMaxRunes 限制释义长度。释义是给回复时看一眼的，不是百科。
	GlossaryMeaningMaxRunes = 400
	// GlossaryExampleMaxRunes 限制例句长度。
	GlossaryExampleMaxRunes = 160
	// GlossaryNoteMaxRunes 限制修订说明长度。
	GlossaryNoteMaxRunes = 160
	// GlossaryMaxAliases 限制别名个数。
	GlossaryMaxAliases = 8

	// glossaryContextLimit 是自动命中最多注入的词条数。命中一堆词说明这条消息
	// 本来就在聊黑话，注太多反而把当前消息挤走。
	glossaryContextLimit = 6
	// glossaryContextBudget 是【词典】段落的字符预算。
	glossaryContextBudget = 900

	// glossaryLookupTimeout 是自动命中查询的超时。查不到就当没有词典，绝不拖回复。
	glossaryLookupTimeout = 2 * time.Second
)

// GlossaryStatus 描述词条当前状态。删除是软删除：修订史要留得住。
type GlossaryStatus string

const (
	GlossaryStatusActive  GlossaryStatus = "active"
	GlossaryStatusDeleted GlossaryStatus = "deleted"
)

// GlossaryRevision 是一次修订的快照，记录改之前长什么样。
type GlossaryRevision struct {
	Version      int       `json:"version"`
	Meaning      string    `json:"meaning,omitempty"`
	Example      string    `json:"example,omitempty"`
	Aliases      []string  `json:"aliases,omitempty"`
	Note         string    `json:"note,omitempty"`
	EditorUserID string    `json:"editor_user_id,omitempty"`
	EditorName   string    `json:"editor_name,omitempty"`
	RecordedAt   time.Time `json:"recorded_at"`
}

// GlossaryEntry 是一条词条的当前状态。
type GlossaryEntry struct {
	ID           string         `json:"id"`
	ScopeKey     string         `json:"scope_key"`
	Term         string         `json:"term"`
	Aliases      []string       `json:"aliases,omitempty"`
	Meaning      string         `json:"meaning"`
	Example      string         `json:"example,omitempty"`
	Note         string         `json:"note,omitempty"`
	AuthorUserID string         `json:"author_user_id,omitempty"`
	AuthorName   string         `json:"author_name,omitempty"`
	EditorUserID string         `json:"editor_user_id,omitempty"`
	EditorName   string         `json:"editor_name,omitempty"`
	UsageCount   int            `json:"usage_count"`
	LastUsedAt   time.Time      `json:"last_used_at,omitempty"`
	Version      int            `json:"version"`
	Status       GlossaryStatus `json:"status"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	// Revisions 只在按词条取详情时填充，列表和自动命中不带它。
	Revisions []GlossaryRevision `json:"revisions,omitempty"`
}

// GlossaryUpsertRequest 是一次写入。同一个作用域下词条按归一化后的词去重：
// 已存在就是修订（版本 +1，旧内容进修订史），不存在才是新建。
type GlossaryUpsertRequest struct {
	ScopeKey        string
	Term            string
	Aliases         []string
	Meaning         string
	Example         string
	Note            string
	EditorUserID    string
	EditorName      string
	SourceSession   string
	SourceMessageID string
	Now             time.Time
}

// GlossaryQuery 描述一次检索。ScopeKeys 按优先级从高到低排列（当前会话优先于
// global），Terms 用于精确命中，Text 用于「这段话里出现了哪些词条」。
type GlossaryQuery struct {
	ScopeKeys      []string
	Terms          []string
	Text           string
	Limit          int
	IncludeDeleted bool
	Now            time.Time
}

// GlossaryStore 是词典的持久化契约。没有实现时词典整体静默失效：查不到、写不了，
// 但回复照常。
type GlossaryStore interface {
	UpsertGlossaryEntry(ctx context.Context, request GlossaryUpsertRequest) (entry GlossaryEntry, created bool, err error)
	// DeleteGlossaryEntry 软删除一条词条，note 记录为什么删。
	DeleteGlossaryEntry(ctx context.Context, scopeKey, term, editorUserID, editorName, note string, now time.Time) (GlossaryEntry, bool, error)
	// RestoreGlossaryEntry 撤销一次软删除。删错了要能原地救回来，而不是重新编一遍释义。
	RestoreGlossaryEntry(ctx context.Context, scopeKey, term, editorUserID, editorName string, now time.Time) (GlossaryEntry, bool, error)
	// LookupGlossaryEntries 按 Terms 精确命中、按 Text 做「文本里包含哪个词条」匹配。
	LookupGlossaryEntries(ctx context.Context, query GlossaryQuery) ([]GlossaryEntry, error)
	// GlossaryEntryDetail 返回单条词条及其修订史。
	GlossaryEntryDetail(ctx context.Context, scopeKey, term string) (GlossaryEntry, bool, error)
	// ListGlossaryEntries 按作用域列出词条，供人工翻阅和清理。
	ListGlossaryEntries(ctx context.Context, query GlossaryQuery) ([]GlossaryEntry, error)
	// TouchGlossaryEntries 记录一次命中。失败只影响冷热排序，不影响回复。
	TouchGlossaryEntries(ctx context.Context, ids []string, at time.Time) error
}

// NormalizeGlossaryTerm 归一化词条，用于去重和匹配。大小写不敏感，去掉首尾空白，
// 中间连续空白压成一个空格；中文词本来就没有空格，这条只影响英文缩写。
func NormalizeGlossaryTerm(raw string) string {
	return strings.ToLower(strings.Join(strings.Fields(raw), " "))
}

// TruncateGlossaryText 按 rune 截断并去掉换行。词典里的每个字段都要能塞进一行。
func TruncateGlossaryText(raw string, limit int) string {
	text := strings.Join(strings.Fields(raw), " ")
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return strings.TrimSpace(string(runes[:limit])) + "…"
}

// NormalizeGlossaryAliases 清洗别名：去空、去重、去掉和词条本身重复的、限个数。
func NormalizeGlossaryAliases(term string, aliases []string) []string {
	normalizedTerm := NormalizeGlossaryTerm(term)
	seen := map[string]bool{normalizedTerm: true}
	out := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		alias = TruncateGlossaryText(alias, GlossaryTermMaxRunes)
		key := NormalizeGlossaryTerm(alias)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, alias)
		if len(out) >= GlossaryMaxAliases {
			break
		}
	}
	return out
}

// glossaryGlobalScope 返回这台机器人的全局词典作用域。拿不到机器人身份时退回
// 升级前那本共用词典，总比把词条写丢好。
func glossaryGlobalScope(botProfileID string) string {
	if botProfileID = strings.TrimSpace(botProfileID); botProfileID != "" {
		return GlossaryScopeBotPrefix + botProfileID
	}
	return GlossaryScopeGlobal
}

// glossaryScopeKeys 是查词时依次生效的作用域：先看这个会话自己的，再看这台机器人
// 的全局本，最后兜底读升级前那本共用的（迁移之后通常已经空了）。顺序即优先级：
// 同一个词在本群和全局都有释义时，本群的那条说了算。
//
// 共用一本词典时读取顺序不变：新词条都写进机器人全局本，本群作用域里剩下的是打开
// 开关之前记的，让它在自己群里继续生效比凭空消失更合理。
func glossaryScopeKeys(event MessageEvent) []string {
	global := glossaryGlobalScope(event.ProfileID)
	scopes := make([]string, 0, 3)
	if scope := glossarySessionScope(event); scope != "" {
		scopes = append(scopes, scope)
	}
	scopes = append(scopes, global)
	if global != GlossaryScopeGlobal {
		scopes = append(scopes, GlossaryScopeGlobal)
	}
	return scopes
}

// glossarySessionScope 返回当前会话的作用域键，拿不到有效身份时返回空串。
//
// sessionKey 对一个既没有群号也没有账号的事件会拼出 "private:" 这种只有前缀的键。
// 那不是一个会话，而是一个所有匿名事件共用的桶：当成作用域用，等于把互不相干的
// 上下文串到一起。这里要求 id 段非空，取不到就退回全局。
func glossarySessionScope(event MessageEvent) string {
	scope := strings.TrimSpace(sessionKey(event))
	if scope == "" || scope == GlossaryScopeGlobal || strings.HasSuffix(scope, ":") {
		return ""
	}
	return scope
}

// glossaryScopeKeyForWrite 返回写入用的作用域。
//
// 默认按会话隔离：一个群的内部梗默认只在这个群里成立，global 是主人特权。
// 打开「词典跨群共用」之后全部写进 global——这时机器人维护的是一本共用词典，
// 再按群分家反而会让同一个词在不同群各记一遍。
func glossaryScopeKeyForWrite(event MessageEvent, cfg BotConfig, global bool, owner bool) string {
	if boolValue(cfg.GlossarySharedScopeEnabled, false) {
		return glossaryGlobalScope(event.ProfileID)
	}
	if global && owner {
		return glossaryGlobalScope(event.ProfileID)
	}
	// 没有有效会话身份时写这台机器人的全局本："private:" 那种共用桶更糟。
	if scope := glossarySessionScope(event); scope != "" {
		return scope
	}
	return glossaryGlobalScope(event.ProfileID)
}

// glossaryContext 拿当前消息去撞词典，把命中的词条组装成提示词段落。
// 没有存储层、没命中、或者查询失败时一律返回空串：词典是增强，不是前置依赖。
func (r *Runtime) glossaryContext(ctx context.Context, event MessageEvent, queryText string) string {
	store := r.glossaryStore()
	if store == nil {
		return ""
	}
	text := strings.TrimSpace(memoryRetrievalText(event, queryText))
	if text == "" {
		return ""
	}
	lookupCtx, cancel := context.WithTimeout(ctx, glossaryLookupTimeout)
	entries, err := store.LookupGlossaryEntries(lookupCtx, GlossaryQuery{
		ScopeKeys: glossaryScopeKeys(event),
		Text:      text,
		Limit:     glossaryContextLimit,
		Now:       time.Now(),
	})
	cancel()
	if err != nil {
		log.Printf("chatbot glossary lookup failed: %v", err)
		return ""
	}
	if len(entries) == 0 {
		return ""
	}
	r.touchGlossaryEntries(ctx, store, entries)
	return formatGlossaryContext(entries)
}

func (r *Runtime) glossaryStore() GlossaryStore {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.glossary
}

// touchGlossaryEntries 异步回写命中。和记忆那边一样，回写失败最多让冷热排序失真。
func (r *Runtime) touchGlossaryEntries(ctx context.Context, store GlossaryStore, entries []GlossaryEntry) {
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if id := strings.TrimSpace(entry.ID); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return
	}
	now := time.Now()
	go func() {
		touchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), glossaryLookupTimeout)
		defer cancel()
		if err := store.TouchGlossaryEntries(touchCtx, ids, now); err != nil {
			log.Printf("chatbot glossary touch failed: %v", err)
		}
	}()
}

// glossaryContextPrefix 明说这段是「拿来理解，不是拿来复述」。少了这句，模型会
// 把命中的词条当成用户在问词义，回一段解释。
const glossaryContextPrefix = "【词典命中，仅用于理解当前消息里的说法】\n" +
	"按这些释义理解对应的词；不要复述词条，不要解释你查过词典，也不要因为命中就把话题转到这个词上。释义和当前语境明显对不上时以语境为准。\n"

func formatGlossaryContext(entries []GlossaryEntry) string {
	var builder strings.Builder
	builder.WriteString(glossaryContextPrefix)
	used := 0
	for _, entry := range entries {
		line := "- " + formatGlossaryLine(entry)
		if used+len(line) > glossaryContextBudget && used > 0 {
			break
		}
		used += len(line)
		builder.WriteString(line)
		builder.WriteString("\n")
	}
	return strings.TrimRight(builder.String(), "\n")
}

func formatGlossaryLine(entry GlossaryEntry) string {
	var builder strings.Builder
	builder.WriteString(entry.Term)
	if len(entry.Aliases) > 0 {
		builder.WriteString("（又作 " + strings.Join(entry.Aliases, "、") + "）")
	}
	builder.WriteString("：")
	builder.WriteString(entry.Meaning)
	if example := strings.TrimSpace(entry.Example); example != "" {
		builder.WriteString("　例：" + example)
	}
	return builder.String()
}

// SortGlossaryEntriesByScope 给命中结果定序：作用域优先级 > 使用次数 > 更新时间。
// 存储层已经排过一遍，这里保证纯内存实现（测试用）和 SQLite 表现一致。
func SortGlossaryEntriesByScope(entries []GlossaryEntry, scopeKeys []string) {
	priority := make(map[string]int, len(scopeKeys))
	for index, scope := range scopeKeys {
		priority[scope] = index
	}
	rank := func(entry GlossaryEntry) int {
		if value, ok := priority[entry.ScopeKey]; ok {
			return value
		}
		return len(scopeKeys)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if left, right := rank(entries[i]), rank(entries[j]); left != right {
			return left < right
		}
		if entries[i].UsageCount != entries[j].UsageCount {
			return entries[i].UsageCount > entries[j].UsageCount
		}
		return entries[i].UpdatedAt.After(entries[j].UpdatedAt)
	})
}
