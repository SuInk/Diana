// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"log"
	"sort"
	"strings"
	"time"
)

// 笔记本：Diana 自己维护的一本随手记。
//
// 它的前身是词典，只能记「某个词是什么意思」。但同一套机制——按作用域归属、
// 每次修改留修订史、软删除、命中即注入、人能在控制台翻和改——对「这个群约定过
// 什么」「某人不吃香菜」「下周三要交房租」一样成立，限死在词条上纯粹是浪费。
// 所以条目带类型：词条、事实、偏好、事件、待办、人物，共用同一套存取和策展。
//
// 和结构化记忆的分工不是「记什么」，而是「谁写的、能不能改」：
//   - 结构化记忆由模型从对话里自动抽取，人只能看，改不了，也没有修订史。
//   - 笔记本是被特意写下来的——模型经工具写，或人在控制台写——每条带版本号，
//     改了能看出改了什么、谁改的、为什么改；删错了还能翻出来。
//
// 需要「说过就记住」用前者，需要「这条必须准，错了我要能改」用后者。
//
// 检索走两条路，缺一不可：
//   - 自动命中：每轮回复前拿当前消息去撞笔记本，命中的条目注进提示词。用户不会
//     为了一个梗特意说「查一下笔记」，模型也不会主动去查一个它以为自己认识的词。
//   - 工具检索：diana.notebook 让模型主动查、写、改、删。
//
// 命中靠标题和触发词。词条的标题本身就会出现在对话里，但「群规：十点后不刷屏」
// 这种条目不会——所以触发词（原来的别名）在这里是必需品而不是可选项：它是让一条
// 不以关键词命名的笔记也能被想起来的唯一手段。
//
// 命中会回写使用次数和最后命中时间，长期没人用的条目自然沉底——一本没人维护的
// 笔记本和没有笔记本没区别。

// NotebookKind 是一条笔记的类型。
//
// 类型只影响两件事：标题长度上限，以及注进提示词时那一行怎么读。存取、修订、
// 命中逻辑对所有类型完全一致——分类是给人和模型看的，不是给存储看的。
type NotebookKind string

const (
	// NotebookKindTerm 是词条：梗、黑话、缩写、外号。笔记本的前身只有这一种，
	// 老数据升级后全部落在这里。
	NotebookKindTerm NotebookKind = "term"
	// NotebookKindFact 是事实：群规、约定、谁负责什么。
	NotebookKindFact NotebookKind = "fact"
	// NotebookKindPreference 是偏好：喜欢什么、忌口什么、别碰什么话题。
	NotebookKindPreference NotebookKind = "preference"
	// NotebookKindEvent 是事件：什么时候发生过什么。
	NotebookKindEvent NotebookKind = "event"
	// NotebookKindTodo 是待办：答应过要做、还没做完的事。
	NotebookKindTodo NotebookKind = "todo"
	// NotebookKindPerson 是人物：这个人是谁、和群里什么关系。
	NotebookKindPerson NotebookKind = "person"
)

// notebookKindLabels 是类型的中文名，控制台和提示词共用一份，避免两处漂移。
var notebookKindLabels = map[NotebookKind]string{
	NotebookKindTerm:       "词条",
	NotebookKindFact:       "事实",
	NotebookKindPreference: "偏好",
	NotebookKindEvent:      "事件",
	NotebookKindTodo:       "待办",
	NotebookKindPerson:     "人物",
}

// notebookKindOrder 固定类型的展示顺序，词条排在最前——它是最常用的一类。
var notebookKindOrder = []NotebookKind{
	NotebookKindTerm, NotebookKindFact, NotebookKindPreference,
	NotebookKindEvent, NotebookKindTodo, NotebookKindPerson,
}

// NotebookKinds 返回全部类型，顺序即展示顺序。
func NotebookKinds() []NotebookKind {
	return append([]NotebookKind(nil), notebookKindOrder...)
}

// Label 返回类型的中文名，未知类型退回原值而不是空串——界面上宁可显示一个
// 陌生的类型名，也不该显示一个没有类型的条目。
func (k NotebookKind) Label() string {
	if label, ok := notebookKindLabels[k]; ok {
		return label
	}
	if trimmed := strings.TrimSpace(string(k)); trimmed != "" {
		return trimmed
	}
	return notebookKindLabels[NotebookKindTerm]
}

