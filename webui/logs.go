// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/SuInk/diana/internal/secretmask"
	"github.com/SuInk/diana/model/llm"
	"github.com/SuInk/diana/model/storage"

	"github.com/gin-gonic/gin"
)

type AppLogStore interface {
	AppendLog(context.Context, storage.AppLogEntry) error
	ListLogs(context.Context, storage.AppLogFilter) ([]storage.AppLogEntry, error)
}

// AppLogWriter 只暴露写入能力，避免只记录审计事件的 handler 依赖日志查询接口。
type AppLogWriter interface {
	AppendLog(context.Context, storage.AppLogEntry) error
}

type AppLogHandler struct {
	store      AppLogStore
	groupNames func(context.Context) map[string]string
}

// appLogActorNamer 是日志存储的可选能力：把账号换成昵称。测试里的内存实现没有它，
// 拿不到就照旧只显示账号。
type appLogActorNamer interface {
	ResolveUserDisplayNames(ctx context.Context, userIDs []string) (map[string]string, error)
}

// appLogGroupMemberNamer 是日志存储的可选能力：取某人在某个群里的群名片。
type appLogGroupMemberNamer interface {
	ResolveGroupMemberNames(ctx context.Context, refs []storage.GroupMemberRef) (map[storage.GroupMemberRef]string, error)
}

// appLogEntry 在存储行之外补上能认出人和群的名字。日志里的 actor 形如 "qq:30001"、
// 群号藏在 metadata 里，光一串号码认不出是谁、在哪个群——和事件列表是同一个问题。
type appLogEntry struct {
	storage.AppLogEntry
	ActorName string `json:"actor_name,omitempty"`
	// ActorCard 是 actor 在这条日志所在群里的群名片，和 ActorName（跨群的名字）分开给，
	// 同一个人在不同群里叫法不一样。
	ActorCard string `json:"actor_card,omitempty"`
	GroupID   string `json:"group_id,omitempty"`
	GroupName string `json:"group_name,omitempty"`
}

type appLogsResponse struct {
	Logs []appLogEntry `json:"logs"`
	// NextCursor 非空表示还有更早的记录，原样放进下一次请求的 before 参数。
	NextCursor string `json:"next_cursor,omitempty"`
}

// NewAppLogHandler 创建 AppLogHandler 实例。
func NewAppLogHandler(store AppLogStore) *AppLogHandler {
	return &AppLogHandler{store: store}
}

// WithGroupNames 接上群号到群名的查询。QQ 的消息事件里不带群名，只能问机器人的实时群列表，
// 这份能力在 BotHandler 那边，所以由装配处传进来；不传就只显示群号。
func (h *AppLogHandler) WithGroupNames(lookup func(context.Context) map[string]string) *AppLogHandler {
	h.groupNames = lookup
	return h
}

// Register 注册当前模块的路由或能力。
func (h *AppLogHandler) Register(router gin.IRouter) {
	router.GET("/api/logs", h.list)
}

// list 封装当前模块的 list 逻辑。
// llmCallLogActions 是「LLM 调用」页签的两种记录。它们每次模型调用都写一条，量比别的
// 日志大一两个数量级，混在「全部」「操作」「错误」里会把真正的记录挤出第一页，所以
// 只在自己的页签里出现。
var llmCallLogActions = []string{"llm_usage", "llm_call_failed"}

func (h *AppLogHandler) list(c *gin.Context) {
	kind := storage.AppLogKind(strings.TrimSpace(c.Query("kind")))
	// all 是日志页的「全部」：操作和错误合在一起按时间排。调试追踪不算在内——它是
	// 按需打开的完整调用链，体量大，混进来会把真正的记录冲掉。
	var kinds []storage.AppLogKind
	var actions, excluded []string
	var metadata map[string]string
	switch kind {
	case "", storage.LogKindOperation, storage.LogKindError:
	case "all":
		kind = ""
		kinds = []storage.AppLogKind{storage.LogKindOperation, storage.LogKindError}
	case "llm":
		kind = ""
		// 「LLM 调用」能按结果、模型和用途筛：只看失败的、只看某个模型的。
		switch status := strings.TrimSpace(c.Query("status")); status {
		case "":
			actions = append(actions, llmCallLogActions...)
		case "ok":
			actions = []string{"llm_usage"}
		case "failed":
			actions = []string{"llm_call_failed"}
		default:
			writeError(c, http.StatusBadRequest, fmt.Errorf("unsupported llm call status %q", status))
			return
		}
		for _, key := range []string{"model", "purpose"} {
			if value := strings.TrimSpace(c.Query(key)); value != "" {
				if metadata == nil {
					metadata = map[string]string{}
				}
				metadata[key] = value
			}
		}
	default:
		writeError(c, http.StatusBadRequest, fmt.Errorf("unsupported log kind %q", kind))
		return
	}
	limit, err := parseLogLimit(c.Query("limit"))
	if err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	beforeTime, beforeID, err := parseLogCursor(c.Query("before"))
	if err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	since, err := parseLogTime(c.Query("since"))
	if err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	until, err := parseLogTime(c.Query("until"))
	if err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	// 日志存储不可用时返回空列表，前端日志页仍可正常打开。
	if h.store == nil {
		c.JSON(http.StatusOK, appLogsResponse{Logs: []appLogEntry{}})
		return
	}
	if len(actions) == 0 {
		for _, action := range strings.Split(c.Query("action"), ",") {
			if action = strings.TrimSpace(action); action != "" {
				actions = append(actions, action)
			}
		}
		if len(actions) == 0 && c.Query("kind") != "" {
			excluded = llmCallLogActions
		}
	}
	filter := storage.AppLogFilter{
		Kind:           kind,
		Kinds:          kinds,
		Actions:        actions,
		ExcludeActions: excluded,
		ProfileID:      strings.TrimSpace(c.Query("profile")),
		Metadata:       metadata,
		Since:          since,
		Until:          until,
		BeforeTime:     beforeTime,
		BeforeID:       beforeID,
	}
	if query := strings.TrimSpace(c.Query("q")); query != "" {
		filter.Query = query
		filter.QueryActors, filter.QueryGroupIDs = h.resolveLogQueryNames(c.Request.Context(), query)
	}
	// 多取一条判断还有没有下一页，返回时去掉。
	pageSize := normalizedLogPageSize(limit)
	filter.Limit = pageSize + 1
	logs, err := h.store.ListLogs(c.Request.Context(), filter)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	response := appLogsResponse{}
	if len(logs) > pageSize {
		logs = logs[:pageSize]
		last := logs[len(logs)-1]
		response.NextCursor = formatLogCursor(last.CreatedAt, last.ID)
	}
	response.Logs = h.namedAppLogs(c.Request.Context(), logs)
	c.JSON(http.StatusOK, response)
}

// normalizedLogPageSize 和存储层的上限保持一致；多取的那一条不能被存储层截掉。
func normalizedLogPageSize(limit int) int {
	switch {
	case limit <= 0:
		return 100
	case limit >= 500:
		return 499
	}
	return limit
}

// 游标是「最后一条的时间|ID」：时间相同的多条靠 ID 接着往下翻，不会漏也不会重复。
func formatLogCursor(at time.Time, id string) string {
	return at.UTC().Format(time.RFC3339Nano) + "|" + id
}

func parseLogCursor(raw string) (time.Time, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, "", nil
	}
	stamp, id, _ := strings.Cut(raw, "|")
	at, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("invalid log cursor")
	}
	return at, id, nil
}

// parseLogTime 解析日期筛选的边界。前端按本地日期换算好再传 RFC3339，这里不猜时区。
func parseLogTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	at, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid log time %q", raw)
	}
	return at, nil
}

// appLogUserFinder 是日志存储的可选能力：按昵称反查账号，让搜索框搜昵称也能
// 翻到更早的记录，而不只是在已经加载的那一页里找。
type appLogUserFinder interface {
	FindUserIDsByDisplayName(ctx context.Context, query string, limit int) ([]string, error)
}

// resolveLogQueryNames 把搜索词按昵称和群名反查成账号、群号。日志行里只存号码，
// 页面上显示的却是名字，不反查的话照着屏幕上的名字搜会什么都搜不到。
func (h *AppLogHandler) resolveLogQueryNames(ctx context.Context, query string) ([]string, []string) {
	keyword := strings.ToLower(strings.TrimSpace(query))
	var actors, groups []string
	if finder, ok := h.store.(appLogUserFinder); ok {
		ids, err := finder.FindUserIDsByDisplayName(ctx, keyword, 50)
		if err != nil {
			log.Printf("find app log actors by name failed: %v", err)
		}
		actors = ids
	}
	if h.groupNames != nil {
		for groupID, name := range h.groupNames(ctx) {
			if strings.Contains(strings.ToLower(name), keyword) {
				groups = append(groups, groupID)
				if len(groups) >= 50 {
					break
				}
			}
		}
	}
	return actors, groups
}