// Valid 判断是不是已知类型。
func (k NotebookKind) Valid() bool {
	_, ok := notebookKindLabels[k]
	return ok
}

// NormalizeNotebookKind 规整类型，空值和未知值一律当词条。
//
// 落在词条而不是报错：类型是后加的，老数据、旧客户端和模型偶尔写错的值都会走到
// 这里，为此拒绝一条本来能用的笔记不值得。
func NormalizeNotebookKind(raw string) NotebookKind {
	kind := NotebookKind(strings.ToLower(strings.TrimSpace(raw)))
	if kind.Valid() {
		return kind
	}
	return NotebookKindTerm
}

// TitleLimit 返回该类型的标题长度上限。
//
// 词条收的是词，放开了模型会把整段对话当成「词」塞进来，所以仍然卡 32；
// 其余类型的标题是一句概括（「群规：十点后不刷屏」），32 个字根本写不下。
func (k NotebookKind) TitleLimit() int {
	if k == NotebookKindTerm {
		return NotebookTermTitleMaxRunes
	}
	return NotebookTitleMaxRunes
}

const (
	// NotebookScopeGlobal 是升级前那本所有机器人共用的笔记。现在每台机器人各有
	// 一本（见 notebookGlobalScope），这个键只作为读取兜底保留：迁移会把里面的
	// 条目搬到当时的当前配置档下，之后不再往这里写。
	NotebookScopeGlobal = "global"

	// NotebookScopeBotPrefix 前缀出来的是「这台机器人的全局笔记本」。同一个梗在
	// 两台机器人那里可以有不同的记法，共用一本会让它们互相改对方的条目。
	NotebookScopeBotPrefix = "bot:"

	// NotebookTermTitleMaxRunes 限制词条标题的长度。词条收的是词，不是句子；
	// 放开了模型会把整段对话当成「词」塞进来。
	NotebookTermTitleMaxRunes = 32
	// NotebookTitleMaxRunes 是其余类型的标题上限：一句概括的长度，不是一段话。
	// 「群规：十点后不刷屏」这种标题 32 个字根本写不下。
	NotebookTitleMaxRunes = 80
	// NotebookContentMaxRunes 限制正文长度。正文是给回复时看一眼的，不是百科。
	NotebookContentMaxRunes = 400
	// NotebookExampleMaxRunes 限制例子长度。
	NotebookExampleMaxRunes = 160
	// NotebookNoteMaxRunes 限制修订说明长度。
	NotebookNoteMaxRunes = 160
	// NotebookMaxKeywords 限制触发词个数。
	NotebookMaxKeywords = 8

	// notebookContextLimit 是自动命中最多注入的条目数。命中一堆说明这条消息
	// 本来就在聊这些，注太多反而把当前消息挤走。
	notebookContextLimit = 6
	// notebookContextBudget 是【笔记】段落的字符预算。
	notebookContextBudget = 900

	// notebookLookupTimeout 是自动命中查询的超时。查不到就当没有笔记，绝不拖回复。
	notebookLookupTimeout = 2 * time.Second
)

// NotebookStatus 描述条目当前状态。删除是软删除：修订史要留得住。
type NotebookStatus string

const (
	NotebookStatusActive  NotebookStatus = "active"
	NotebookStatusDeleted NotebookStatus = "deleted"
)

// NotebookRevision 是一次修订的快照，记录改之前长什么样。
type NotebookRevision struct {
	Version      int          `json:"version"`
	Kind         NotebookKind `json:"kind,omitempty"`
	Meaning      string       `json:"meaning,omitempty"`
	Example      string       `json:"example,omitempty"`
	Aliases      []string     `json:"aliases,omitempty"`
	Note         string       `json:"note,omitempty"`
	EditorUserID string       `json:"editor_user_id,omitempty"`
	EditorName   string       `json:"editor_name,omitempty"`
	RecordedAt   time.Time    `json:"recorded_at"`
}