// namedAppLogs 给日志行补上 actor 的昵称、群名片和群名。一次查完整页，不逐行查库。
// 查不到名字、或者存储不支持这次查询，都只是少一个名字，日志照常返回。
func (h *AppLogHandler) namedAppLogs(ctx context.Context, logs []storage.AppLogEntry) []appLogEntry {
	entries := make([]appLogEntry, 0, len(logs))
	userIDs := make([]string, 0, len(logs))
	members := make([]storage.GroupMemberRef, 0, len(logs))
	for _, entry := range logs {
		item := appLogEntry{AppLogEntry: entry, GroupID: appLogGroupID(entry.Metadata)}
		if userID := appLogActorUserID(entry.Actor); userID != "" {
			userIDs = append(userIDs, userID)
			if item.GroupID != "" {
				members = append(members, storage.GroupMemberRef{GroupID: item.GroupID, UserID: userID})
			}
		}
		entries = append(entries, item)
	}
	var names map[string]string
	if namer, ok := h.store.(appLogActorNamer); ok && len(userIDs) > 0 {
		var err error
		if names, err = namer.ResolveUserDisplayNames(ctx, userIDs); err != nil {
			log.Printf("resolve app log actor names failed: %v", err)
		}
	}
	var cards map[storage.GroupMemberRef]string
	if namer, ok := h.store.(appLogGroupMemberNamer); ok && len(members) > 0 {
		var err error
		if cards, err = namer.ResolveGroupMemberNames(ctx, members); err != nil {
			log.Printf("resolve app log group member names failed: %v", err)
		}
	}
	var groupNames map[string]string
	if h.groupNames != nil {
		for _, entry := range entries {
			if entry.GroupID != "" {
				groupNames = h.groupNames(ctx)
				break
			}
		}
	}
	for index := range entries {
		entry := &entries[index]
		userID := appLogActorUserID(entry.Actor)
		entry.ActorName = names[userID]
		entry.ActorCard = cards[storage.GroupMemberRef{GroupID: entry.GroupID, UserID: userID}]
		entry.GroupName = groupNames[entry.GroupID]
	}
	return entries
}

// appLogGroupID 取日志 metadata 里的群号。各处写日志时群号都放在 group_id 下，
// 私聊和后台任务没有群号，这里就是空。
func appLogGroupID(metadata map[string]any) string {
	switch value := metadata["group_id"].(type) {
	case string:
		return strings.TrimSpace(value)
	case float64:
		if value > 0 {
			return strconv.FormatInt(int64(value), 10)
		}
	}
	return ""
}

// appLogActorUserID 从 actor 里取出聊天账号。写法有两种：带命名空间的 "qq:123456"
// 和早先直接写账号的 "123456"。控制台操作者的 actor 不是纯数字，不会被误认。
func appLogActorUserID(actor string) string {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return ""
	}
	if _, rest, found := strings.Cut(actor, ":"); found {
		actor = strings.TrimSpace(rest)
	}
	if actor == "" {
		return ""
	}
	for _, char := range actor {
		if char < '0' || char > '9' {
			return ""
		}
	}
	return actor
}

// parseLogLimit 解析日志列表接口的 limit 参数。
func parseLogLimit(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 0 {
		return 0, fmt.Errorf("limit must be a non-negative integer")
	}
	return limit, nil
}

// recordOperation 写入一条后台操作日志。
func recordOperation(ctx context.Context, logger AppLogWriter, action string, message string, target string, metadata map[string]any) {
	recordAppLog(ctx, logger, storage.AppLogEntry{
		Kind:     storage.LogKindOperation,
		Level:    storage.LogLevelInfo,
		Action:   action,
		Message:  message,
		Target:   target,
		Metadata: metadata,
	})
}

// recordRequestOperation 写入带 HTTP 操作者信息的操作日志。
func recordRequestOperation(c *gin.Context, logger AppLogWriter, action string, message string, target string, metadata map[string]any) {
	recordAppLog(c.Request.Context(), logger, storage.AppLogEntry{
		Kind:     storage.LogKindOperation,
		Level:    storage.LogLevelInfo,
		Action:   action,
		Message:  message,
		Actor:    requestActor(c),
		Target:   target,
		Metadata: metadata,
	})
}