// NotebookEntry 是一条笔记的当前状态。
//
// 字段名沿用笔记本时代的 term/meaning/aliases 而不是改成 title/content/keywords：
// 它们已经是落库的列名和前端的字段名，为了措辞好看去改会把一次纯增量的升级
// 变成一次需要双向兼容的数据迁移。语义以类型和界面文案为准。
type NotebookEntry struct {
	ID       string       `json:"id"`
	ScopeKey string       `json:"scope_key"`
	Kind     NotebookKind `json:"kind"`
	// Term 是标题：词条是那个词本身，其余类型是一句概括。
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
	Status       NotebookStatus `json:"status"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	// Revisions 只在按条目取详情时填充，列表和自动命中不带它。
	Revisions []NotebookRevision `json:"revisions,omitempty"`
}

// NotebookUpsertRequest 是一次写入。同一个作用域下条目按归一化后的词去重：
// 已存在就是修订（版本 +1，旧内容进修订史），不存在才是新建。
type NotebookUpsertRequest struct {
	ScopeKey        string
	Kind            NotebookKind
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

// NotebookQuery 描述一次检索。ScopeKeys 按优先级从高到低排列（当前会话优先于
// global），Terms 用于精确命中，Text 用于「这段话里出现了哪些条目」。
type NotebookQuery struct {
	ScopeKeys []string
	// Kinds 为空表示不限类型。控制台按类型筛选时用它，自动命中从不限制类型——
	// 一条待办和一个梗同样可能是这句话的上下文。
	Kinds          []NotebookKind
	Terms          []string
	Text           string
	Limit          int
	IncludeDeleted bool
	Now            time.Time
}

// NotebookStore 是笔记本的持久化契约。没有实现时笔记本整体静默失效：查不到、写不了，
// 但回复照常。
type NotebookStore interface {
	UpsertNotebookEntry(ctx context.Context, request NotebookUpsertRequest) (entry NotebookEntry, created bool, err error)
	// DeleteNotebookEntry 软删除一条条目，note 记录为什么删。
	DeleteNotebookEntry(ctx context.Context, scopeKey, term, editorUserID, editorName, note string, now time.Time) (NotebookEntry, bool, error)
	// RestoreNotebookEntry 撤销一次软删除。删错了要能原地救回来，而不是重新编一遍释义。
	RestoreNotebookEntry(ctx context.Context, scopeKey, term, editorUserID, editorName string, now time.Time) (NotebookEntry, bool, error)
	// LookupNotebookEntries 按 Terms 精确命中、按 Text 做「文本里包含哪个条目」匹配。
	LookupNotebookEntries(ctx context.Context, query NotebookQuery) ([]NotebookEntry, error)
	// NotebookEntryDetail 返回单条条目及其修订史。
	NotebookEntryDetail(ctx context.Context, scopeKey, term string) (NotebookEntry, bool, error)
	// ListNotebookEntries 按作用域列出条目，供人工翻阅和清理。
	ListNotebookEntries(ctx context.Context, query NotebookQuery) ([]NotebookEntry, error)
	// TouchNotebookEntries 记录一次命中。失败只影响冷热排序，不影响回复。
	TouchNotebookEntries(ctx context.Context, ids []string, at time.Time) error
}

// NormalizeNotebookTitle 归一化条目，用于去重和匹配。大小写不敏感，去掉首尾空白，
// 中间连续空白压成一个空格；中文词本来就没有空格，这条只影响英文缩写。
func NormalizeNotebookTitle(raw string) string {
	return strings.ToLower(strings.Join(strings.Fields(raw), " "))
}

// TruncateNotebookText 按 rune 截断并去掉换行。笔记本里的每个字段都要能塞进一行。
func TruncateNotebookText(raw string, limit int) string {
	text := strings.Join(strings.Fields(raw), " ")
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return strings.TrimSpace(string(runes[:limit])) + "…"
}

// NormalizeNotebookAliases 清洗别名：去空、去重、去掉和条目本身重复的、限个数。
func NormalizeNotebookAliases(term string, aliases []string) []string {
	normalizedTerm := NormalizeNotebookTitle(term)
	seen := map[string]bool{normalizedTerm: true}
	out := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		alias = TruncateNotebookText(alias, NotebookTitleMaxRunes)
		key := NormalizeNotebookTitle(alias)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, alias)
		if len(out) >= NotebookMaxKeywords {
			break
		}
	}
	return out
}

// notebookGlobalScope 返回这台机器人的全局笔记本作用域。拿不到机器人身份时退回
// 升级前那本共用笔记本，总比把条目写丢好。
func notebookGlobalScope(botProfileID string) string {
	if botProfileID = strings.TrimSpace(botProfileID); botProfileID != "" {
		return NotebookScopeBotPrefix + botProfileID
	}
	return NotebookScopeGlobal
}

// notebookScopeKeys 是查词时依次生效的作用域：先看这个会话自己的，再看这台机器人
// 的全局本，最后兜底读升级前那本共用的（迁移之后通常已经空了）。顺序即优先级：
// 同一个词在本群和全局都有释义时，本群的那条说了算。
//
// 共用一本笔记本时读取顺序不变：新条目都写进机器人全局本，本群作用域里剩下的是打开
// 开关之前记的，让它在自己群里继续生效比凭空消失更合理。
func notebookScopeKeys(event MessageEvent) []string {
	global := notebookGlobalScope(event.ProfileID)
	scopes := make([]string, 0, 3)
	if scope := notebookSessionScope(event); scope != "" {
		scopes = append(scopes, scope)
	}
	scopes = append(scopes, global)
	if global != NotebookScopeGlobal {
		scopes = append(scopes, NotebookScopeGlobal)
	}
	return scopes
}

// notebookSessionScope 返回当前会话的作用域键，拿不到有效身份时返回空串。
//
// sessionKey 对一个既没有群号也没有账号的事件会拼出 "private:" 这种只有前缀的键。
// 那不是一个会话，而是一个所有匿名事件共用的桶：当成作用域用，等于把互不相干的
// 上下文串到一起。这里要求 id 段非空，取不到就退回全局。
func notebookSessionScope(event MessageEvent) string {
	scope := strings.TrimSpace(sessionKey(event))
	if scope == "" || scope == NotebookScopeGlobal || strings.HasSuffix(scope, ":") {
		return ""
	}
	return scope
}

// errNotebookBotIdentityMissing 表示这次写入落不到任何一台机器人名下。
//
// 升级前那本所有机器人共用的 global 只作为读取兜底保留；往里写等于把这台机器人
// 学到的梗记到别的机器人头上，还会在多实例下互相改。宁可报错让模型如实说没记住，
// 也不要悄悄写进一本谁都不该再写的旧本子。
var errNotebookBotIdentityMissing = errors.New("无法确定当前机器人身份（配置档 ID 为空），这条没有写入笔记本")

// notebookScopeKeyForWrite 返回写入用的作用域。
//
// 默认笔记本跟随机器人：群聊私聊都写进这台机器人的全局本——机器人维护的是一本
// 自己的笔记，按群分家会让同一个词在不同群各记一遍。关掉「跟随机器人」之后才按
// 会话隔离：一个群的内部梗只在这个群里成立，global 变成主人特权。
//
// 任何会落到升级前共用 global 的写入都报错（见 errNotebookBotIdentityMissing）。
func notebookScopeKeyForWrite(event MessageEvent, cfg BotConfig, global bool, owner bool) (string, error) {
	scope := ""
	switch {
	case boolValue(cfg.NotebookSharedScopeEnabled, true):
		scope = notebookGlobalScope(event.ProfileID)
	case global && owner:
		scope = notebookGlobalScope(event.ProfileID)
	case notebookSessionScope(event) != "":
		scope = notebookSessionScope(event)
	default:
		// 没有有效会话身份时写这台机器人的全局本："private:" 那种共用桶更糟。
		scope = notebookGlobalScope(event.ProfileID)
	}
	if scope == NotebookScopeGlobal {
		return "", errNotebookBotIdentityMissing
	}
	return scope, nil
}

// 一条笔记的正文由「主释义」和至多几条「补充说法」组成，补充说法跟在主释义后面：
//
//	带薪拉屎的缩写 补充说法（小明，2026-09-03）：也有人用来指开会摸鱼
//
// 正文经 TruncateNotebookText 后只有单行（空白全部折成空格），所以补充说法靠固定
// 前缀定位而不是靠换行。
//
// 这是「有人说不对」时的合并策略。以前谁说一句「不是这个意思」，模型就整条覆盖：
// 一个群里的一句话能把另一个群教会它的释义抹掉，笔记本跟随机器人之后尤其如此。
// 现在只有主人和当初记它的人能改主释义；别人的纠正和另一个群的不同用法都作为补充
// 说法并存，回复时两种都能提，主人或原记录者看到后再拍板。
const (
	notebookSupplementPrefix = "补充说法（"
	// notebookMaxSupplements 限制并存的补充说法条数。超过三条说明这个词本身就是
	// 各说各话，再往上堆只会把正文撑爆，最老的一条让位。
	notebookMaxSupplements = 3
)

type notebookSupplement struct {
	Editor string
	Date   string
	Text   string
}

func (s notebookSupplement) line() string {
	return notebookSupplementPrefix + s.Editor + "，" + s.Date + "）：" + s.Text
}

// splitNotebookMeaning 把正文拆成主释义和补充说法。解析不出「（谁，日期）：」头部的
// 片段原样留在主释义里，不丢内容。
func splitNotebookMeaning(meaning string) (string, []notebookSupplement) {
	segments := strings.Split(strings.TrimSpace(meaning), notebookSupplementPrefix)
	primary := strings.TrimSpace(segments[0])
	var supplements []notebookSupplement
	for _, segment := range segments[1:] {
		head, text, ok := strings.Cut(segment, "）：")
		editor, date, dated := strings.Cut(head, "，")
		if !ok || !dated || strings.TrimSpace(text) == "" || strings.ContainsAny(head, "（）") {
			primary = strings.TrimSpace(primary + " " + notebookSupplementPrefix + segment)
			continue
		}
		supplements = append(supplements, notebookSupplement{Editor: strings.TrimSpace(editor), Date: strings.TrimSpace(date), Text: strings.TrimSpace(text)})
	}
	return primary, supplements
}

func joinNotebookMeaning(primary string, supplements []notebookSupplement) string {
	parts := []string{strings.TrimSpace(primary)}
	for _, supplement := range supplements {
		parts = append(parts, supplement.line())
	}
	return strings.Join(parts, " ")
}

// mergeNotebookSupplement 把一条来自非记录者的说法并进现有正文，返回合并后的正文；
// 说法和主释义或已有补充完全一样时原样返回，表示不需要改动。
//
// 同一个人再说一次只更新他自己那条；条数和总长度超限时先丢最老的补充说法，
// 主释义永远保留。
func mergeNotebookSupplement(existing string, editorName string, text string, now time.Time) string {
	primary, supplements := splitNotebookMeaning(existing)
	text = strings.TrimSpace(text)
	editorName = strings.TrimSpace(editorName)
	if editorName == "" {
		editorName = "群友"
	}
	if strings.EqualFold(strings.TrimSpace(primary), text) {
		return existing
	}
	kept := supplements[:0:0]
	for _, supplement := range supplements {
		if supplement.Editor == editorName {
			if strings.EqualFold(supplement.Text, text) {
				return existing
			}
			continue
		}
		if strings.EqualFold(supplement.Text, text) {
			// 别人已经这么说过：不重复记，第二个人的认同体现在原条目上就够了。
			return existing
		}
		kept = append(kept, supplement)
	}
	kept = append(kept, notebookSupplement{Editor: editorName, Date: now.Format("2006-01-02"), Text: text})
	for len(kept) > notebookMaxSupplements {
		kept = kept[1:]
	}
	merged := joinNotebookMeaning(primary, kept)
	for len([]rune(merged)) > NotebookContentMaxRunes && len(kept) > 1 {
		kept = kept[1:]
		merged = joinNotebookMeaning(primary, kept)
	}
	return TruncateNotebookText(merged, NotebookContentMaxRunes)
}

// mergeNotebookAliases 取并集，保留原有顺序。
func mergeNotebookAliases(term string, existing []string, added []string) []string {
	return NormalizeNotebookAliases(term, append(append([]string(nil), existing...), added...))
}

// notebookContext 拿当前消息去撞笔记本，把命中的条目组装成提示词段落。
// 没有存储层、没命中、或者查询失败时一律返回空串：笔记本是增强，不是前置依赖。
func (r *Runtime) notebookContext(ctx context.Context, event MessageEvent, queryText string) string {
	return r.notebookContextMatched(ctx, event, queryText, true)
}

func (r *Runtime) notebookContextForRouting(ctx context.Context, event MessageEvent, queryText string) string {
	return r.notebookContextMatched(ctx, event, queryText, false)
}

func (r *Runtime) notebookContextMatched(ctx context.Context, event MessageEvent, queryText string, recordUsage bool) string {
	store := r.notebookStore()
	if store == nil {
		return ""
	}
	text := strings.TrimSpace(memoryRetrievalText(event, queryText))
	if text == "" {
		return ""
	}
	lookupCtx, cancel := context.WithTimeout(ctx, notebookLookupTimeout)
	entries, err := store.LookupNotebookEntries(lookupCtx, NotebookQuery{
		ScopeKeys: notebookScopeKeys(event),
		Text:      text,
		Limit:     notebookContextLimit,
		Now:       time.Now(),
	})
	cancel()
	if err != nil {
		log.Printf("diana notebook lookup failed: %v", err)
		return ""
	}
	if len(entries) == 0 {
		return ""
	}
	if recordUsage {
		r.touchNotebookEntries(ctx, store, entries)
	}
	return formatNotebookContext(entries)
}

func (r *Runtime) notebookStore() NotebookStore {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.notebook
}

// touchNotebookEntries 异步回写命中。和记忆那边一样，回写失败最多让冷热排序失真。
func (r *Runtime) touchNotebookEntries(ctx context.Context, store NotebookStore, entries []NotebookEntry) {
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
		touchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), notebookLookupTimeout)
		defer cancel()
		if err := store.TouchNotebookEntries(touchCtx, ids, now); err != nil {
			log.Printf("diana notebook touch failed: %v", err)
		}
	}()
}

// notebookContextPrefix 明说这段是「拿来理解，不是拿来复述」。少了这句，模型会
// 把命中的条目当成用户在问词义，回一段解释。
const notebookContextPrefix = "【笔记命中，仅用于理解当前消息】\n" +
	"按这些笔记理解对应的说法和背景；不要复述笔记，不要解释你查过笔记，也不要因为命中就把话题转到这条笔记上。笔记和当前语境明显对不上时以语境为准。\n"

func formatNotebookContext(entries []NotebookEntry) string {
	var builder strings.Builder
	builder.WriteString(notebookContextPrefix)
	used := 0
	for _, entry := range entries {
		line := "- " + formatNotebookLine(entry)
		if used+len(line) > notebookContextBudget && used > 0 {
			break
		}
		used += len(line)
		builder.WriteString(line)
		builder.WriteString("\n")
	}
	return strings.TrimRight(builder.String(), "\n")
}

// formatNotebookLine 把一条笔记写成提示词里的一行。
//
// 行首标出类型：同样一句「十点后不刷屏」，作为「事实」是群规，作为「待办」是还没
// 做的事，作为「词条」是个梗——不标类型，模型只能靠猜，而这三种猜错的后果不一样。
// 词条不标：它本来就是「这个词是什么意思」，标了反而多余。
//
// 触发词只在词条上写成「又作」——那确实是别名；其余类型的触发词是检索用的钩子，
// 不是这条笔记的另一个名字，写进提示词只会让模型以为它们是同义词。
func formatNotebookLine(entry NotebookEntry) string {
	var builder strings.Builder
	kind := NormalizeNotebookKind(string(entry.Kind))
	if kind != NotebookKindTerm {
		builder.WriteString("[" + kind.Label() + "] ")
	}
	builder.WriteString(entry.Term)
	if kind == NotebookKindTerm && len(entry.Aliases) > 0 {
		builder.WriteString("（又作 " + strings.Join(entry.Aliases, "、") + "）")
	}
	builder.WriteString("：")
	builder.WriteString(entry.Meaning)
	if example := strings.TrimSpace(entry.Example); example != "" {
		builder.WriteString("　例：" + example)
	}
	return builder.String()
}

// SortNotebookEntriesByScope 给命中结果定序：作用域优先级 > 使用次数 > 更新时间。
// 存储层已经排过一遍，这里保证纯内存实现（测试用）和 SQLite 表现一致。
func SortNotebookEntriesByScope(entries []NotebookEntry, scopeKeys []string) {
	priority := make(map[string]int, len(scopeKeys))
	for index, scope := range scopeKeys {
		priority[scope] = index
	}
	rank := func(entry NotebookEntry) int {
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