// recordError 用于没有 HTTP 请求上下文的后台任务，例如保存运行态失败。
func recordError(ctx context.Context, logger AppLogWriter, action string, err error, target string, metadata map[string]any) {
	if err == nil {
		return
	}
	recordAppLog(ctx, logger, storage.AppLogEntry{
		Kind:     storage.LogKindError,
		Level:    storage.LogLevelError,
		Action:   action,
		Message:  err.Error(),
		Detail:   err.Error(),
		Target:   target,
		Metadata: metadata,
	})
}

// statusUpstreamFailed 是 WebUI 接口「后端正常，但它依赖的上游失败了」时的状态码。
//
// 以前这类错误回 502/504。这两个码在反向代理眼里就是「网关自己的错」：Cloudflare
// 会把源站的 502/504 换成自己的错误页，nginx 开了 proxy_intercept_errors 也会套
// error_page。后端写好的原因（上游地址、状态码、响应片段）就这样被整段丢掉，用户
// 只看到「后端出错（HTTP 502）」。424 表示「依赖的请求失败」，代理不会改写它的正文。
//
// 对外的 Open API 仍回 502：外部客户端按网关错误重试，是公开契约的一部分。
const statusUpstreamFailed = http.StatusFailedDependency

// logAndWriteError 记录接口错误并返回 HTTP 错误响应。
func logAndWriteError(c *gin.Context, logger AppLogWriter, status int, action string, err error, target string, metadata map[string]any) {
	if err != nil {
		recordAppLog(c.Request.Context(), logger, storage.AppLogEntry{
			Kind:     storage.LogKindError,
			Level:    storage.LogLevelError,
			Action:   action,
			Message:  err.Error(),
			Detail:   err.Error(),
			Actor:    requestActor(c),
			Target:   target,
			Metadata: metadata,
		})
	}
	writeError(c, status, err)
}

// 日志写入失败不能影响原始业务请求，失败时只写普通运行日志便于排查。
func recordAppLog(ctx context.Context, logger AppLogWriter, entry storage.AppLogEntry) {
	if logger == nil {
		return
	}
	if enabled, _ := ctx.Value(consoleConfigContextKey{}).(bool); enabled || secretmask.IsChatConfig(ctx) {
		entry.Message = secretmask.ChatConfigText(entry.Message)
		entry.Detail = secretmask.ChatConfigText(entry.Detail)
		entry.Target = secretmask.ChatConfigText(entry.Target)
		if entry.Metadata != nil {
			entry.Metadata = secretmask.ChatConfigValue(entry.Metadata).(map[string]any)
		}
	}
	if err := logger.AppendLog(ctx, entry); err != nil {
		log.Printf("app log skipped: %v", err)
	}
}

// 优先使用网关传入的操作者身份；本地直连时退回到客户端 IP，日志里仍能看到操作人。
func requestActor(c *gin.Context) string {
	for _, header := range []string{
		"X-Diana-Actor",
		"X-Operator",
		"X-Forwarded-User",
		"X-Remote-User",
		"X-User",
		"X-User-ID",
	} {
		if value := strings.TrimSpace(c.GetHeader(header)); value != "" {
			return value
		}
	}
	if ip := strings.TrimSpace(c.ClientIP()); ip != "" {
		return "web:" + ip
	}
	return "web:unknown"
}

// recordLLMUsage 给控制台里直接发起的模型调用（提供商测试、生图测试、AI 生成人设）
// 记一条用量。这些调用不经过机器人运行时的装饰链，不在这里记就不进 token 统计；
// 字段和运行时写的 llm_usage 保持一致，统计那边不用区分来源。
func recordLLMUsage(c *gin.Context, logger AppLogWriter, provider llm.Provider, model string, usage llm.Usage, purpose string, duration time.Duration) {
	if usage.TotalTokens <= 0 && (usage.InputTokens > 0 || usage.OutputTokens > 0) {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	}
	metadata := map[string]any{
		"provider":            string(provider),
		"model":               strings.TrimSpace(model),
		"purpose":             purpose,
		"input_tokens":        usage.InputTokens,
		"output_tokens":       usage.OutputTokens,
		"total_tokens":        usage.TotalTokens,
		"cached_input_tokens": usage.CachedInputTokens,
		"duration_ms":         duration.Milliseconds(),
	}
	if usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.TotalTokens == 0 {
		metadata["usage_missing"] = true
	}
	recordAppLog(c.Request.Context(), logger, storage.AppLogEntry{
		Kind:     storage.LogKindOperation,
		Level:    storage.LogLevelInfo,
		Action:   "llm_usage",
		Message:  "LLM 调用用量已记录",
		Actor:    requestActor(c),
		Metadata: metadata,
	})
}
