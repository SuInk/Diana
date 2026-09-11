// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/SuInk/diana/model/applog"
)

const (
	dianaRepositoryIssuesToolName = "diana.repository_issues"
	repositoryIssueBodyLimit      = 60_000
	repositoryIssueTitleLimit     = 256
	// repositoryIssueConfirmationCodeLength 是确认码取草稿 ID 前缀的长度。
	// 6 位十六进制既短到能手打，又不可能在正常聊天里被无意打出来。
	repositoryIssueConfirmationCodeLength = 6
	repositoryIssueCommentLimit           = 60_000
	repositoryIssueListLimit              = 100
	repositoryIssueRecentWindow           = 90 * 24 * time.Hour
	repositoryIssueCommentMaxPages        = 100
	repositoryIssueListMaxPages           = 10
	repositoryIssueConfirmationTTL        = 15 * time.Minute
	repositoryIssueResponseLimit          = 16 << 20
	repositoryIssueCredentialKey          = `(?:[a-z0-9]+[_-])*(?:authorization|api[_-]?key|access[_-]?key|access[_-]?token|refresh[_-]?token|client[_-]?secret|private[_-]?key|token|secret|password|passwd)(?:[_-][a-z0-9]+)*`
)

var (
	repositoryIssueCreateMarkerPattern          = regexp.MustCompile(`<!--\s*diana-operation:create:([a-f0-9]{64})(?::[a-f0-9]{64})?\s*-->`)
	repositoryIssueAnyMarkerPattern             = regexp.MustCompile(`<!--\s*diana-operation:[a-z_]+:[a-f0-9]{64}(?::[a-f0-9]{64})?\s*-->`)
	repositoryIssueCQPattern                    = regexp.MustCompile(`(?i)\[CQ:[^\]]+\]`)
	repositoryIssueGitHubTokenPattern           = regexp.MustCompile(`\b(?:gh[pousr]_|github_pat_)[A-Za-z0-9_]{20,}\b`)
	repositoryIssueQuotedCredentialPattern      = regexp.MustCompile(`(?i)["'](` + repositoryIssueCredentialKey + `)["']\s*:\s*["'][^"'\r\n]+["']`)
	repositoryIssueQuotedValueCredentialPattern = regexp.MustCompile(`(?i)(` + repositoryIssueCredentialKey + `)\s*[:=]\s*["'][^"'\r\n]+["']`)
	repositoryIssueAuthorizationPattern         = regexp.MustCompile(`(?i)\bauthorization\b\s*[:=]\s*(?:(?:bearer|token|basic)\s+)?[^\s,;]+`)
	repositoryIssueCredentialPattern            = regexp.MustCompile(`(?i)(` + repositoryIssueCredentialKey + `)\s*[:=]\s*[^\s,;]+`)
	repositoryIssueBearerPattern                = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{12,}`)
	repositoryIssuePrivateKeyPattern            = regexp.MustCompile(`(?s)-----BEGIN [^-\r\n]{0,40}PRIVATE KEY-----.*?-----END [^-\r\n]{0,40}PRIVATE KEY-----`)
	repositoryIssueEmailPattern                 = regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`)
	repositoryIssuePhonePattern                 = regexp.MustCompile(`(?:\+?86[\s-]?)?1[3-9](?:[\s-]?[0-9]){9}`)
	repositoryIssueIPv4Pattern                  = regexp.MustCompile(`\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b`)
	repositoryIssueRuntimeIDPattern             = regexp.MustCompile(`(?i)\b(user_id|group_id|message_id|self_id|qq|uin)\b\s*[:=]\s*["']?[A-Za-z0-9_-]{4,}["']?`)
	repositoryIssueUUIDPattern                  = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\b`)
	repositoryIssueCommonTokenPattern           = regexp.MustCompile(`\b(?:sk-(?:proj-|svcacct-)?[A-Za-z0-9_-]{16,}|(?:AKIA|ASIA)[A-Z0-9]{16}|xox[baprs]-[A-Za-z0-9-]{10,}|AIza[0-9A-Za-z_-]{30,}|npm_[A-Za-z0-9]{30,}|eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,})\b`)
	repositoryIssueURLPattern                   = regexp.MustCompile(`https?://[^\s<>"']+`)
	repositoryIssuePlainRepositoryPattern       = regexp.MustCompile(`(?i)(?:^|[^a-z0-9_./-])([a-z0-9_.-]*[a-z0-9_-]/[a-z0-9_.-]*[a-z0-9_-])(?:$|\.(?:$|\s)|[^a-z0-9_./-])`)
	repositoryIssueGitHubRepositoryPattern      = regexp.MustCompile(`(?i)(?:^|[^a-z0-9_./-])(?:https?://)?github\.com/([a-z0-9_.-]*[a-z0-9_-]/[a-z0-9_.-]*[a-z0-9_-])(?:\.git)?(?:/issues(?:/[0-9]+)?/?)?(?:$|\.(?:$|\s)|[^a-z0-9_./-])`)
	repositoryIssueNumberMentionPattern         = regexp.MustCompile(`(?i)(?:#\s*|issues?\s*(?:#|/)?\s*|工单\s*#?\s*|议题\s*#?\s*)([0-9]+)`)
	repositoryIssueSearchQualifierPattern       = regexp.MustCompile(`(?i)(?:^|[^a-z0-9_])-?(?:repo|org|user|is|state|type|in|label|assignee|author|mentions|milestone|comments|created|updated|closed|no|language|archived|draft|linked|sort):`)
	repositoryIssueSearchBooleanPattern         = regexp.MustCompile(`(?:^|[^A-Za-z0-9_])(?:AND|OR|NOT)(?:[^A-Za-z0-9_]|$)`)
)

type dianaRepositoryIssuesTool struct {
	runtime  *Runtime
	event    MessageEvent
	plugin   *RepositoryPublishPlugin
	settings SettingValues
	// credentialSource 记下本次请求实际用了哪种凭据，只用于把 404 之类的报错说清楚，
	// 不含 Token 本身。
	credentialSource string
}

type repositoryIssueResult struct {
	OK              bool                     `json:"ok"`
	Operation       string                   `json:"operation"`
	Outcome         string                   `json:"outcome,omitempty"`
	Repository      string                   `json:"repository,omitempty"`
	RequestedNumber int                      `json:"requested_number,omitempty"`
	FailureCode     string                   `json:"failure_code,omitempty"`
	Message         string                   `json:"message"`
	Issue           *repositoryIssueSummary  `json:"issue,omitempty"`
	Items           []repositoryIssueSummary `json:"items,omitempty"`
	// RequestedNumbers 和 Failures 只在批量写入（numbers）时出现：Items 是成功的那些，
	// Failures 逐条说明哪个编号为什么没写上。
	RequestedNumbers []int                         `json:"requested_numbers,omitempty"`
	Failures         []repositoryIssueBatchFailure `json:"failures,omitempty"`
	CommentURL       string                        `json:"comment_url,omitempty"`
	// IssueBody 和 Comments 只在 get 返回：update 是整段覆盖，模型不先看一眼原文
	// 就没法安全地改；以前没有任何操作能把正文和评论读回来。
	IssueBody            string                       `json:"issue_body,omitempty"`
	IssueBodyTruncated   bool                         `json:"issue_body_truncated,omitempty"`
	Comments             []repositoryIssueCommentView `json:"comments,omitempty"`
	CommentsTruncated    bool                         `json:"comments_truncated,omitempty"`
	Fingerprint          string                       `json:"fingerprint,omitempty"`
	Idempotent           bool                         `json:"idempotent,omitempty"`
	Reconciled           bool                         `json:"reconciled,omitempty"`
	RequiresConfirmation bool                         `json:"requires_confirmation,omitempty"`
	ConfirmationToken    string                       `json:"confirmation_token,omitempty"`
	RequiresApproval     bool                         `json:"requires_approval,omitempty"`
	Draft                *repositoryIssueDraftView    `json:"draft,omitempty"`
	Drafts               []repositoryIssueDraftView   `json:"drafts,omitempty"`
	Redactions           int                          `json:"redactions,omitempty"`
}

type RepositoryIssueDraft struct {
	ID         string `json:"id"`
	Platform   string `json:"platform,omitempty"`
	ProfileID  string `json:"profile_id,omitempty"`
	GroupID    string `json:"group_id"`
	Repository string `json:"repository"`
	// Operation 记录这份草稿最终要执行的写操作。以前草稿只用于 create，其余写操作
	// 靠比对用户措辞放行；措辞判断已经移除，所有写操作统一走草稿加确认码。
	Operation     string         `json:"operation,omitempty"`
	RequesterID   string         `json:"requester_id"`
	RequesterName string         `json:"requester_name,omitempty"`
	Input         map[string]any `json:"input"`
	Status        string         `json:"status"`
	IssueNumber   int            `json:"issue_number,omitempty"`
	IssueURL      string         `json:"issue_url,omitempty"`
	ResolvedBy    string         `json:"resolved_by,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

type repositoryIssueDraft = RepositoryIssueDraft

type RepositoryIssueDraftStore interface {
	SaveRepositoryIssueDraft(context.Context, RepositoryIssueDraft) error
	RepositoryIssueDraft(context.Context, string) (RepositoryIssueDraft, bool, error)
	ListRepositoryIssueDrafts(context.Context, string, string) ([]RepositoryIssueDraft, error)
}

// repositoryIssueDraftView 是交给模型转述给用户看的草稿内容。
//
// 确认码要成立，用户必须看得到「确认之后到底会写什么」，所以这里列出全部会进入
// 请求体的字段——以前 assignees、milestone 和目标 Issue 编号靠比对用户措辞来防止
// 模型夹带，那层措辞判断已经移除，改由这份清单让用户自己核对。
type repositoryIssueDraftView struct {
	ID string `json:"id"`
	// Operation 区分这份草稿要执行的写操作。历史草稿没有这个字段，读取时按
	// create 处理。
	Operation   string `json:"operation,omitempty"`
	IssueTarget int    `json:"issue_target,omitempty"`
	// IssueTargets 是批量草稿要改的全部编号；单个目标时只有 IssueTarget。
	IssueTargets []int  `json:"issue_targets,omitempty"`
	GroupID      string `json:"group_id"`
	Repository   string `json:"repository"`
	Title        string `json:"title"`
	Body         string `json:"body,omitempty"`
	// AppendBody 是 update 草稿里「追加到正文末尾」的那段，和整段覆盖的 Body 分开。
	AppendBody    string   `json:"append_body,omitempty"`
	Labels        []string `json:"labels,omitempty"`
	Assignees     []string `json:"assignees,omitempty"`
	Milestone     any      `json:"milestone,omitempty"`
	State         string   `json:"state,omitempty"`
	RequesterID   string   `json:"requester_id"`
	RequesterName string   `json:"requester_name,omitempty"`
	Status        string   `json:"status"`
	// ConfirmationCode 是这份草稿的确认码，只对还等着审批的草稿给。草稿列表原先只
	// 有 id，模型要自己去截前六位才能报出确认码——那是让它做字符串运算，不可靠；
	// 报错了用户照着打也过不了校验。直接给出来。
	ConfirmationCode string    `json:"confirmation_code,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	IssueNumber      int       `json:"issue_number,omitempty"`
	IssueURL         string    `json:"issue_url,omitempty"`
}

type repositoryIssueSummary struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	State     string    `json:"state"`
	URL       string    `json:"url"`
	Labels    []string  `json:"labels,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type githubRepositoryIssue struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	State       string    `json:"state"`
	HTMLURL     string    `json:"html_url"`
	UpdatedAt   time.Time `json:"updated_at"`
	ClosedAt    time.Time `json:"closed_at"`
	PullRequest *struct{} `json:"pull_request,omitempty"`
	Labels      []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

type githubIssueComment struct {
	Body      string    `json:"body"`
	HTMLURL   string    `json:"html_url"`
	CreatedAt time.Time `json:"created_at,omitempty"`
	User      *struct {
		Login string `json:"login"`
	} `json:"user,omitempty"`
}

// repositoryIssueBatchFailure 记录批量写入里没成功的那一条。
type repositoryIssueBatchFailure struct {
	Number      int    `json:"number"`
	FailureCode string `json:"failure_code"`
	Message     string `json:"message"`
}

// repositoryIssueCommentView 是 get 返回给模型看的评论：去掉了运行时对账用的
// 隐藏标记，正文按上限截断。
type repositoryIssueCommentView struct {
	Author    string    `json:"author,omitempty"`
	Body      string    `json:"body"`
	Truncated bool      `json:"truncated,omitempty"`
	URL       string    `json:"url,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

type repositoryIssueAPIError struct {
	Code      string
	Status    int
	Uncertain bool
}

type repositoryIssueMarkerMatch int

const (
	repositoryIssueMarkerMissing repositoryIssueMarkerMatch = iota
	repositoryIssueMarkerExact
	repositoryIssueMarkerConflict
)

func (e *repositoryIssueAPIError) Error() string {
	if e == nil {
		return ""
	}
	return e.Code
}

func newDianaRepositoryIssuesTool(runtime *Runtime, event MessageEvent, plugin *RepositoryPublishPlugin, settings SettingValues) *dianaRepositoryIssuesTool {
	return &dianaRepositoryIssuesTool{runtime: runtime, event: event, plugin: plugin, settings: settings}
}

func (t *dianaRepositoryIssuesTool) Name() string {
	return dianaRepositoryIssuesToolName
}

func (t *dianaRepositoryIssuesTool) Description() string {
	description := `搜索和管理 GitHub Issues。search 按关键词找，get 读回某个 Issue 的标题、正文和最近评论——要改已有 Issue 之前先 get，update 的 body 是整段覆盖，只想补几句就用 append_body（追加到正文末尾，原文不动）。要对多个 Issue 做同一件事（同样的评论、同样的追加、一起关闭）时用 numbers 一次传全部编号，只需要一份草稿和一个确认码。create 和 comment 的内容由你根据当前需求整理；只有用户在消息里逐字写出内容时才会立即写入 GitHub，你自己组织措辞时一律先落成待审批草稿。拿到草稿后把内容复述给用户，并把结果里的 confirmation_code 原样写进你的回复——不写出来对方就无从确认；有权限的人自己打出这个码之后再调用 approve 提交，明确拒绝时调用 cancel_draft；list_drafts 可查看待审批草稿。写操作必须传 user_confirmed_write=true。不得把凭据、运行时 ID 或私密上下文写进 Issue。`
	if t == nil || t.runtime == nil {
		return description
	}
	// 把当前会话能操作的仓库直接写进描述：用户往往只说简称（「给 milksu 提个
	// issue」），模型手里没有清单就只能反问一句完整的 owner/repo，白白多一轮。
	// 这里只需要「是不是主人」，用配置里的 OwnerID 直接比即可；relationshipPolicy
	// 还会去读用户记忆档案，构造工具描述时不值得为此多打一次库。
	isOwner := t.runtime.effectiveConfigForEvent(t.event).IsOwnerEvent(t.event)
	repositories := repositoryPublishEventRepositories(t.event, isOwner, t.settings)
	if len(repositories) == 0 {
		return description + "\n当前会话没有任何已授权仓库，任何 repository 都会被拒绝；应说明尚未授权，不要让用户改用别的写法重试。"
	}
	return description + "\n当前会话可操作的仓库：" + strings.Join(repositories, "、") +
		"。用户只给出仓库简称、别名或链接时，按这份清单匹配后直接填 repository，不要反问完整的 owner/repo；只有确实对不上时才追问。"
}

// InputSchema 声明参数契约。写操作对当前用户消息原文的要求写在 user_confirmed_write
// 的字段说明里——这是最容易踩的一条，放在参数旁边比埋在描述中段更显眼。
func (t *dianaRepositoryIssuesTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("要执行的操作。create 在群聊里由非管理人员发起时会存成草稿，等管理人员 approve 才真正写入。",
			"search", "get", "create", "update", "comment", "close", "reopen", "approve", "cancel_draft", "list_drafts"),
		"repository":  toolStringParam("目标仓库，写成 owner/repo。approve、cancel_draft、list_drafts 不需要。"),
		"number":      toolIntParam("目标 Issue 编号；get 必填，update、comment、close、reopen 单个目标时用它。", 1, 1_000_000),
		"numbers":     toolIntArrayParam("update、comment、close、reopen 的批量目标：对这些 Issue 执行同样的改动，一份草稿、一个确认码；最多 "+itoa(repositoryIssueBatchLimit)+" 个。", 1, 1_000_000),
		"query":       toolStringParam("search 专用：检索关键词。"),
		"title":       toolStringParam("create 必填、update 可选：Issue 标题，最多 " + itoa(repositoryIssueTitleLimit) + " 字符。"),
		"body":        toolStringParam("create 的正文、comment 的评论内容；update 时整段覆盖原正文，最多 " + itoa(repositoryIssueBodyLimit) + " 字符。"),
		"append_body": toolStringParam("update 专用：追加到现有正文末尾的内容，原正文保持不动；给已有 Issue 补充信息（复现版本、补图说明）用它，不要用 body 重写整段。"),
		"labels":      toolStringArrayParam("要设置的标签；传空数组表示清空。"),
		"assignees":   toolStringArrayParam("要设置的负责人；传空数组表示清空。"),
		"milestone":   toolStringParam("要设置的里程碑；传 null 表示清空。"),
		"user_confirmed_write": toolBoolParam("确认当前这条用户消息就是在要求执行这次写入。写操作必填 true。" +
			"写操作不会直接落到 GitHub：create/update/comment/close/reopen 都先存成待审批草稿并返回确认码，" +
			"把草稿内容和确认码复述给用户，等有权限的人原样打出确认码后再用 approve 提交。" +
			"后端不再按用户消息的措辞核对仓库或编号，只认确认码；对多个 Issue 做同样的改动用 numbers 合成一份草稿。"),
		"operation_id":       toolStringParam("幂等标识：同一次写入重试时传相同值，避免重复发布。"),
		"draft_id":           toolStringParam("approve 与 cancel_draft 必填：要审批或取消的草稿 ID，可用 list_drafts 查到。"),
		"confirmation_token": toolStringParam("审批流程返回的确认令牌，按提示原样回传。"),
	})
}

func (t *dianaRepositoryIssuesTool) Run(ctx context.Context, input map[string]any) (string, error) {
	operation := normalizeRepositoryIssueOperation(configToolString(input, "operation"), configToolString(input, "state"))
	result := repositoryIssueResult{Operation: operation, Message: "GitHub Issue 操作未执行。"}
	if operation == "" {
		return t.finish(ctx, result.fail("invalid_operation", "operation 必须是 search、get、create、update、comment、close、reopen、approve、cancel_draft 或 list_drafts。"))
	}
	if t == nil || t.runtime == nil || t.plugin == nil || t.plugin.client == nil {
		return t.finish(ctx, result.fail("plugin_unavailable", "仓库 Issue 发布插件未正确配置。"))
	}
	if operation == "approve" {
		return t.finish(ctx, t.approveDraft(ctx, input))
	}
	if operation == "list_drafts" {
		return t.finish(ctx, t.listDrafts(ctx, input))
	}
	if operation == "cancel_draft" {
		return t.finish(ctx, t.cancelDraft(ctx, input))
	}
	repository, err := normalizeGitHubRepository(configToolString(input, "repository"))
	if err != nil {
		return t.finish(ctx, result.fail("invalid_repository", err.Error()))
	}
	result.Repository = repository
	owner := t.runtime.relationshipPolicy(ctx, t.event).Owner
	userAllowed, groupAllowed, code, message := repositoryPublishAccessForEvent(t.event, repository, owner, t.settings)
	if code != "" {
		return t.finish(ctx, result.fail(code, message))
	}
	if operation == "create" && !userAllowed && groupAllowed {
		return t.finish(ctx, t.createWriteDraft(ctx, repository, operation, input))
	}
	if !userAllowed {
		return t.finish(ctx, result.fail("permission_denied", "当前用户没有该仓库的审批或写入权限。"))
	}
	if operation != "create" && operation != "search" {
		result.RequestedNumber = repositoryIssueNumber(input)
		result.RequestedNumbers = repositoryIssueBatchTargets(input)
	}
	if operation == "search" || operation == "get" {
		if !owner {
			if code, message := t.validateWriteAccess(repository, false); code != "" {
				return t.finish(ctx, result.fail(code, message))
			}
		}
		if operation == "get" {
			return t.finish(ctx, t.get(ctx, repository, input))
		}
		return t.finish(ctx, t.search(ctx, repository, input))
	}
	if code, message := t.validateWriteAccess(repository, owner); code != "" {
		return t.finish(ctx, result.fail(code, message))
	}
	// 写操作一律先落草稿。真正执行只发生在 approve：那里要求用户本人原样打出确认码，
	// 而不是让代码去比对「用户是不是真的这么要求了」——那件事以前靠字段名、否定词和
	// 清空词三组词表来判，属于用关键词判断语义意图。
	return t.finish(ctx, t.createWriteDraft(ctx, repository, operation, input))
}

func normalizeRepositoryIssueOperation(operation, state string) string {
	switch strings.ToLower(strings.TrimSpace(operation)) {
	case "search", "find", "list":
		return "search"
	case "get", "get_issue", "view", "read", "show":
		return "get"
	case "create", "create_issue", "new":
		return "create"
	case "update", "update_issue", "edit":
		return "update"
	case "comment", "comment_issue", "reply":
		return "comment"
	case "close", "closed":
		return "close"
	case "reopen":
		return "reopen"
	case "approve", "approve_draft":
		return "approve"
	case "list_drafts", "drafts", "list_draft":
		return "list_drafts"
	case "cancel_draft", "cancel_draft_issue":
		return "cancel_draft"
	case "set_state":
		switch strings.ToLower(strings.TrimSpace(state)) {
		case "closed", "close":
			return "close"
		case "open", "reopen":
			return "reopen"
		}
	}
	return ""
}

func repositoryPublishAccessForEvent(event MessageEvent, repository string, owner bool, settings SettingValues) (bool, bool, string, string) {
	if owner {
		return true, event.Kind == EventKindGroup, "", ""
	}
	legacyUsers, err := repositoryPublishUserAccess(settings.String(repositoryPublishSettingUserAccess, ""))
	if err != nil {
		return false, false, "invalid_user_repository_access", "用户仓库授权配置无效。"
	}
	legacyGroups, err := repositoryPublishGroupAccess(settings.String(repositoryPublishSettingGroupAccess, ""))
	if err != nil {
		return false, false, "invalid_group_repository_access", "群聊草稿范围配置无效。"
	}
	key := strings.ToLower(repository)
	managerUsers, managerGroups, draftUsers, draftGroups, err := repositoryPublishEffectiveAccess(settings, legacyUsers, legacyGroups)
	if err != nil {
		return false, false, "invalid_repository_access", "Issue 授权配置无效。"
	}
	directAllowed := managerUsers[strings.TrimSpace(event.UserID)][key] || event.Kind == EventKindGroup && managerGroups[strings.TrimSpace(event.GroupID)][key]
	draftAllowed := draftUsers[strings.TrimSpace(event.UserID)][key] || event.Kind == EventKindGroup && draftGroups[strings.TrimSpace(event.GroupID)][key]
	if !directAllowed && !draftAllowed {
		return false, false, "permission_denied", "当前群聊不能为该仓库发起草稿，当前用户也没有该仓库权限。"
	}
	allowed, err := repositoryPublishAllowlist(settings.String(repositoryPublishSettingAllowlist, ""))
	if err != nil {
		return false, false, "invalid_allowlist", "仓库写入白名单配置无效，请使用逗号或换行分隔的精确 owner/repo。"
	}
	if !allowed[key] {
		return false, false, "repository_not_allowed", "目标仓库不在“仓库 Issue 发布”插件的全局白名单中。"
	}
	return directAllowed, draftAllowed, "", ""
}

func repositoryPublishEffectiveAccess(settings SettingValues, legacyUsers, legacyGroups map[string]map[string]bool) (map[string]map[string]bool, map[string]map[string]bool, map[string]map[string]bool, map[string]map[string]bool, error) {
	managerUsers, err := repositoryPublishUserAccess(settings.String(repositoryPublishSettingManagerUsers, ""))
	if err != nil {
		return nil, nil, nil, nil, err
	}
	managerGroups, err := repositoryPublishGroupAccess(settings.String(repositoryPublishSettingManagerGroups, ""))
	if err != nil {
		return nil, nil, nil, nil, err
	}
	draftUsers, err := repositoryPublishUserAccess(settings.String(repositoryPublishSettingDraftUsers, ""))
	if err != nil {
		return nil, nil, nil, nil, err
	}
	draftGroups, err := repositoryPublishGroupAccess(settings.String(repositoryPublishSettingDraftGroups, ""))
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if len(managerUsers) == 0 {
		managerUsers = legacyUsers
	}
	if len(draftGroups) == 0 {
		draftGroups = legacyGroups
	}
	// Managers can always submit a draft as well.
	for id, repos := range managerUsers {
		if draftUsers[id] == nil {
			draftUsers[id] = map[string]bool{}
		}
		for repo := range repos {
			draftUsers[id][repo] = true
		}
	}
	for id, repos := range managerGroups {
		if draftGroups[id] == nil {
			draftGroups[id] = map[string]bool{}
		}
		for repo := range repos {
			draftGroups[id][repo] = true
		}
	}
	return managerUsers, managerGroups, draftUsers, draftGroups, nil
}

func repositoryIssueCurrentRequestText(event MessageEvent) string {
	var builder strings.Builder
	for _, segment := range event.Segments {
		if segment.Type == "text" && segment.Data["source_type"] != "forward" {
			builder.WriteString(segment.Data["text"])
		}
	}
	text := repositoryIssueStripUntrustedContext(builder.String())
	if text != "" || len(event.Segments) > 0 {
		return text
	}
	return repositoryIssueStripUntrustedContext(event.RawMessage)
}

func repositoryIssueStripUntrustedContext(text string) string {
	for _, marker := range []string{"\n\n【被引用的消息】", "\n\n【指代判断选中的历史消息】", "【合并转发 "} {
		if index := strings.Index(text, marker); index >= 0 {
			text = text[:index]
		}
	}
	return strings.TrimSpace(text)
}

func repositoryIssueMentionedNumbers(text string) map[int]bool {
	result := map[int]bool{}
	for _, match := range repositoryIssueNumberMentionPattern.FindAllStringSubmatchIndex(text, -1) {
		if len(match) < 4 || match[2] < 0 {
			continue
		}
		number, err := strconv.Atoi(text[match[2]:match[3]])
		if err != nil || number <= 0 {
			continue
		}
		result[number] = true
	}
	return result
}

func repositoryIssueASCIIIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '_' || char == '-' || char == '.' {
			continue
		}
		return false
	}
	return true
}

func (r repositoryIssueResult) fail(code, message string) repositoryIssueResult {
	r.OK = false
	r.Outcome = "failed"
	r.FailureCode = strings.TrimSpace(code)
	r.Message = strings.TrimSpace(message)
	return r
}

func (t *dianaRepositoryIssuesTool) finish(ctx context.Context, result repositoryIssueResult) (string, error) {
	if result.Operation != "" && result.Operation != "search" {
		t.audit(result)
	}
	// GitHub 上已经落地的写入不可撤销：标记之后，这一轮回复不会再被后续消息
	// 打断丢弃，用户至少能看到「已经建好了」和链接。草稿只存在本地，不算。
	if result.OK && result.Outcome != "" && result.Outcome != "draft_pending" && result.Operation != "search" && result.Operation != "list_drafts" {
		markExternalSideEffect(ctx)
	}
	body, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (t *dianaRepositoryIssuesTool) validateWriteAccess(repository string, owner bool) (string, string) {
	allowed, err := repositoryPublishAllowlist(t.settings.String(repositoryPublishSettingAllowlist, ""))
	if err != nil {
		return "invalid_allowlist", "仓库写入白名单配置无效，请使用逗号或换行分隔的精确 owner/repo。"
	}
	if !allowed[strings.ToLower(repository)] {
		return "repository_not_allowed", "目标仓库不在“仓库 Issue 发布”插件的精确写入白名单中。"
	}
	if !owner {
		legacyUsers, err := repositoryPublishUserAccess(t.settings.String(repositoryPublishSettingUserAccess, ""))
		if err != nil {
			return "invalid_user_repository_access", "用户仓库授权配置无效。"
		}
		legacyGroups, err := repositoryPublishGroupAccess(t.settings.String(repositoryPublishSettingGroupAccess, ""))
		if err != nil {
			return "invalid_group_repository_access", "群聊仓库授权配置无效。"
		}
		managerUsers, managerGroups, _, _, err := repositoryPublishEffectiveAccess(t.settings, legacyUsers, legacyGroups)
		if err != nil {
			return "invalid_repository_access", "Issue 授权配置无效。"
		}
		userID := strings.TrimSpace(t.event.UserID)
		key := strings.ToLower(repository)
		groupDirect := t.event.Kind == EventKindGroup && managerGroups[strings.TrimSpace(t.event.GroupID)][key]
		if !managerUsers[userID][key] && !groupDirect {
			return "permission_denied", "当前用户没有该仓库的写入权限。"
		}
		if groupDirect {
			return "", ""
		}
		tokens, err := repositoryPublishUserTokens(t.settings.String(repositoryPublishSettingUserTokens, ""))
		if err != nil {
			return "invalid_user_tokens", "用户 GitHub Token 配置无效。"
		}
		modes, err := repositoryPublishUserAuthModes(t.settings.String(repositoryPublishSettingUserAuth, ""))
		if err != nil {
			return "invalid_user_auth_modes", "用户 GitHub 认证来源配置无效。"
		}
		mode := modes[userID]
		// 未配置来源的旧规则继续要求个人 Token，避免升级后悄然扩大凭据权限。
		if (mode == "" || mode == repositoryPublishAuthToken) && strings.TrimSpace(tokens[userID]) == "" {
			return "user_token_required", "当前授权用户尚未配置自己的 GitHub Token。"
		}
		if mode == repositoryPublishUserAuthInherit && repositoryPublishAuthMode(t.settings) == repositoryPublishAuthToken && t.effectiveGlobalToken() == "" {
			return "token_required", "当前用户沿用的全局认证方式要求配置 GitHub Token，请在「GitHub 仓库 · 设置」里填写。"
		}
		return "", ""
	}
	if _, _, ok := t.repositoryBoundCredential(repository); ok {
		return "", ""
	}
	mode := repositoryPublishAuthMode(t.settings)
	if mode == repositoryPublishAuthToken && t.effectiveGlobalToken() == "" {
		return "token_required", "当前认证方式要求配置 GitHub Token，请在「GitHub 仓库 · 设置」里填写。"
	}
	return "", ""
}

// effectiveGlobalToken 返回实际会用到的公共 Token：优先发布插件自己的那份，为空时
// 回落到订阅插件，与 repositoryPublishCredential 的取值口径保持一致。
func (t *dianaRepositoryIssuesTool) effectiveGlobalToken() string {
	if token := strings.TrimSpace(t.settings.String(repositoryPublishSettingToken, "")); token != "" {
		return token
	}
	return t.sharedGitHubToken()
}

func repositoryPublishAuthMode(settings SettingValues) string {
	switch strings.ToLower(strings.TrimSpace(settings.String(repositoryPublishSettingAuthMode, repositoryPublishAuthToken))) {
	case repositoryPublishAuthGH:
		return repositoryPublishAuthGH
	case repositoryPublishAuthAuto:
		return repositoryPublishAuthAuto
	default:
		return repositoryPublishAuthToken
	}
}

func repositoryPublishValidateEventAccess(event MessageEvent, repository string, owner bool, settings SettingValues) (string, string) {
	if owner {
		return "", ""
	}
	userAccess, err := repositoryPublishUserAccess(settings.String(repositoryPublishSettingUserAccess, ""))
	if err != nil {
		return "invalid_user_repository_access", "用户仓库授权配置无效，请按每行“用户ID = owner/repo, owner/repo”填写。"
	}
	groupAccess, err := repositoryPublishGroupAccess(settings.String(repositoryPublishSettingGroupAccess, ""))
	if err != nil {
		return "invalid_group_repository_access", "群聊仓库授权配置无效，请按每行“群ID = owner/repo, owner/repo”填写。"
	}
	repositoryKey := strings.ToLower(repository)
	_, _, draftUsers, draftGroups, effectiveErr := repositoryPublishEffectiveAccess(settings, userAccess, groupAccess)
	if effectiveErr != nil {
		return "invalid_repository_access", "Issue 授权配置无效。"
	}
	userAllowed := draftUsers[strings.TrimSpace(event.UserID)][repositoryKey]
	groupAllowed := event.Kind == EventKindGroup && draftGroups[strings.TrimSpace(event.GroupID)][repositoryKey]
	if !userAllowed && !groupAllowed {
		return "permission_denied", "当前用户或所在群聊未获授权操作该 GitHub 仓库。"
	}
	allowed, err := repositoryPublishAllowlist(settings.String(repositoryPublishSettingAllowlist, ""))
	if err != nil {
		return "invalid_allowlist", "仓库写入白名单配置无效，请使用逗号或换行分隔的精确 owner/repo。"
	}
	if !allowed[strings.ToLower(repository)] {
		return "repository_not_allowed", "目标仓库不在“仓库 Issue 发布”插件的全局白名单中。"
	}
	return "", ""
}

func repositoryPublishUserHasAccess(userID string, settings SettingValues) bool {
	legacy, err := repositoryPublishUserAccess(settings.String(repositoryPublishSettingUserAccess, ""))
	if err != nil {
		return false
	}
	managers, _, drafts, _, err := repositoryPublishEffectiveAccess(settings, legacy, map[string]map[string]bool{})
	return err == nil && (len(managers[strings.TrimSpace(userID)]) > 0 || len(drafts[strings.TrimSpace(userID)]) > 0)
}

func repositoryPublishEventHasAccess(event MessageEvent, settings SettingValues) bool {
	if repositoryPublishUserHasAccess(event.UserID, settings) {
		return true
	}
	if event.Kind != EventKindGroup || strings.TrimSpace(event.GroupID) == "" {
		return false
	}
	legacy, err := repositoryPublishGroupAccess(settings.String(repositoryPublishSettingGroupAccess, ""))
	if err != nil {
		return false
	}
	_, managers, _, drafts, err := repositoryPublishEffectiveAccess(settings, map[string]map[string]bool{}, legacy)
	return err == nil && (len(managers[strings.TrimSpace(event.GroupID)]) > 0 || len(drafts[strings.TrimSpace(event.GroupID)]) > 0)
}

func repositoryPublishUserAccess(raw string) (map[string]map[string]bool, error) {
	return repositoryPublishScopedAccess(raw, "user")
}

func repositoryPublishGroupAccess(raw string) (map[string]map[string]bool, error) {
	return repositoryPublishScopedAccess(raw, "group")
}

func repositoryPublishScopedAccess(raw, scope string) (map[string]map[string]bool, error) {
	access := map[string]map[string]bool{}
	for _, line := range strings.FieldsFunc(raw, func(char rune) bool { return char == '\n' || char == '\r' || char == ';' || char == '；' }) {
		scopeID, repositories, ok := strings.Cut(line, "=")
		scopeID = strings.TrimSpace(scopeID)
		if !ok || scopeID == "" || strings.Contains(repositories, "=") {
			return nil, fmt.Errorf("invalid %s repository rule", scope)
		}
		if access[scopeID] == nil {
			access[scopeID] = map[string]bool{}
		}
		for _, item := range strings.Split(repositories, ",") {
			repository, err := normalizeGitHubRepository(item)
			if err != nil {
				return nil, err
			}
			access[scopeID][strings.ToLower(repository)] = true
		}
	}
	return access, nil
}

func repositoryPublishAllowlist(raw string) (map[string]bool, error) {
	items := strings.FieldsFunc(raw, func(char rune) bool {
		return char == ',' || char == ';' || char == '\n' || char == '\r'
	})
	allowed := make(map[string]bool, len(items))
	for _, item := range items {
		repository, err := normalizeGitHubRepository(item)
		if err != nil {
			return nil, err
		}
		allowed[strings.ToLower(repository)] = true
	}
	return allowed, nil
}

// repositoryPublishAllowlistNames 按配置顺序返回白名单里的仓库，保留原始大小写。
// repositoryPublishAllowlist 为了比对把键统一小写了，展示给人看时得用原始写法。
func repositoryPublishAllowlistNames(raw string) []string {
	items := strings.FieldsFunc(raw, func(char rune) bool {
		return char == ',' || char == ';' || char == '\n' || char == '\r'
	})
	names := make([]string, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		repository, err := normalizeGitHubRepository(item)
		if err != nil {
			continue
		}
		key := strings.ToLower(repository)
		if seen[key] {
			continue
		}
		seen[key] = true
		names = append(names, repository)
	}
	return names
}

// repositoryPublishEventRepositories 列出当前会话实际能操作的仓库。Owner 拿到整份
// 白名单；其他人只拿到自己或本群被授权、且仍在白名单里的那些。模型有了这份清单，
// 用户说「给 milksu 提个 issue」时就能直接对上号，不用再反问完整的 owner/repo。
func repositoryPublishEventRepositories(event MessageEvent, owner bool, settings SettingValues) []string {
	names := repositoryPublishAllowlistNames(settings.String(repositoryPublishSettingAllowlist, ""))
	if len(names) == 0 || owner {
		return names
	}
	legacyUsers, err := repositoryPublishUserAccess(settings.String(repositoryPublishSettingUserAccess, ""))
	if err != nil {
		return nil
	}
	legacyGroups, err := repositoryPublishGroupAccess(settings.String(repositoryPublishSettingGroupAccess, ""))
	if err != nil {
		return nil
	}
	managerUsers, managerGroups, draftUsers, draftGroups, err := repositoryPublishEffectiveAccess(settings, legacyUsers, legacyGroups)
	if err != nil {
		return nil
	}
	userID := strings.TrimSpace(event.UserID)
	groupID := strings.TrimSpace(event.GroupID)
	granted := make([]string, 0, len(names))
	for _, repository := range names {
		key := strings.ToLower(repository)
		reachable := managerUsers[userID][key] || draftUsers[userID][key]
		if !reachable && event.Kind == EventKindGroup {
			reachable = managerGroups[groupID][key] || draftGroups[groupID][key]
		}
		if reachable {
			granted = append(granted, repository)
		}
	}
	return granted
}

func (t *dianaRepositoryIssuesTool) search(ctx context.Context, repository string, input map[string]any) repositoryIssueResult {
	result := repositoryIssueResult{Operation: "search", Repository: repository}
	query, redactions := sanitizeRepositoryIssueText(configToolString(input, "query"), 500, true)
	result.Redactions = redactions
	if query == "" {
		return result.fail("invalid_input", "search 必须提供 query。")
	}
	if repositoryIssueSearchQualifierPattern.MatchString(query) || repositoryIssueSearchBooleanPattern.MatchString(query) || strings.ContainsAny(query, "\"`") {
		return result.fail("invalid_input", "query 只能包含普通关键词，不能注入仓库限定符、布尔操作或引号。")
	}
	state := strings.ToLower(strings.TrimSpace(configToolString(input, "state")))
	if state == "" {
		state = "open"
	}
	if state != "open" && state != "closed" && state != "all" {
		return result.fail("invalid_input", "state 必须是 open、closed 或 all。")
	}
	searchQuery := "repo:" + repository + " is:issue " + query
	if state != "all" {
		searchQuery += " is:" + state
	}
	values := url.Values{"q": {searchQuery}, "per_page": {"10"}}
	var payload struct {
		Items []githubRepositoryIssue `json:"items"`
	}
	if apiErr := t.doJSON(ctx, http.MethodGet, "/search/issues?"+values.Encode(), nil, &payload); apiErr != nil {
		return result.fail(apiErr.Code, t.failureMessage(apiErr.Code))
	}
	items := make([]repositoryIssueSummary, 0, len(payload.Items))
	for _, item := range payload.Items {
		if item.PullRequest != nil {
			continue
		}
		if !validRepositoryIssueCanonicalURL(item.HTMLURL, repository, "issues", item.Number) {
			return result.fail("invalid_response", "GitHub 搜索返回了目标仓库之外的 Issue，结果已拒绝。")
		}
		items = append(items, repositoryIssueSummaryFromGitHub(item))
	}
	result.OK = true
	result.Outcome = "searched"
	result.Items = items
	result.Message = fmt.Sprintf("已在 %s 中找到 %d 个匹配 Issue。", repository, len(items))
	return result
}

// describeResolvedDraft 说明一份已经处理过的草稿的真实归宿。
func (t *dianaRepositoryIssuesTool) describeResolvedDraft(draft repositoryIssueDraft) repositoryIssueResult {
	result := repositoryIssueResult{
		Operation:  "approve",
		Repository: draft.Repository,
		Draft:      repositoryIssueDraftViewFromDraft(draft),
	}
	switch strings.TrimSpace(draft.Status) {
	case "created":
		result.OK = true
		result.Outcome = "already_applied"
		result.Idempotent = true
		result.Message = "这份草稿已经提交过了，本次没有重复写入。"
		if draft.IssueNumber > 0 {
			result.RequestedNumber = draft.IssueNumber
			result.Message = fmt.Sprintf("这份草稿已经提交过了（#%d），本次没有重复写入。", draft.IssueNumber)
			result.Issue = &repositoryIssueSummary{Number: draft.IssueNumber, URL: draft.IssueURL, Title: configToolString(draft.Input, "title")}
		}
		return result
	case "cancelled":
		return result.fail("draft_cancelled", "这份草稿已经被取消，没有提交。")
	}
	return result.fail("draft_not_found", "本群没有可审批的 Issue 草稿，或草稿已处理。")
}

// repositoryIssueDraftOperation 返回草稿要执行的写操作。
//
// 新草稿把它存在 Operation 字段上；更早的版本存在 Input["operation"] 里，再早的
// 版本压根没存（那时只有 create 会落草稿）。三种都要认，否则升级前留下的待审批草稿
// 会被当成 create 执行。
func repositoryIssueDraftOperation(draft repositoryIssueDraft) string {
	if operation := strings.TrimSpace(draft.Operation); operation != "" {
		return operation
	}
	if operation := strings.TrimSpace(configToolString(draft.Input, "operation")); operation != "" {
		return operation
	}
	return "create"
}

// createWriteDraft 把一次写操作存成待确认草稿。
//
// 以前只有 create 和 comment 会走这里，update/close/reopen 直接执行，靠比对用户措辞
// （字段名、否定词、清空词）确认「用户真的要求了这件事」。措辞判断已经移除，而替代
// 不能是「什么都不查」——那就只剩模型自报。改为所有写操作先落草稿，由用户原样打出
// 确认码之后再执行：用户看到的是将要写入的确切内容，比猜措辞更强也更好解释。
func (t *dianaRepositoryIssuesTool) createWriteDraft(ctx context.Context, repository, operation string, input map[string]any) repositoryIssueResult {
	result := repositoryIssueResult{Operation: operation, Repository: repository}
	draftScope := strings.TrimSpace(t.event.GroupID)
	if t.event.Kind != EventKindGroup {
		if strings.TrimSpace(t.event.UserID) == "" {
			return result.fail("permission_denied", "Issue 草稿需要明确的私聊对象。")
		}
		draftScope = "private:" + strings.TrimSpace(t.event.UserID)
	}
	title, redactions := sanitizeRepositoryIssueText(configToolString(input, "title"), repositoryIssueTitleLimit, true)
	body, bodyRedactions := sanitizeRepositoryIssueText(configToolString(input, "body"), repositoryIssueBodyLimit, false)
	result.Redactions = redactions + bodyRedactions
	if operation == "comment" {
		if body == "" {
			return result.fail("invalid_input", "评论草稿必须提供非空 body。")
		}
		result.RequestedNumber = repositoryIssueNumber(input)
	} else if operation == "create" && title == "" {
		return result.fail("invalid_input", "生成 Issue 草稿必须提供标题。")
	}
	// 同标题的草稿不重复建。上一轮批准提交的结果还没进入这一轮的上下文时，模型会
	// 把同一个 Issue 再起一份草稿：待审批的直接把原草稿和确认码再给一次，刚提交过
	// 的直接报编号——用户明确要再建一份时传 allow_duplicate。
	// 带了 operation_id 的调用方自己在管幂等，交给指纹那条路。
	if operation == "create" && !boolInput(input, "allow_duplicate") && strings.TrimSpace(configToolString(input, "operation_id")) == "" {
		if existing, kind := t.findSameTitleDraft(ctx, draftScope, repository, title); kind == "pending" {
			result.OK = true
			result.Outcome = "draft_pending"
			result.RequiresApproval = true
			result.Draft = repositoryIssueDraftViewFromDraft(existing)
			result.Message = fmt.Sprintf(
				"同标题的草稿已经在等待审批，没有重复建。把这份草稿的内容和确认码 %s 单独成行再告诉用户一次，"+
					"等有权限的人自己打出确认码后用 operation=approve 和这个 draft_id 提交。",
				repositoryIssueConfirmationCode(existing.ID))
			return result
		} else if kind == "created" {
			result.OK = true
			result.Outcome = "already_created"
			result.Idempotent = true
			result.RequestedNumber = existing.IssueNumber
			result.Issue = &repositoryIssueSummary{Number: existing.IssueNumber, URL: existing.IssueURL, Title: configToolString(existing.Input, "title")}
			result.Draft = repositoryIssueDraftViewFromDraft(existing)
			result.Message = fmt.Sprintf(
				"同标题的 Issue 刚刚已经提交成功（#%d %s），这次没有再建草稿，也不需要确认码。"+
					"直接把这个编号和链接告诉用户；只有用户明确要再建一份时才带 allow_duplicate=true 重试。",
				existing.IssueNumber, existing.IssueURL)
			return result
		}
	}
	numbers, code, message := repositoryIssueNumbers(input)
	if code != "" {
		return result.fail(code, message)
	}
	if operation != "create" && len(numbers) == 0 {
		return result.fail("invalid_input", "改动已有 Issue 必须提供 issue 编号（number 或 numbers）。")
	}
	if operation == "create" && len(numbers) > 0 {
		return result.fail("invalid_input", "create 不接受 number/numbers。")
	}
	result.RequestedNumbers = repositoryIssueBatchTargets(input)
	appendBody, appendRedactions := sanitizeRepositoryIssueText(configToolString(input, "append_body"), repositoryIssueBodyLimit, false)
	result.Redactions += appendRedactions
	if operation == "update" && !repositoryIssueUpdateHasChanges(input, title, body, appendBody) {
		// 空 update 以前要等到审批那一步才被拒，用户先确认了一个什么都不改的草稿。
		return result.fail("invalid_input", "update 至少要提供 title、body、append_body、labels、assignees 或 milestone 中的一项。")
	}
	labels, _, code, message := repositoryIssueStringList(input, "labels", 20)
	if code != "" {
		return result.fail(code, message)
	}
	// 只把调用方真正传了的字段写进草稿。无条件塞 title/body 会让 update 把没提到的
	// 字段当成「要求清空」，把一次改标题变成连正文一起抹掉。
	draftInput := map[string]any{"user_confirmed_write": true}
	if _, present := input["title"]; present || operation == "create" {
		draftInput["title"] = title
	}
	if _, present := input["body"]; present {
		draftInput["body"] = body
	}
	if appendBody != "" {
		draftInput["append_body"] = appendBody
	}
	// 单个目标仍然写 number，老草稿和执行路径都认它；多个目标才写 numbers。
	if len(numbers) == 1 {
		draftInput["number"] = numbers[0]
	} else if len(numbers) > 1 {
		draftInput["numbers"] = numbers
	}
	// 幂等键、去重放行和确认令牌都属于本次写操作的一部分，必须跟着草稿走，否则
	// 确认之后执行的是一个丢了这些参数的请求。
	for _, key := range []string{"operation_id", "allow_duplicate", "confirmation_token"} {
		if value, present := input[key]; present {
			draftInput[key] = value
		}
	}
	// 记下提出这次写操作的那条用户消息。确认阶段的消息里只有确认码，而重复候选校验
	// 要看的是「提出者有没有点名候选编号」，那件事发生在提出的时候。
	if requestText := repositoryIssueCurrentRequestText(t.event); requestText != "" {
		draftInput["request_text"] = requestText
	}
	if state := strings.TrimSpace(configToolString(input, "state")); state != "" {
		draftInput["state"] = state
	}
	if len(labels) > 0 {
		draftInput["labels"] = labels
	}
	if assignees, present, code, message := repositoryIssueStringList(input, "assignees", 10); code != "" {
		return result.fail(code, message)
	} else if present {
		draftInput["assignees"] = assignees
	}
	if milestone, present, code, message := repositoryIssueMilestone(input, false); code != "" {
		return result.fail(code, message)
	} else if present {
		draftInput["milestone"] = milestone
	}
	draft, err := t.plugin.saveDraft(ctx, repositoryIssueDraft{
		Platform: t.event.Platform, ProfileID: t.event.ProfileID,
		GroupID: draftScope, Repository: repository, Operation: operation,
		RequesterID: strings.TrimSpace(t.event.UserID), RequesterName: strings.TrimSpace(t.event.SenderName), Input: draftInput,
	})
	if err != nil {
		return result.fail("draft_store_failed", "Issue 草稿保存失败。")
	}
	result.OK = true
	result.Outcome = "draft_pending"
	// 这段文案里「确认码要发出去」和「确认码只能由用户自己打出来」是两件事，必须
	// 分开说清楚。原先写的是「不要替用户说出确认码」——本意是别代替用户完成确认，
	// 但模型完全可以读成「别把确认码说出来」，于是它真的不发，管理员无从确认，
	// 整条审批链路就断在这里。
	result.Message = fmt.Sprintf(
		"草稿已生成，尚未写入 GitHub。把将要写入的内容原样告诉用户，并把确认码 %s 单独成行写进这次回复里——"+
			"不发出来对方就没法确认。然后等有权限的人自己把这个码打出来，收到之后再用 operation=approve "+
			"和这个 draft_id 执行；在那之前不要调用 approve，也不要当作对方已经确认过。",
		repositoryIssueConfirmationCode(draft.ID))
	result.RequiresApproval = true
	result.Draft = repositoryIssueDraftViewFromDraft(draft)
	return result
}

func (t *dianaRepositoryIssuesTool) approveDraft(ctx context.Context, input map[string]any) repositoryIssueResult {
	result := repositoryIssueResult{Operation: "approve", Message: "Issue 草稿未提交。"}
	scope := strings.TrimSpace(t.event.GroupID)
	if t.event.Kind != EventKindGroup {
		scope = "private:" + strings.TrimSpace(t.event.UserID)
	}
	draft, ok, err := t.plugin.findDraft(ctx, scope, configToolString(input, "draft_id"))
	if err != nil {
		return result.fail("draft_store_failed", "读取 Issue 草稿失败。")
	}
	if !ok {
		// 并发审批时草稿可能已经被前一条消息消费掉。这时必须照实说明它已经
		// 执行过，而不是含糊地报「找不到」——后者会让用户以为写入失败，可
		// GitHub 上其实已经建好了。
		if resolved, found, findErr := t.plugin.findResolvedDraft(ctx, scope, configToolString(input, "draft_id")); findErr == nil && found {
			return t.describeResolvedDraft(resolved)
		}
		return result.fail("draft_not_found", "本群没有可审批的 Issue 草稿，或草稿已处理。")
	}
	result.Repository = draft.Repository
	owner := t.runtime.relationshipPolicy(ctx, t.event).Owner
	userAllowed, _, code, message := repositoryPublishAccessForEvent(t.event, draft.Repository, owner, t.settings)
	if code != "" || !userAllowed {
		if code == "" {
			code, message = "permission_denied", "当前用户没有该仓库的审批权限。"
		}
		return result.fail(code, message)
	}
	// 确认必须是用户本人原样打出运行时给的确认码。以前这里扫「同意/批准/提交」并用
	// 「不同意/取消/拒绝」反向排除，那是拿关键词判断意图：措辞千变万化，判宽了会替
	// 用户写 Issue，判严了又让人确认不了。确认码没有这个歧义。
	if !repositoryIssueRequestConfirms(repositoryIssueCurrentRequestText(t.event), draft.ID) {
		return result.fail("explicit_approval_required", fmt.Sprintf(
			"当前消息里没有确认码 %s。请让有权限的人原样回复它再执行。", repositoryIssueConfirmationCode(draft.ID)))
	}
	if code, message := t.validateWriteAccess(draft.Repository, owner); code != "" {
		return result.fail(code, message)
	}
	writeInput := make(map[string]any, len(draft.Input)+2)
	for key, value := range draft.Input {
		writeInput[key] = value
	}
	for _, key := range []string{"allow_duplicate", "confirmation_token"} {
		if value, ok := input[key]; ok {
			writeInput[key] = value
		}
	}
	// 执行草稿记录的那个操作，而不是一律当成 create：草稿现在也承载 update、comment
	// 和开关状态。旧草稿没有 operation 字段，按 create 处理保持兼容。
	operation := repositoryIssueDraftOperation(draft)
	if operation != "create" && operation != "update" && operation != "comment" && operation != "close" && operation != "reopen" {
		return result.fail("invalid_operation", "草稿记录的操作无法执行。")
	}
	var executed repositoryIssueResult
	if targets := repositoryIssueBatchTargets(writeInput); len(targets) > 1 {
		executed = t.executeBatch(ctx, draft.Repository, operation, writeInput, targets)
	} else {
		executed = t.executeWrite(ctx, draft.Repository, operation, writeInput)
	}
	executed.Operation = "approve"
	executed.Draft = repositoryIssueDraftViewFromDraft(draft)
	// 批量里只要有一条写上了，草稿就算用掉：再批一次会把成功的那些重做一遍
	// （update 不幂等）。没写上的编号在 Failures 里逐条列出，用户另起一份草稿。
	if executed.OK || len(executed.Items) > 0 {
		draft.Status = "created"
		draft.ResolvedBy = strings.TrimSpace(t.event.UserID)
		if executed.Issue != nil {
			draft.IssueNumber = executed.Issue.Number
			draft.IssueURL = executed.Issue.URL
		}
		if err := t.plugin.updateDraft(ctx, draft); err != nil {
			executed.Message += " 写操作已执行，但草稿状态保存失败。"
		}
	}
	return executed
}

// executeWrite 对单个目标执行草稿记录的写操作。
func (t *dianaRepositoryIssuesTool) executeWrite(ctx context.Context, repository, operation string, input map[string]any) repositoryIssueResult {
	switch operation {
	case "create":
		return t.create(ctx, repository, input)
	case "update":
		return t.update(ctx, repository, input)
	case "comment":
		return t.comment(ctx, repository, input)
	default:
		return t.setState(ctx, repository, input, operation)
	}
}

// executeBatch 把同一份改动逐个写到 targets 上。一条失败不拦后面的：用户要的是
// 「这几个都改掉」，中途停下只会留下一半改了一半没改、还得自己数哪些成了。
func (t *dianaRepositoryIssuesTool) executeBatch(ctx context.Context, repository, operation string, input map[string]any, targets []int) repositoryIssueResult {
	result := repositoryIssueResult{Operation: operation, Repository: repository, RequestedNumbers: targets}
	for _, number := range targets {
		single := make(map[string]any, len(input))
		for key, value := range input {
			if key == "numbers" {
				continue
			}
			single[key] = value
		}
		single["number"] = number
		one := t.executeWrite(ctx, repository, operation, single)
		result.Redactions += one.Redactions
		if !one.OK {
			result.Failures = append(result.Failures, repositoryIssueBatchFailure{Number: number, FailureCode: one.FailureCode, Message: one.Message})
			continue
		}
		if one.Issue != nil {
			result.Items = append(result.Items, *one.Issue)
		}
	}
	succeeded := len(targets) - len(result.Failures)
	switch {
	case succeeded == len(targets):
		result.OK = true
		result.Outcome = repositoryIssueBatchOutcome(operation)
		result.Message = fmt.Sprintf("GitHub 已对 %d 个 Issue 完成 %s。", succeeded, operation)
	case succeeded == 0:
		result.Outcome = "failed"
		result.FailureCode = result.Failures[0].FailureCode
		result.Message = fmt.Sprintf("%d 个 Issue 全部未能完成 %s，见 failures。", len(targets), operation)
	default:
		result.Outcome = "partial"
		result.FailureCode = "partial_failure"
		result.Message = fmt.Sprintf("%d 个 Issue 完成了 %s，%d 个失败，见 failures；失败的编号需要另起草稿重试。", succeeded, operation, len(result.Failures))
	}
	return result
}

func repositoryIssueBatchOutcome(operation string) string {
	switch operation {
	case "update":
		return "updated"
	case "comment":
		return "commented"
	case "close":
		return "closed"
	case "reopen":
		return "reopened"
	}
	return operation
}

const repositoryIssueBatchLimit = 20

// repositoryIssueNumbers 汇总 number 与 numbers：去重、保序、全部必须是正整数。
func repositoryIssueNumbers(input map[string]any) ([]int, string, string) {
	seen := map[int]bool{}
	var out []int
	add := func(number int) {
		if !seen[number] {
			seen[number] = true
			out = append(out, number)
		}
	}
	if _, present := input["number"]; present {
		number := repositoryIssueNumber(input)
		if number <= 0 {
			return nil, "invalid_input", "number 必须是正整数。"
		}
		add(number)
	}
	if raw, present := input["numbers"]; present && raw != nil {
		var items []any
		switch typed := raw.(type) {
		case []any:
			items = typed
		case []int:
			for _, item := range typed {
				items = append(items, item)
			}
		default:
			return nil, "invalid_input", "numbers 必须是 Issue 编号数组。"
		}
		for _, item := range items {
			value, ok := numberValue(item)
			if !ok || value <= 0 || value != float64(int(value)) {
				return nil, "invalid_input", "numbers 里每一项都必须是正整数。"
			}
			add(int(value))
		}
	}
	if len(out) > repositoryIssueBatchLimit {
		return nil, "invalid_input", "一次最多改 " + itoa(repositoryIssueBatchLimit) + " 个 Issue。"
	}
	return out, "", ""
}

// repositoryIssueBatchTargets 只在确实是批量（两个及以上）时返回编号列表。
func repositoryIssueBatchTargets(input map[string]any) []int {
	numbers, code, _ := repositoryIssueNumbers(input)
	if code != "" || len(numbers) < 2 {
		return nil
	}
	return numbers
}

func (t *dianaRepositoryIssuesTool) listDrafts(ctx context.Context, input map[string]any) repositoryIssueResult {
	result := repositoryIssueResult{Operation: "list_drafts", Message: "没有找到 Issue 草稿。"}
	if t.event.Kind != EventKindGroup && strings.TrimSpace(t.event.UserID) == "" {
		return result.fail("permission_denied", "只能在有明确对象的会话中列出 Issue 草稿。")
	}
	scope := strings.TrimSpace(t.event.GroupID)
	if t.event.Kind != EventKindGroup {
		scope = "private:" + strings.TrimSpace(t.event.UserID)
	}
	groups, err := repositoryPublishGroupAccess(t.settings.String(repositoryPublishSettingGroupAccess, ""))
	managerUsers, managerGroups, draftUsers, draftGroups, effectiveErr := repositoryPublishEffectiveAccess(t.settings, func() map[string]map[string]bool {
		v, _ := repositoryPublishUserAccess(t.settings.String(repositoryPublishSettingUserAccess, ""))
		return v
	}(), groups)
	// 按用户授权的人在群里同样有权限：create 和 approve 都只看 managerUsers/draftUsers，
	// 不限会话类型。这里以前在群聊里只查群维度配置，于是「群友甲是管理员、但整个群
	// 没放开」时他能建能批，却列不出草稿。
	userID := strings.TrimSpace(t.event.UserID)
	groupID := strings.TrimSpace(t.event.GroupID)
	reachable := len(draftUsers[userID]) > 0 || len(managerUsers[userID]) > 0
	if !reachable && t.event.Kind == EventKindGroup {
		reachable = len(draftGroups[groupID]) > 0 || len(managerGroups[groupID]) > 0
	}
	// 主人建得了也批得了，列表没理由把他拦在外面：这里以前只看名单，主人没把自己
	// 写进名单就列不出草稿，而 create/approve 都是认主人的。
	if !reachable && t.runtime.relationshipPolicy(ctx, t.event).Owner {
		reachable = true
	}
	if err != nil || effectiveErr != nil || !reachable {
		return result.fail("permission_denied", "当前会话没有任何已授权的 Issue 草稿仓库。")
	}
	status := strings.ToLower(strings.TrimSpace(configToolString(input, "status")))
	if status == "" {
		status = "recent"
	}
	if status != "recent" && status != "pending" && status != "created" && status != "cancelled" && status != "all" {
		return result.fail("invalid_input", "status 必须是 recent、pending、created、cancelled 或 all。")
	}
	queryStatus := status
	if status == "recent" {
		queryStatus = "all"
	}
	drafts, err := t.plugin.listDrafts(ctx, scope, queryStatus)
	if err != nil {
		return result.fail("draft_store_failed", "读取 Issue 草稿列表失败。")
	}
	// 默认列表以前只有待审批的：一份草稿刚被批准提交，下一轮模型再 list 就看不到
	// 它了，于是把「已经提交」读成「草稿丢了」，接着重新建一份、再要一次确认码。
	// 今晚 #67 和 #70 都是这么重复出来的——上一轮的回复还没进入下一轮的上下文，
	// 模型手里只有这份列表。所以默认把最近处理完的草稿也列出来，带着状态和编号。
	resolved := 0
	if status == "recent" {
		kept := make([]repositoryIssueDraft, 0, len(drafts))
		cutoff := time.Now().Add(-repositoryIssueRecentDraftWindow)
		for _, draft := range drafts {
			if draft.Status == "pending" {
				kept = append(kept, draft)
				continue
			}
			if resolved >= repositoryIssueRecentResolvedLimit || draft.UpdatedAt.Before(cutoff) {
				continue
			}
			resolved++
			kept = append(kept, draft)
		}
		drafts = kept
	}
	result.OK = true
	result.Outcome = "listed"
	// 只报条数等于没报：用户看完列表还是不知道拿什么去确认。待审批的草稿必须连
	// confirmation_code 一起复述，否则这条链路到列表这一步就断了。
	result.Message = fmt.Sprintf(
		"共找到 %d 条 Issue 草稿。逐条把标题和内容复述给用户；待审批的草稿要把它的 confirmation_code 原样写进回复——"+
			"不发出来对方就没法确认。说明由有权限的人自己打出该确认码才会提交；在那之前不要调用 approve，"+
			"也不要当作对方已经确认过。", len(drafts))
	if resolved > 0 {
		result.Message += fmt.Sprintf(" 其中 %d 条是最近已处理的：status=created 的已经写进 GitHub（issue_number/issue_url 就是结果），"+
			"status=cancelled 的已被用户取消；这两种都不要再为同一件事重新建草稿或要确认码。", resolved)
	}
	result.Drafts = make([]repositoryIssueDraftView, 0, len(drafts))
	for _, draft := range drafts {
		result.Drafts = append(result.Drafts, *repositoryIssueDraftViewFromDraft(draft))
	}
	return result
}

const (
	// repositoryIssueRecentDraftWindow 是默认草稿列表里保留已处理草稿的时长。
	repositoryIssueRecentDraftWindow   = 12 * time.Hour
	repositoryIssueRecentResolvedLimit = 10
)

// repositoryIssueDraftTitleKey 把标题归一成比对键：大小写、首尾和连续空白都不算差异。
func repositoryIssueDraftTitleKey(title string) string {
	return strings.ToLower(strings.Join(strings.Fields(title), " "))
}

// findSameTitleDraft 在当前会话范围里找同仓库、同标题的草稿：待审批的直接复用，
// 最近刚提交成功的当成「已经建好」。返回的第二个值说明找到的是哪一种。
func (t *dianaRepositoryIssuesTool) findSameTitleDraft(ctx context.Context, scope, repository, title string) (repositoryIssueDraft, string) {
	key := repositoryIssueDraftTitleKey(title)
	if key == "" {
		return repositoryIssueDraft{}, ""
	}
	drafts, err := t.plugin.listDrafts(ctx, scope, "all")
	if err != nil {
		return repositoryIssueDraft{}, ""
	}
	cutoff := time.Now().Add(-repositoryIssueRecentDraftWindow)
	var created repositoryIssueDraft
	for _, draft := range drafts {
		if !strings.EqualFold(draft.Repository, repository) || repositoryIssueDraftOperation(draft) != "create" {
			continue
		}
		if repositoryIssueDraftTitleKey(configToolString(draft.Input, "title")) != key {
			continue
		}
		switch draft.Status {
		case "pending":
			return draft, "pending"
		case "created":
			if created.ID == "" && draft.IssueNumber > 0 && !draft.UpdatedAt.Before(cutoff) {
				created = draft
			}
		}
	}
	if created.ID != "" {
		return created, "created"
	}
	return repositoryIssueDraft{}, ""
}

func (t *dianaRepositoryIssuesTool) cancelDraft(ctx context.Context, input map[string]any) repositoryIssueResult {
	result := repositoryIssueResult{Operation: "cancel_draft", Message: "Issue 草稿未取消。"}
	scope := strings.TrimSpace(t.event.GroupID)
	if t.event.Kind != EventKindGroup {
		scope = "private:" + strings.TrimSpace(t.event.UserID)
	}
	draft, ok, err := t.plugin.findDraft(ctx, scope, configToolString(input, "draft_id"))
	if err != nil {
		return result.fail("draft_store_failed", "读取 Issue 草稿失败。")
	}
	if !ok {
		return result.fail("draft_not_found", "本群没有可取消的待审批草稿。")
	}
	result.Repository = draft.Repository
	owner := t.runtime.relationshipPolicy(ctx, t.event).Owner
	userAllowed, _, code, _ := repositoryPublishAccessForEvent(t.event, draft.Repository, owner, t.settings)
	if code != "" || !userAllowed {
		return result.fail("permission_denied", "当前用户没有该仓库的草稿管理权限。")
	}
	// 取消草稿只会让写操作不发生，判错的代价是「本该写的没写」，用户重说一次即可。
	// 这里原本扫「取消/拒绝/作废」确认意图，属于关键词判断；权限已在上面校验过，
	// 安全方向的动作不需要再猜措辞。
	draft.Status = "cancelled"
	draft.ResolvedBy = strings.TrimSpace(t.event.UserID)
	if err := t.plugin.updateDraft(ctx, draft); err != nil {
		return result.fail("draft_store_failed", "取消草稿时保存状态失败。")
	}
	result.OK = true
	result.Outcome = "cancelled"
	result.Message = "Issue 草稿已取消，不会写入 GitHub。"
	result.Draft = repositoryIssueDraftViewFromDraft(draft)
	return result
}

// repositoryIssueConfirmationCode 取草稿 ID 的前缀作为确认码。
//
// 确认要用一个运行时自己生成、用户必须原样打出来的记号，而不是去猜「同意/批准」
// 这类措辞——那是拿关键词判断意图。前缀足够短到能手打，又不可能在正常聊天里撞上。
func repositoryIssueConfirmationCode(draftID string) string {
	draftID = strings.TrimSpace(draftID)
	if len(draftID) <= repositoryIssueConfirmationCodeLength {
		return draftID
	}
	return draftID[:repositoryIssueConfirmationCodeLength]
}

// repositoryIssueRequestConfirms 判断用户本人这条消息里是否原样写出了确认码。
// 只看用户自己的话：引用和转发内容已由 repositoryIssueStripUntrustedContext 去掉，
// 免得别人贴一段带确认码的记录就能替他确认。
func repositoryIssueRequestConfirms(text, draftID string) bool {
	code := repositoryIssueConfirmationCode(draftID)
	if code == "" {
		return false
	}
	pattern := regexp.MustCompile(`(?i)(?:^|[^a-z0-9])` + regexp.QuoteMeta(code) + `(?:[^a-z0-9]|$)`)
	return pattern.MatchString(text)
}

// writeRequestText 返回提出这次写操作的用户消息。走草稿时它记在草稿里，因为确认阶段
// 的消息只有确认码；直接调用时就是当前消息。
func (t *dianaRepositoryIssuesTool) writeRequestText(input map[string]any) string {
	if recorded := strings.TrimSpace(configToolString(input, "request_text")); recorded != "" {
		return recorded
	}
	return repositoryIssueCurrentRequestText(t.event)
}

func repositoryIssueDraftViewFromDraft(draft repositoryIssueDraft) *repositoryIssueDraftView {
	labels, _, _, _ := repositoryIssueStringList(draft.Input, "labels", 20)
	assignees, _, _, _ := repositoryIssueStringList(draft.Input, "assignees", 10)
	operation := strings.TrimSpace(draft.Operation)
	if operation == "" {
		operation = "create"
	}
	return &repositoryIssueDraftView{
		ID: draft.ID, GroupID: draft.GroupID, Repository: draft.Repository, Operation: operation,
		IssueTarget:  repositoryIssueNumber(draft.Input),
		IssueTargets: repositoryIssueBatchTargets(draft.Input),
		Title:        configToolString(draft.Input, "title"),
		Body:         configToolString(draft.Input, "body"), Labels: labels,
		AppendBody:  configToolString(draft.Input, "append_body"),
		Assignees:   assignees,
		Milestone:   draft.Input["milestone"],
		State:       configToolString(draft.Input, "state"),
		RequesterID: draft.RequesterID, RequesterName: draft.RequesterName, Status: draft.Status, CreatedAt: draft.CreatedAt,
		// 只有待审批的草稿需要确认码：已提交和已取消的草稿再报一个码，只会让人以为
		// 还能确认。
		ConfirmationCode: confirmationCodeForPendingDraft(draft),
		IssueNumber:      draft.IssueNumber, IssueURL: draft.IssueURL,
	}
}

func confirmationCodeForPendingDraft(draft repositoryIssueDraft) string {
	if strings.TrimSpace(draft.Status) != "pending" {
		return ""
	}
	return repositoryIssueConfirmationCode(draft.ID)
}

func (t *dianaRepositoryIssuesTool) create(ctx context.Context, repository string, input map[string]any) repositoryIssueResult {
	result := repositoryIssueResult{Operation: "create", Repository: repository}
	title, titleRedactions := sanitizeRepositoryIssueText(configToolString(input, "title"), repositoryIssueTitleLimit, true)
	body, bodyRedactions := sanitizeRepositoryIssueText(configToolString(input, "body"), repositoryIssueBodyLimit, false)
	result.Redactions = titleRedactions + bodyRedactions
	if title == "" {
		return result.fail("invalid_input", "create 必须提供非空 title。")
	}
	labels, _, code, message := repositoryIssueStringList(input, "labels", 20)
	if code != "" {
		return result.fail(code, message)
	}
	assignees, _, code, message := repositoryIssueStringList(input, "assignees", 10)
	if code != "" {
		return result.fail(code, message)
	}
	sort.Strings(labels)
	sort.Strings(assignees)
	milestone, milestoneSet, code, message := repositoryIssueMilestone(input, false)
	if code != "" {
		return result.fail(code, message)
	}
	operationID := strings.TrimSpace(configToolString(input, "operation_id"))
	fingerprint, payloadHash, code, message := repositoryIssueFingerprint(repository, "create", operationID, map[string]any{
		"title": title, "body": body, "labels": labels, "assignees": assignees, "milestone": milestone,
	})
	if code != "" {
		return result.fail(code, message)
	}
	result.Fingerprint = fingerprint
	marker := repositoryIssueOperationMarkerWithPayload("create", fingerprint, payloadHash)
	legacyMarker := repositoryIssueOperationMarker("create", fingerprint)
	markerPrefix := repositoryIssueOperationMarkerPrefix("create", fingerprint)
	operationKey := strings.ToLower(repository) + ":create:" + fingerprint
	unlock := t.plugin.operationLock(operationKey)
	defer unlock()

	issues, apiErr := t.listRecentIssues(ctx, repository)
	if apiErr != nil {
		return result.fail(apiErr.Code, t.failureMessage(apiErr.Code))
	}
	if existing, ok := repositoryIssueWithAnyMarker(issues, marker, legacyMarker); ok {
		t.plugin.clearOperationUncertain(operationKey)
		result.OK = true
		result.Outcome = "reused"
		result.Message = "该创建操作已经由 GitHub 确认，已返回原 Issue。"
		result.Issue = ptrRepositoryIssueSummary(repositoryIssueSummaryFromGitHub(existing))
		result.Idempotent = true
		return result
	}
	if operationID != "" {
		if _, ok := repositoryIssueWithMarkerPrefix(issues, markerPrefix); ok {
			return result.fail("operation_id_conflict", "operation_id 已用于不同的创建内容；请更新原 Issue 或使用新的 operation_id。")
		}
	}
	if t.plugin.operationUncertain(operationKey) {
		return result.fail("pending_reconciliation", "此前写入结果仍不确定；为避免重复创建，本操作只允许继续对账，请稍后再试或使用新的 operation_id。")
	}
	candidates := similarRepositoryIssues(issues, title, labels, time.Now(), 5)
	if len(candidates) > 0 {
		confirmation := strings.TrimSpace(configToolString(input, "confirmation_token"))
		confirmed := boolInput(input, "allow_duplicate") &&
			repositoryIssueRequestMentionsCandidate(t.writeRequestText(input), candidates) &&
			t.verifyDuplicateConfirmation(confirmation, repository, fingerprint, candidates)
		if !confirmed {
			result.OK = false
			result.Outcome = "duplicate_candidate"
			result.FailureCode = "duplicate_candidate"
			result.Message = "发现标题或标签相似的现有 Issue；请先向用户展示候选。用户必须在新消息中点名候选编号并明确坚持另行新建。"
			result.Items = candidates
			result.RequiresConfirmation = true
			result.ConfirmationToken = t.newDuplicateConfirmation(repository, fingerprint, candidates)
			return result
		}
	}

	payload := map[string]any{
		"title": title,
		"body":  appendRepositoryIssueMarker(body, marker),
	}
	if len(labels) > 0 {
		payload["labels"] = labels
	}
	if len(assignees) > 0 {
		payload["assignees"] = assignees
	}
	if milestoneSet {
		payload["milestone"] = milestone
	}
	var created githubRepositoryIssue
	apiErr = t.doJSON(ctx, http.MethodPost, "/repos/"+repository+"/issues", payload, &created)
	if apiErr == nil {
		result.OK = true
		result.Outcome = "created"
		result.Message = "GitHub 已创建 Issue。"
		result.Issue = ptrRepositoryIssueSummary(repositoryIssueSummaryFromGitHub(created))
		return result
	}
	if apiErr.Uncertain {
		t.plugin.markOperationUncertain(operationKey)
		if existing, ok := t.reconcileIssueMarker(repository, marker); ok {
			t.plugin.clearOperationUncertain(operationKey)
			result.OK = true
			result.Outcome = "reconciled"
			result.Message = "创建确认一度不确定，已通过远端操作标记对账到唯一 Issue。"
			result.Issue = ptrRepositoryIssueSummary(repositoryIssueSummaryFromGitHub(existing))
			result.Idempotent = true
			result.Reconciled = true
			return result
		}
	}
	return result.fail(apiErr.Code, t.failureMessage(apiErr.Code))
}

func (t *dianaRepositoryIssuesTool) update(ctx context.Context, repository string, input map[string]any) repositoryIssueResult {
	result := repositoryIssueResult{Operation: "update", Repository: repository, RequestedNumber: repositoryIssueNumber(input)}
	number := repositoryIssueNumber(input)
	if number <= 0 {
		return result.fail("invalid_input", "update 必须提供有效的 Issue number。")
	}
	current, apiErr := t.getIssue(ctx, repository, number)
	if apiErr != nil {
		return result.fail(apiErr.Code, t.failureMessage(apiErr.Code))
	}
	payload := map[string]any{}
	redactions := 0
	if _, present := input["title"]; present {
		title, count := sanitizeRepositoryIssueText(configToolString(input, "title"), repositoryIssueTitleLimit, true)
		redactions += count
		if title == "" {
			return result.fail("invalid_input", "title 不能清空。")
		}
		payload["title"] = title
	}
	if _, present := input["body"]; present {
		body, count := sanitizeRepositoryIssueText(configToolString(input, "body"), repositoryIssueBodyLimit, false)
		redactions += count
		payload["body"] = preserveRepositoryIssueCreateMarker(body, current.Body)
	}
	if appendBody, count := sanitizeRepositoryIssueText(configToolString(input, "append_body"), repositoryIssueBodyLimit, false); appendBody != "" {
		redactions += count
		// 追加是在「当前要写入的正文」上做：没传 body 就是远端现有正文。对账标记
		// 从正文里摘出来再补回末尾，免得新内容追加到隐藏注释后面。
		base := current.Body
		if replaced, ok := payload["body"].(string); ok {
			base = replaced
		}
		combined := appendRepositoryIssueBody(base, appendBody)
		if len(combined) > repositoryIssueBodyLimit {
			return result.fail("invalid_input", "追加后的正文超过 "+itoa(repositoryIssueBodyLimit)+" 字符上限。")
		}
		payload["body"] = preserveRepositoryIssueCreateMarker(combined, current.Body)
	}
	for _, key := range []string{"labels", "assignees"} {
		limit := 20
		if key == "assignees" {
			limit = 10
		}
		items, present, code, message := repositoryIssueStringList(input, key, limit)
		if code != "" {
			return result.fail(code, message)
		}
		if present {
			payload[key] = items
		}
	}
	if _, present := input["milestone"]; present {
		milestone, _, code, message := repositoryIssueMilestone(input, true)
		if code != "" {
			return result.fail(code, message)
		}
		if input["milestone"] == nil {
			payload["milestone"] = nil
		} else {
			payload["milestone"] = milestone
		}
	}
	result.Redactions = redactions
	if len(payload) == 0 {
		return result.fail("invalid_input", "update 至少要提供 title、body、append_body、labels、assignees 或 milestone 中的一项。")
	}
	var updated githubRepositoryIssue
	if apiErr := t.doJSON(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/issues/%d", repository, number), payload, &updated); apiErr != nil {
		return result.fail(apiErr.Code, t.failureMessage(apiErr.Code))
	}
	result.OK = true
	result.Outcome = "updated"
	result.Message = "GitHub 已更新 Issue。"
	result.Issue = ptrRepositoryIssueSummary(repositoryIssueSummaryFromGitHub(updated))
	return result
}

// get 把一个 Issue 的标题、正文和最近评论读回来。update 是整段覆盖，模型不先看
// 原文就只能凭记忆重写，很容易把原内容冲掉；以前工具里没有任何操作能读正文。
func (t *dianaRepositoryIssuesTool) get(ctx context.Context, repository string, input map[string]any) repositoryIssueResult {
	result := repositoryIssueResult{Operation: "get", Repository: repository, RequestedNumber: repositoryIssueNumber(input)}
	number := repositoryIssueNumber(input)
	if number <= 0 {
		return result.fail("invalid_input", "get 必须提供有效的 Issue number。")
	}
	issue, apiErr := t.getIssue(ctx, repository, number)
	if apiErr != nil {
		return result.fail(apiErr.Code, t.failureMessage(apiErr.Code))
	}
	if !validRepositoryIssueCanonicalURL(issue.HTMLURL, repository, "issues", issue.Number) {
		return result.fail("invalid_response", "GitHub 返回了目标仓库之外的 Issue，结果已拒绝。")
	}
	values := url.Values{"per_page": {strconv.Itoa(repositoryIssueGetCommentLimit + 1)}}
	var comments []githubIssueComment
	if apiErr := t.doJSON(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/issues/%d/comments?%s", repository, number, values.Encode()), nil, &comments); apiErr != nil {
		return result.fail(apiErr.Code, t.failureMessage(apiErr.Code))
	}
	result.OK = true
	result.Outcome = "fetched"
	result.Issue = ptrRepositoryIssueSummary(repositoryIssueSummaryFromGitHub(issue))
	result.IssueBody, result.IssueBodyTruncated = repositoryIssueDisplayText(issue.Body, repositoryIssueGetBodyLimit)
	if len(comments) > repositoryIssueGetCommentLimit {
		comments = comments[:repositoryIssueGetCommentLimit]
		result.CommentsTruncated = true
	}
	result.Comments = make([]repositoryIssueCommentView, 0, len(comments))
	for _, comment := range comments {
		view := repositoryIssueCommentView{URL: comment.HTMLURL, CreatedAt: comment.CreatedAt}
		if comment.User != nil {
			view.Author = comment.User.Login
		}
		view.Body, view.Truncated = repositoryIssueDisplayText(comment.Body, repositoryIssueGetCommentBodyLimit)
		result.Comments = append(result.Comments, view)
	}
	result.Message = fmt.Sprintf("已读取 %s#%d 的正文和 %d 条评论。", repository, number, len(result.Comments))
	return result
}

const (
	repositoryIssueGetBodyLimit        = 8_000
	repositoryIssueGetCommentLimit     = 20
	repositoryIssueGetCommentBodyLimit = 2_000
)

// repositoryIssueDisplayText 去掉运行时对账用的隐藏标记并按上限截断，给模型看。
func repositoryIssueDisplayText(text string, limit int) (string, bool) {
	text = strings.TrimSpace(repositoryIssueAnyMarkerPattern.ReplaceAllString(text, ""))
	runes := []rune(text)
	if len(runes) <= limit {
		return text, false
	}
	return string(runes[:limit]), true
}

// appendRepositoryIssueBody 把 addition 接到 base 正文末尾。base 里的对账标记先
// 摘掉，由调用方在合并后重新补到末尾。
func appendRepositoryIssueBody(base, addition string) string {
	base = strings.TrimSpace(repositoryIssueCreateMarkerPattern.ReplaceAllString(base, ""))
	addition = strings.TrimSpace(addition)
	switch {
	case base == "":
		return addition
	case addition == "":
		return base
	default:
		return base + "\n\n" + addition
	}
}

// repositoryIssueUpdateHasChanges 判断一份 update 请求到底有没有要改的东西。
func repositoryIssueUpdateHasChanges(input map[string]any, title, body, appendBody string) bool {
	if _, present := input["title"]; present && title != "" {
		return true
	}
	if _, present := input["body"]; present && body != "" {
		return true
	}
	if appendBody != "" {
		return true
	}
	for _, key := range []string{"labels", "assignees", "milestone"} {
		if _, present := input[key]; present {
			return true
		}
	}
	return false
}

func (t *dianaRepositoryIssuesTool) comment(ctx context.Context, repository string, input map[string]any) repositoryIssueResult {
	result := repositoryIssueResult{Operation: "comment", Repository: repository, RequestedNumber: repositoryIssueNumber(input)}
	number := repositoryIssueNumber(input)
	if number <= 0 {
		return result.fail("invalid_input", "comment 必须提供有效的 Issue number。")
	}
	body, redactions := sanitizeRepositoryIssueText(configToolString(input, "body"), repositoryIssueCommentLimit, false)
	result.Redactions = redactions
	if body == "" {
		return result.fail("invalid_input", "comment 必须提供非空 body。")
	}
	issue, apiErr := t.getIssue(ctx, repository, number)
	if apiErr != nil {
		return result.fail(apiErr.Code, t.failureMessage(apiErr.Code))
	}
	operationID := strings.TrimSpace(configToolString(input, "operation_id"))
	fingerprint, payloadHash, code, message := repositoryIssueFingerprint(repository, "comment:"+strconv.Itoa(number), operationID, map[string]any{
		"number": number, "body": body,
	})
	if code != "" {
		return result.fail(code, message)
	}
	result.Fingerprint = fingerprint
	marker := repositoryIssueOperationMarkerWithPayload("comment", fingerprint, payloadHash)
	legacyMarker := repositoryIssueOperationMarker("comment", fingerprint)
	markerPrefix := repositoryIssueOperationMarkerPrefix("comment", fingerprint)
	operationKey := fmt.Sprintf("%s:comment:%d:%s", strings.ToLower(repository), number, fingerprint)
	unlock := t.plugin.operationLock(operationKey)
	defer unlock()
	if existing, match, apiErr := t.findCommentMarker(ctx, repository, number, marker, legacyMarker, markerPrefix); apiErr != nil {
		return result.fail(apiErr.Code, t.failureMessage(apiErr.Code))
	} else if match == repositoryIssueMarkerExact {
		t.plugin.clearOperationUncertain(operationKey)
		result.OK = true
		result.Outcome = "reused"
		result.Message = "该评论操作已经由 GitHub 确认，未重复发布。"
		result.Issue = ptrRepositoryIssueSummary(repositoryIssueSummaryFromGitHub(issue))
		result.CommentURL = existing.HTMLURL
		result.Idempotent = true
		return result
	} else if match == repositoryIssueMarkerConflict && operationID != "" {
		return result.fail("operation_id_conflict", "operation_id 已用于不同的评论内容；请使用新的 operation_id。")
	}
	if t.plugin.operationUncertain(operationKey) {
		return result.fail("pending_reconciliation", "此前评论写入结果仍不确定；为避免重复评论，本操作只允许继续对账，请稍后再试或使用新的 operation_id。")
	}
	var created githubIssueComment
	apiErr = t.doJSON(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/issues/%d/comments", repository, number), map[string]any{
		"body": appendRepositoryIssueMarker(body, marker),
	}, &created)
	if apiErr == nil {
		result.OK = true
		result.Outcome = "commented"
		result.Message = "GitHub 已添加 Issue 评论。"
		result.Issue = ptrRepositoryIssueSummary(repositoryIssueSummaryFromGitHub(issue))
		result.CommentURL = created.HTMLURL
		return result
	}
	if apiErr.Uncertain {
		t.plugin.markOperationUncertain(operationKey)
		if existing, ok := t.reconcileCommentMarker(repository, number, marker, legacyMarker); ok {
			t.plugin.clearOperationUncertain(operationKey)
			result.OK = true
			result.Outcome = "reconciled"
			result.Message = "评论确认一度不确定，已通过远端操作标记对账，未重复发布。"
			result.Issue = ptrRepositoryIssueSummary(repositoryIssueSummaryFromGitHub(issue))
			result.CommentURL = existing.HTMLURL
			result.Idempotent = true
			result.Reconciled = true
			return result
		}
	}
	return result.fail(apiErr.Code, t.failureMessage(apiErr.Code))
}

func (t *dianaRepositoryIssuesTool) setState(ctx context.Context, repository string, input map[string]any, operation string) repositoryIssueResult {
	result := repositoryIssueResult{Operation: operation, Repository: repository, RequestedNumber: repositoryIssueNumber(input)}
	number := repositoryIssueNumber(input)
	if number <= 0 {
		return result.fail("invalid_input", operation+" 必须提供有效的 Issue number。")
	}
	targetState := "closed"
	if operation == "reopen" {
		targetState = "open"
	}
	current, apiErr := t.getIssue(ctx, repository, number)
	if apiErr != nil {
		return result.fail(apiErr.Code, t.failureMessage(apiErr.Code))
	}
	if current.State == targetState {
		result.OK = true
		result.Outcome = "unchanged"
		result.Message = "Issue 已处于目标状态，未重复修改。"
		result.Issue = ptrRepositoryIssueSummary(repositoryIssueSummaryFromGitHub(current))
		result.Idempotent = true
		return result
	}
	var updated githubRepositoryIssue
	if apiErr := t.doJSON(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/issues/%d", repository, number), map[string]any{"state": targetState}, &updated); apiErr != nil {
		return result.fail(apiErr.Code, t.failureMessage(apiErr.Code))
	}
	if !strings.EqualFold(updated.State, targetState) {
		return result.fail("invalid_response", repositoryIssueFailureMessage("invalid_response"))
	}
	result.OK = true
	result.Outcome = "closed"
	if operation == "reopen" {
		result.Outcome = "reopened"
	}
	result.Message = "GitHub 已" + map[bool]string{true: "重新打开", false: "关闭"}[operation == "reopen"] + " Issue。"
	result.Issue = ptrRepositoryIssueSummary(repositoryIssueSummaryFromGitHub(updated))
	return result
}

func (t *dianaRepositoryIssuesTool) listRecentIssues(ctx context.Context, repository string) ([]githubRepositoryIssue, *repositoryIssueAPIError) {
	readPage := func(page int) ([]githubRepositoryIssue, http.Header, *repositoryIssueAPIError) {
		values := url.Values{
			"state":     {"all"},
			"sort":      {"updated"},
			"direction": {"desc"},
			"per_page":  {strconv.Itoa(repositoryIssueListLimit)},
			"page":      {strconv.Itoa(page)},
		}
		var payload []githubRepositoryIssue
		headers, apiErr := t.doJSONWithHeaders(ctx, http.MethodGet, "/repos/"+repository+"/issues?"+values.Encode(), nil, &payload)
		return payload, headers, apiErr
	}
	payload, headers, apiErr := readPage(1)
	if apiErr != nil {
		return nil, apiErr
	}
	lastPage, paginationKnown := repositoryIssueLastPage(headers.Get("Link"))
	if !paginationKnown || lastPage > repositoryIssueListMaxPages {
		return nil, &repositoryIssueAPIError{Code: "idempotency_scan_incomplete"}
	}
	for page := 2; page <= lastPage; page++ {
		items, _, apiErr := readPage(page)
		if apiErr != nil {
			return nil, apiErr
		}
		payload = append(payload, items...)
	}
	issues := payload[:0]
	for _, issue := range payload {
		if issue.PullRequest == nil {
			if !validRepositoryIssueCanonicalURL(issue.HTMLURL, repository, "issues", issue.Number) {
				return nil, &repositoryIssueAPIError{Code: "invalid_response"}
			}
			issues = append(issues, issue)
		}
	}
	return issues, nil
}

func (t *dianaRepositoryIssuesTool) getIssue(ctx context.Context, repository string, number int) (githubRepositoryIssue, *repositoryIssueAPIError) {
	var issue githubRepositoryIssue
	apiErr := t.doJSON(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/issues/%d", repository, number), nil, &issue)
	if apiErr == nil && issue.PullRequest != nil {
		apiErr = &repositoryIssueAPIError{Code: "not_an_issue"}
	}
	return issue, apiErr
}

func (t *dianaRepositoryIssuesTool) findCommentMarker(ctx context.Context, repository string, number int, marker, legacyMarker, markerPrefix string) (githubIssueComment, repositoryIssueMarkerMatch, *repositoryIssueAPIError) {
	readPage := func(page int) ([]githubIssueComment, http.Header, *repositoryIssueAPIError) {
		values := url.Values{
			"per_page": {"100"},
			"page":     {strconv.Itoa(page)},
		}
		var comments []githubIssueComment
		headers, apiErr := t.doJSONWithHeaders(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/issues/%d/comments?%s", repository, number, values.Encode()), nil, &comments)
		return comments, headers, apiErr
	}
	find := func(comments []githubIssueComment) (githubIssueComment, repositoryIssueMarkerMatch) {
		for _, comment := range comments {
			if strings.Contains(comment.Body, marker) || (legacyMarker != "" && strings.Contains(comment.Body, legacyMarker)) {
				return comment, repositoryIssueMarkerExact
			}
			if markerPrefix != "" && strings.Contains(comment.Body, markerPrefix) {
				return comment, repositoryIssueMarkerConflict
			}
		}
		return githubIssueComment{}, repositoryIssueMarkerMissing
	}
	first, headers, apiErr := readPage(1)
	if apiErr != nil {
		return githubIssueComment{}, repositoryIssueMarkerMissing, apiErr
	}
	if comment, match := find(first); match != repositoryIssueMarkerMissing {
		return comment, match, nil
	}
	lastPage, paginationKnown := repositoryIssueLastPage(headers.Get("Link"))
	if !paginationKnown {
		return githubIssueComment{}, repositoryIssueMarkerMissing, &repositoryIssueAPIError{Code: "idempotency_scan_incomplete"}
	}
	if lastPage <= 1 {
		return githubIssueComment{}, repositoryIssueMarkerMissing, nil
	}
	last, _, apiErr := readPage(lastPage)
	if apiErr != nil {
		return githubIssueComment{}, repositoryIssueMarkerMissing, apiErr
	}
	if comment, match := find(last); match != repositoryIssueMarkerMissing {
		return comment, match, nil
	}
	if lastPage > repositoryIssueCommentMaxPages {
		return githubIssueComment{}, repositoryIssueMarkerMissing, &repositoryIssueAPIError{Code: "idempotency_scan_incomplete"}
	}
	for page := lastPage - 1; page >= 2; page-- {
		comments, _, apiErr := readPage(page)
		if apiErr != nil {
			return githubIssueComment{}, repositoryIssueMarkerMissing, apiErr
		}
		if comment, match := find(comments); match != repositoryIssueMarkerMissing {
			return comment, match, nil
		}
	}
	return githubIssueComment{}, repositoryIssueMarkerMissing, nil
}

func repositoryIssueLastPage(linkHeader string) (int, bool) {
	linkHeader = strings.TrimSpace(linkHeader)
	if linkHeader == "" {
		return 1, true
	}
	for _, item := range strings.Split(linkHeader, ",") {
		if !strings.Contains(item, `rel="last"`) {
			continue
		}
		start := strings.Index(item, "<")
		end := strings.Index(item, ">")
		if start < 0 || end <= start {
			continue
		}
		parsed, err := url.Parse(strings.TrimSpace(item[start+1 : end]))
		if err != nil {
			continue
		}
		page, err := strconv.Atoi(parsed.Query().Get("page"))
		if err == nil && page > 0 {
			return page, true
		}
	}
	return 0, false
}

func (t *dianaRepositoryIssuesTool) reconcileIssueMarker(repository, marker string) (githubRepositoryIssue, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), min(t.requestTimeout(), 10*time.Second))
	defer cancel()
	issues, apiErr := t.listRecentIssues(ctx, repository)
	if apiErr != nil {
		return githubRepositoryIssue{}, false
	}
	return repositoryIssueWithMarker(issues, marker)
}

func (t *dianaRepositoryIssuesTool) reconcileCommentMarker(repository string, number int, marker, legacyMarker string) (githubIssueComment, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), min(t.requestTimeout(), 10*time.Second))
	defer cancel()
	comment, match, _ := t.findCommentMarker(ctx, repository, number, marker, legacyMarker, "")
	return comment, match == repositoryIssueMarkerExact
}

func repositoryIssueWithMarker(issues []githubRepositoryIssue, marker string) (githubRepositoryIssue, bool) {
	for _, issue := range issues {
		if strings.Contains(issue.Body, marker) {
			return issue, true
		}
	}
	return githubRepositoryIssue{}, false
}

func repositoryIssueSummaryFromGitHub(issue githubRepositoryIssue) repositoryIssueSummary {
	labels := make([]string, 0, len(issue.Labels))
	for _, label := range issue.Labels {
		if name := strings.TrimSpace(label.Name); name != "" {
			labels = append(labels, name)
		}
	}
	sort.Strings(labels)
	return repositoryIssueSummary{
		Number:    issue.Number,
		Title:     strings.TrimSpace(issue.Title),
		State:     strings.TrimSpace(issue.State),
		URL:       strings.TrimSpace(issue.HTMLURL),
		Labels:    labels,
		UpdatedAt: issue.UpdatedAt,
	}
}

func ptrRepositoryIssueSummary(value repositoryIssueSummary) *repositoryIssueSummary {
	return &value
}

func repositoryIssueNumber(input map[string]any) int {
	value, ok := numberValue(input["number"])
	if !ok || value <= 0 || value != float64(int(value)) {
		return 0
	}
	return int(value)
}

func repositoryIssueStringList(input map[string]any, key string, limit int) ([]string, bool, string, string) {
	raw, present := input[key]
	if !present {
		return nil, false, "", ""
	}
	items, err := stringSliceValue(raw)
	if err != nil {
		return nil, true, "invalid_input", key + " 必须是字符串数组。"
	}
	if len(items) > limit {
		return nil, true, "invalid_input", fmt.Sprintf("%s 最多允许 %d 项。", key, limit)
	}
	out := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" || len([]rune(item)) > 100 {
			return nil, true, "invalid_input", key + " 包含空值或过长值。"
		}
		if _, redactions := sanitizeRepositoryIssueText(item, 100, true); redactions > 0 {
			return nil, true, "sensitive_input", key + " 包含疑似凭据或隐私标识，已拒绝公开写入。"
		}
		if !seen[item] {
			seen[item] = true
			out = append(out, item)
		}
	}
	return out, true, "", ""
}

func repositoryIssueMilestone(input map[string]any, allowNull bool) (int, bool, string, string) {
	raw, present := input["milestone"]
	if !present {
		return 0, false, "", ""
	}
	if raw == nil && allowNull {
		return 0, true, "", ""
	}
	value, ok := numberValue(raw)
	if !ok || value <= 0 || value != float64(int(value)) {
		return 0, true, "invalid_input", "milestone 必须是正整数；update 可传 null 清除。"
	}
	return int(value), true, "", ""
}

func boolInput(input map[string]any, key string) bool {
	value, _ := input[key].(bool)
	return value
}

func repositoryIssueFingerprint(repository, operation, operationID string, payload map[string]any) (string, string, string, string) {
	operationID = strings.TrimSpace(operationID)
	payloadBody, err := json.Marshal(payload)
	if err != nil {
		return "", "", "invalid_input", "无法生成稳定操作指纹。"
	}
	payloadSum := sha256.Sum256(payloadBody)
	payloadHash := hex.EncodeToString(payloadSum[:])
	var source []byte
	if operationID != "" {
		if len(operationID) > 128 {
			return "", "", "invalid_input", "operation_id 不能超过 128 个字符。"
		}
		for _, char := range operationID {
			if unicode.IsLetter(char) || unicode.IsDigit(char) || char == '-' || char == '_' || char == '.' || char == ':' {
				continue
			}
			return "", "", "invalid_input", "operation_id 只能包含字母、数字、点、冒号、下划线和连字符。"
		}
		source = []byte("client:" + operationID)
	} else {
		source = []byte("payload:" + payloadHash)
	}
	sum := sha256.Sum256(bytes.Join([][]byte{[]byte(strings.ToLower(repository)), []byte(operation), source}, []byte{0}))
	return hex.EncodeToString(sum[:]), payloadHash, "", ""
}

func repositoryIssueOperationMarker(operation, fingerprint string) string {
	return "<!-- diana-operation:" + operation + ":" + fingerprint + " -->"
}

func repositoryIssueOperationMarkerWithPayload(operation, fingerprint, payloadHash string) string {
	return "<!-- diana-operation:" + operation + ":" + fingerprint + ":" + payloadHash + " -->"
}

func repositoryIssueOperationMarkerPrefix(operation, fingerprint string) string {
	return "<!-- diana-operation:" + operation + ":" + fingerprint + ":"
}

func appendRepositoryIssueMarker(body, marker string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return marker
	}
	return body + "\n\n" + marker
}

func preserveRepositoryIssueCreateMarker(body, previous string) string {
	marker := repositoryIssueCreateMarkerPattern.FindString(previous)
	if marker == "" || strings.Contains(body, marker) {
		return body
	}
	return appendRepositoryIssueMarker(body, marker)
}

func repositoryIssueWithAnyMarker(issues []githubRepositoryIssue, markers ...string) (githubRepositoryIssue, bool) {
	for _, issue := range issues {
		for _, marker := range markers {
			if marker != "" && strings.Contains(issue.Body, marker) {
				return issue, true
			}
		}
	}
	return githubRepositoryIssue{}, false
}

func repositoryIssueWithMarkerPrefix(issues []githubRepositoryIssue, markerPrefix string) (githubRepositoryIssue, bool) {
	for _, issue := range issues {
		if markerPrefix != "" && strings.Contains(issue.Body, markerPrefix) {
			return issue, true
		}
	}
	return githubRepositoryIssue{}, false
}

func similarRepositoryIssues(issues []githubRepositoryIssue, title string, labels []string, now time.Time, limit int) []repositoryIssueSummary {
	type scoredIssue struct {
		summary repositoryIssueSummary
		score   float64
	}
	requestedLabels := map[string]bool{}
	for _, label := range labels {
		requestedLabels[strings.ToLower(strings.TrimSpace(label))] = true
	}
	result := make([]scoredIssue, 0, limit)
	for _, issue := range issues {
		if issue.State == "closed" && !issue.ClosedAt.IsZero() && now.Sub(issue.ClosedAt) > repositoryIssueRecentWindow {
			continue
		}
		score := repositoryIssueTitleSimilarity(title, issue.Title)
		labelMatch := false
		for _, label := range issue.Labels {
			if requestedLabels[strings.ToLower(strings.TrimSpace(label.Name))] {
				labelMatch = true
				break
			}
		}
		if score < 0.72 && !(labelMatch && score >= 0.55) {
			continue
		}
		if labelMatch {
			score += 0.1
		}
		result = append(result, scoredIssue{summary: repositoryIssueSummaryFromGitHub(issue), score: score})
	}
	sort.SliceStable(result, func(left, right int) bool {
		if result[left].score == result[right].score {
			return result[left].summary.UpdatedAt.After(result[right].summary.UpdatedAt)
		}
		return result[left].score > result[right].score
	})
	if len(result) > limit {
		result = result[:limit]
	}
	summaries := make([]repositoryIssueSummary, 0, len(result))
	for _, item := range result {
		summaries = append(summaries, item.summary)
	}
	return summaries
}

func repositoryIssueRequestMentionsCandidate(text string, candidates []repositoryIssueSummary) bool {
	numbers := repositoryIssueMentionedNumbers(text)
	for _, candidate := range candidates {
		if numbers[candidate.Number] {
			return true
		}
	}
	return false
}

func (t *dianaRepositoryIssuesTool) newDuplicateConfirmation(repository, fingerprint string, candidates []repositoryIssueSummary) string {
	if t == nil || t.plugin == nil || !t.plugin.confirmationOK {
		return ""
	}
	origin := repositoryIssueEventDigest(t.event)
	if origin == "" {
		return ""
	}
	expires := time.Now().Add(repositoryIssueConfirmationTTL).Unix()
	scope := repositoryIssueConfirmationScope(t.event.UserID, repository, fingerprint, repositoryIssueCandidatesDigest(candidates), origin, expires)
	mac := hmac.New(sha256.New, t.plugin.confirmationKey[:])
	_, _ = mac.Write([]byte(scope))
	return strings.Join([]string{
		"v1",
		origin,
		strconv.FormatInt(expires, 10),
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil)),
	}, ".")
}

func (t *dianaRepositoryIssuesTool) verifyDuplicateConfirmation(token, repository, fingerprint string, candidates []repositoryIssueSummary) bool {
	if t == nil || t.plugin == nil || !t.plugin.confirmationOK {
		return false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != "v1" {
		return false
	}
	expires, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || time.Now().Unix() > expires {
		return false
	}
	current := repositoryIssueEventDigest(t.event)
	if current == "" || hmac.Equal([]byte(current), []byte(parts[1])) {
		return false
	}
	provided, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	scope := repositoryIssueConfirmationScope(t.event.UserID, repository, fingerprint, repositoryIssueCandidatesDigest(candidates), parts[1], expires)
	mac := hmac.New(sha256.New, t.plugin.confirmationKey[:])
	_, _ = mac.Write([]byte(scope))
	return hmac.Equal(provided, mac.Sum(nil))
}

func repositoryIssueConfirmationScope(actor, repository, fingerprint, candidatesDigest, origin string, expires int64) string {
	return strings.Join([]string{actor, strings.ToLower(repository), fingerprint, candidatesDigest, origin, strconv.FormatInt(expires, 10)}, "\x00")
}

func repositoryIssueEventDigest(event MessageEvent) string {
	if strings.TrimSpace(event.MessageID) == "" {
		return ""
	}
	source := strings.Join([]string{
		event.Platform,
		event.ProfileID,
		event.ContextNamespace,
		string(event.Kind),
		event.GroupID,
		event.UserID,
		event.MessageID,
	}, "\x00")
	sum := sha256.Sum256([]byte(source))
	return hex.EncodeToString(sum[:])
}

func repositoryIssueCandidatesDigest(candidates []repositoryIssueSummary) string {
	items := append([]repositoryIssueSummary(nil), candidates...)
	sort.Slice(items, func(left, right int) bool { return items[left].Number < items[right].Number })
	hash := sha256.New()
	for _, item := range items {
		_, _ = io.WriteString(hash, strconv.Itoa(item.Number))
		_, _ = io.WriteString(hash, "\x00"+strings.ToLower(strings.TrimSpace(item.State))+"\x00"+strings.TrimSpace(item.Title)+"\x00")
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func repositoryIssueTitleSimilarity(left, right string) float64 {
	leftCompact := repositoryIssueCompactTitle(left)
	rightCompact := repositoryIssueCompactTitle(right)
	if leftCompact == "" || rightCompact == "" {
		return 0
	}
	if leftCompact == rightCompact {
		return 1
	}
	leftTerms := repositoryIssueTitleTerms(left)
	rightTerms := repositoryIssueTitleTerms(right)
	if len(leftTerms) == 0 || len(rightTerms) == 0 {
		return 0
	}
	intersection := 0
	for term := range leftTerms {
		if rightTerms[term] {
			intersection++
		}
	}
	if intersection < 2 {
		return 0
	}
	return float64(2*intersection) / float64(len(leftTerms)+len(rightTerms))
}

func repositoryIssueCompactTitle(value string) string {
	var builder strings.Builder
	for _, char := range strings.ToLower(value) {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			builder.WriteRune(char)
		}
	}
	return builder.String()
}

func repositoryIssueTitleTerms(value string) map[string]bool {
	terms := map[string]bool{}
	var ascii strings.Builder
	flushASCII := func() {
		if text := ascii.String(); len(text) >= 2 {
			terms[text] = true
		}
		ascii.Reset()
	}
	var han []rune
	flushHan := func() {
		for index := range han {
			terms[string(han[index])] = true
			if index+1 < len(han) {
				terms[string(han[index:index+2])] = true
			}
		}
		han = han[:0]
	}
	for _, char := range strings.ToLower(value) {
		switch {
		case char <= unicode.MaxASCII && (unicode.IsLetter(char) || unicode.IsDigit(char)):
			flushHan()
			ascii.WriteRune(char)
		case unicode.Is(unicode.Han, char):
			flushASCII()
			han = append(han, char)
		default:
			flushASCII()
			flushHan()
		}
	}
	flushASCII()
	flushHan()
	return terms
}

func (t *dianaRepositoryIssuesTool) requestTimeout() time.Duration {
	seconds := t.settings.Int(repositoryPublishSettingTimeout, defaultRepositoryPublishTimeoutSecs)
	if seconds <= 0 {
		seconds = defaultRepositoryPublishTimeoutSecs
	}
	return time.Duration(seconds) * time.Second
}

func (t *dianaRepositoryIssuesTool) doJSON(ctx context.Context, method, path string, payload any, target any) *repositoryIssueAPIError {
	_, apiErr := t.doJSONWithHeaders(ctx, method, path, payload, target)
	return apiErr
}

func (t *dianaRepositoryIssuesTool) doJSONWithHeaders(ctx context.Context, method, path string, payload any, target any) (http.Header, *repositoryIssueAPIError) {
	requestCtx, cancel := context.WithTimeout(ctx, t.requestTimeout())
	defer cancel()
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, &repositoryIssueAPIError{Code: "invalid_input"}
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(requestCtx, method, t.plugin.baseURL+path, body)
	if err != nil {
		return nil, &repositoryIssueAPIError{Code: "invalid_request"}
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "Diana-Repository-Issues")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	token, credentialErr := t.repositoryPublishCredential(requestCtx, repositoryFromGitHubAPIPath(path))
	if credentialErr != nil {
		return nil, credentialErr
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := t.plugin.client.Do(req)
	if err != nil {
		return nil, repositoryIssueTransportError(requestCtx, err, method == http.MethodPost)
	}
	defer resp.Body.Close()
	headers := resp.Header.Clone()
	expectedStatus := http.StatusOK
	if method == http.MethodPost {
		expectedStatus = http.StatusCreated
	}
	if resp.StatusCode != expectedStatus {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		responseText := strings.ToLower(string(responseBody))
		code := "github_api_error"
		switch resp.StatusCode {
		case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
			code = "redirect_refused"
		case http.StatusRequestTimeout:
			code = "timeout"
		case http.StatusUnauthorized:
			code = "unauthorized"
		case http.StatusForbidden:
			if strings.TrimSpace(resp.Header.Get("X-RateLimit-Remaining")) == "0" || strings.TrimSpace(resp.Header.Get("Retry-After")) != "" || strings.Contains(responseText, "rate limit") {
				code = "rate_limited"
			} else {
				code = "permission_denied"
			}
		case http.StatusTooManyRequests:
			code = "rate_limited"
		case http.StatusNotFound:
			code = "not_found"
		case http.StatusGone:
			code = "gone"
		case http.StatusUnprocessableEntity:
			if strings.Contains(responseText, "spam") {
				code = "rate_limited"
			} else {
				code = "validation_failed"
			}
		default:
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				code = "invalid_response"
			} else if resp.StatusCode >= 500 {
				code = "github_unavailable"
			}
		}
		return headers, &repositoryIssueAPIError{
			Code:      code,
			Status:    resp.StatusCode,
			Uncertain: method == http.MethodPost && (resp.StatusCode >= 500 || resp.StatusCode >= 200 && resp.StatusCode < 400),
		}
	}
	if target == nil {
		return headers, nil
	}
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, repositoryIssueResponseLimit+1))
	if err != nil {
		return headers, repositoryIssueTransportError(requestCtx, err, method == http.MethodPost)
	}
	if len(responseBody) > repositoryIssueResponseLimit {
		return headers, &repositoryIssueAPIError{Code: "invalid_response", Status: resp.StatusCode, Uncertain: method == http.MethodPost}
	}
	if err := json.Unmarshal(responseBody, target); err != nil || !validRepositoryIssueAPIResponse(method, path, target) {
		return headers, &repositoryIssueAPIError{Code: "invalid_response", Status: resp.StatusCode, Uncertain: method == http.MethodPost}
	}
	return headers, nil
}

func (t *dianaRepositoryIssuesTool) repositoryPublishCredential(ctx context.Context, repository string) (string, *repositoryIssueAPIError) {
	userID := strings.TrimSpace(t.event.UserID)
	tokens, _ := repositoryPublishUserTokens(t.settings.String(repositoryPublishSettingUserTokens, ""))
	modes, _ := repositoryPublishUserAuthModes(t.settings.String(repositoryPublishSettingUserAuth, ""))
	userMode := modes[userID]
	if userMode == "" || userMode == repositoryPublishAuthToken {
		if token := strings.TrimSpace(tokens[userID]); token != "" {
			t.credentialSource = "用户 " + userID + " 的 Token"
			return token, nil
		}
	}
	if userMode == repositoryPublishAuthGH {
		t.credentialSource = "gh CLI"
		return t.repositoryPublishGHCredential(ctx)
	}
	// 用户自己配了 Token 的情况上面已经处理；到这里先看目标仓库有没有绑定凭据。
	if credential, credentialToken, ok := t.repositoryBoundCredential(repository); ok {
		if credential.authMode() == repositoryCredentialAuthGH {
			t.credentialSource = "凭据「" + credential.label() + "」（gh CLI）"
			return t.repositoryPublishGHCredential(ctx)
		}
		t.credentialSource = "凭据「" + credential.label() + "」"
		return credentialToken, nil
	}
	token := strings.TrimSpace(t.settings.String(repositoryPublishSettingToken, ""))
	t.credentialSource = "公共 GitHub Token"
	if token == "" {
		if token = t.sharedGitHubToken(); token != "" {
			t.credentialSource = "公共 GitHub Token（来自仓库订阅插件）"
		}
	}
	mode := repositoryPublishAuthMode(t.settings)
	if mode == repositoryPublishAuthToken || mode == repositoryPublishAuthAuto && token != "" {
		if token == "" {
			return "", &repositoryIssueAPIError{Code: "token_required"}
		}
		return token, nil
	}
	t.credentialSource = "gh CLI"
	return t.repositoryPublishGHCredential(ctx)
}

// sharedGitHubToken 回落到「仓库订阅」插件里的 Token。
//
// 「GitHub 仓库 · 设置」把两个插件呈现成同一个「公共 Token」，界面上明写它同时用于
// 仓库更新检查和 Issue 创建。但两个插件各存各的：前端只在本次真的重新输入了 Token
// 时，才顺手往发布插件也写一份，而那个输入框每次保存后都会清空、显示成「已配置 —
// 留空沿用」。于是先配好 Token、之后再改别的设置并保存，发布插件这边始终是空的；
// 「已配置」的提示又是「两个插件任一有就算」，结果就是界面说配好了、Issue 却用不了。
// 与其指望前端每次都能镜像过去，不如让读取侧兑现界面的承诺。
func (t *dianaRepositoryIssuesTool) sharedGitHubToken() string {
	return strings.TrimSpace(t.watchSettings().String(repositoryWatchSettingToken, ""))
}

// watchSettings 取回「仓库订阅」插件的设置。凭据列表和仓库绑定都存在那边——界面上
// 它们同属一个「GitHub 仓库 · 设置」，仓库本身也归订阅插件管。
func (t *dianaRepositoryIssuesTool) watchSettings() SettingValues {
	if t == nil || t.runtime == nil || t.runtime.plugins == nil {
		return nil
	}
	_, settings, enabled := t.runtime.pluginWithSettingsForEvent(repositoryWatchPluginID, t.event)
	if !enabled {
		return nil
	}
	return settings
}

// repositoryBoundCredential 返回目标仓库单独绑定的凭据。没绑定就返回 false，调用方
// 继续走原来的用户 Token / 公共 Token / gh 顺序。
func (t *dianaRepositoryIssuesTool) repositoryBoundCredential(repository string) (repositoryCredential, string, bool) {
	settings := t.watchSettings()
	if settings == nil {
		return repositoryCredential{}, "", false
	}
	return repositoryCredentialFor(repository, settings)
}

func (t *dianaRepositoryIssuesTool) repositoryPublishGHCredential(ctx context.Context) (string, *repositoryIssueAPIError) {
	if t.plugin == nil || t.plugin.ghAuthToken == nil {
		return "", &repositoryIssueAPIError{Code: "gh_unavailable"}
	}
	token, err := t.plugin.ghAuthToken(ctx)
	if err == nil && strings.TrimSpace(token) != "" {
		return strings.TrimSpace(token), nil
	}
	if errors.Is(err, errRepositoryPublishGHUnavailable) {
		return "", &repositoryIssueAPIError{Code: "gh_unavailable"}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "", &repositoryIssueAPIError{Code: "timeout"}
	}
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return "", &repositoryIssueAPIError{Code: "cancelled"}
	}
	return "", &repositoryIssueAPIError{Code: "gh_auth_required"}
}

func repositoryIssueTransportError(ctx context.Context, err error, uncertain bool) *repositoryIssueAPIError {
	code := "network_error"
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		code = "timeout"
	} else if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		code = "cancelled"
	}
	return &repositoryIssueAPIError{Code: code, Uncertain: uncertain}
}

func validRepositoryIssueAPIResponse(method, path string, target any) bool {
	switch value := target.(type) {
	case *githubRepositoryIssue:
		resource := "issues"
		if value != nil && value.PullRequest != nil {
			resource = "pull"
		}
		if value == nil || value.Number <= 0 || !validRepositoryIssueCanonicalURL(value.HTMLURL, repositoryIssueRepositoryFromAPIPath(path), resource, value.Number) {
			return false
		}
		if expected := repositoryIssueNumberFromAPIPath(path); expected > 0 && expected != value.Number {
			return false
		}
	case *githubIssueComment:
		if value == nil || strings.TrimSpace(value.HTMLURL) == "" {
			return false
		}
		parsed, err := url.Parse(value.HTMLURL)
		expectedRepository := repositoryIssueRepositoryFromAPIPath(path)
		expectedNumber := repositoryIssueNumberFromAPIPath(path)
		if err != nil || parsed.RawQuery != "" || !strings.EqualFold(parsed.Scheme, "https") || !strings.EqualFold(parsed.Hostname(), "github.com") ||
			!strings.EqualFold(strings.TrimRight(parsed.Path, "/"), "/"+expectedRepository+"/issues/"+strconv.Itoa(expectedNumber)) ||
			!strings.HasPrefix(parsed.Fragment, "issuecomment-") {
			return false
		}
	}
	return true
}

func repositoryIssueNumberFromAPIPath(path string) int {
	path = strings.SplitN(path, "?", 2)[0]
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 5 || parts[0] != "repos" || parts[3] != "issues" {
		return 0
	}
	number, _ := strconv.Atoi(parts[4])
	return number
}

func repositoryIssueRepositoryFromAPIPath(path string) string {
	path = strings.SplitN(path, "?", 2)[0]
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 4 || parts[0] != "repos" || parts[3] != "issues" {
		return ""
	}
	return parts[1] + "/" + parts[2]
}

func validRepositoryIssueCanonicalURL(raw, repository, resource string, number int) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || repository == "" || parsed.RawQuery != "" || parsed.Fragment != "" || !strings.EqualFold(parsed.Scheme, "https") || !strings.EqualFold(parsed.Hostname(), "github.com") {
		return false
	}
	return strings.EqualFold(strings.TrimRight(parsed.Path, "/"), "/"+repository+"/"+resource+"/"+strconv.Itoa(number))
}

// failureMessage 在凭据相关的报错后面补一句「本次用的是哪种凭据」。配了 Token 却
// 报 404 时，这句话直接指出该去查哪一份配置；只报来源，不含 Token 本身。
func (t *dianaRepositoryIssuesTool) failureMessage(code string) string {
	message := repositoryIssueFailureMessage(code)
	source := ""
	if t != nil {
		source = strings.TrimSpace(t.credentialSource)
	}
	switch code {
	case "not_found", "unauthorized", "permission_denied":
		if source != "" {
			return message + "（本次凭据：" + source + "）"
		}
	}
	return message
}

func repositoryIssueFailureMessage(code string) string {
	switch code {
	case "unauthorized":
		return "当前 GitHub 凭据无效或已过期。"
	case "permission_denied":
		return "GitHub 拒绝了操作；请确认当前凭据对目标仓库具有 Issues 所需权限。"
	case "token_required":
		return "当前认证方式要求配置 GitHub Token，请在「GitHub 仓库 · 设置」里填写。"
	case "gh_unavailable":
		return "当前系统未安装 gh，无法使用 GitHub CLI 认证。"
	case "gh_auth_required":
		return "gh 尚未登录 github.com 或登录凭据不可用，请先执行 gh auth login。"
	case "rate_limited":
		return "GitHub API 已限流，请稍后再试。"
	case "not_found":
		// GitHub 对「看不到的私有仓库」和「不存在的仓库」都回 404，不区分二者是它
		// 的防探测设计。仓库能走到这一步说明已经过了白名单，所以凭据看不到的可能性
		// 通常更大，别让人以为是自己链接写错了。
		return "GitHub 返回 404：仓库或 Issue 不存在，或当前 GitHub 凭据看不到它。私有仓库没有授权给该 Token 时同样是 404，请先确认 Token 覆盖了这个仓库。"
	case "not_an_issue":
		return "目标编号属于 Pull Request；本工具只允许修改 Issue。"
	case "gone":
		return "GitHub 端点或资源已不可用。"
	case "redirect_refused":
		return "GitHub 返回了仓库重定向；为避免跨仓库误写，操作已停止，请更新并重新确认目标 allowlist。"
	case "validation_failed":
		return "GitHub 拒绝了字段校验；请检查标题、标签、负责人或里程碑。"
	case "operation_id_conflict":
		return "operation_id 已绑定到不同内容，不能复用。"
	case "idempotency_scan_incomplete":
		return "评论历史过多，未能完整核对幂等标记；为避免重复发布，操作已停止。"
	case "pending_reconciliation":
		return "此前写入结果仍不确定；为避免重复发布，当前只允许继续对账。"
	case "timeout":
		return "GitHub 请求超时，且未能通过远端操作标记确认结果；重试会先再次对账。"
	case "cancelled":
		return "操作已取消，且未能确认 GitHub 是否接收。"
	case "network_error":
		return "GitHub 网络请求失败，且未能通过远端操作标记确认结果。"
	case "github_unavailable":
		return "GitHub 暂时不可用，且未能确认写入结果。"
	case "invalid_response":
		return "GitHub 返回了无法解析的响应；写操作不会盲目重试。"
	default:
		return "GitHub API 请求失败。"
	}
}

func sanitizeRepositoryIssueText(value string, limit int, singleLine bool) (string, int) {
	value = strings.TrimSpace(value)
	redactions := 0
	value, redactions = replaceRepositoryIssueSensitive(value, repositoryIssueCQPattern, "[REDACTED_MEDIA]", redactions)
	value, redactions = replaceRepositoryIssueSensitive(value, repositoryIssuePrivateKeyPattern, "[REDACTED_PRIVATE_KEY]", redactions)
	value, redactions = replaceRepositoryIssueSensitive(value, repositoryIssueGitHubTokenPattern, "[REDACTED_TOKEN]", redactions)
	value, redactions = replaceRepositoryIssueSensitive(value, repositoryIssueCommonTokenPattern, "[REDACTED_TOKEN]", redactions)
	value, redactions = replaceRepositoryIssueSensitive(value, repositoryIssueBearerPattern, "Bearer [REDACTED]", redactions)
	value, redactions = replaceRepositoryIssueSensitive(value, repositoryIssueQuotedCredentialPattern, `"$1":"[REDACTED]"`, redactions)
	value, redactions = replaceRepositoryIssueSensitive(value, repositoryIssueQuotedValueCredentialPattern, "$1=[REDACTED]", redactions)
	value, redactions = replaceRepositoryIssueSensitive(value, repositoryIssueAuthorizationPattern, "Authorization=[REDACTED]", redactions)
	value, redactions = replaceRepositoryIssueSensitive(value, repositoryIssueCredentialPattern, "$1=[REDACTED]", redactions)
	value, redactions = replaceRepositoryIssueSensitive(value, repositoryIssueEmailPattern, "[REDACTED_EMAIL]", redactions)
	value, redactions = replaceRepositoryIssueSensitive(value, repositoryIssuePhonePattern, "[REDACTED_PHONE]", redactions)
	value, redactions = replaceRepositoryIssueSensitive(value, repositoryIssueIPv4Pattern, "[REDACTED_IP]", redactions)
	value, redactions = replaceRepositoryIssueSensitive(value, repositoryIssueRuntimeIDPattern, "$1=[REDACTED_ID]", redactions)
	value, redactions = replaceRepositoryIssueSensitive(value, repositoryIssueUUIDPattern, "[REDACTED_ID]", redactions)
	value, signedURLRedactions := redactRepositoryIssueSignedURLs(value)
	redactions += signedURLRedactions
	if singleLine {
		value = strings.Join(strings.Fields(value), " ")
	}
	value = truncateRunes(strings.TrimSpace(value), limit)
	return value, redactions
}

func replaceRepositoryIssueSensitive(value string, pattern *regexp.Regexp, replacement string, count int) (string, int) {
	matches := pattern.FindAllStringIndex(value, -1)
	if len(matches) == 0 {
		return value, count
	}
	return pattern.ReplaceAllString(value, replacement), count + len(matches)
}

func redactRepositoryIssueSignedURLs(value string) (string, int) {
	count := 0
	redacted := repositoryIssueURLPattern.ReplaceAllStringFunc(value, func(raw string) string {
		trimmed := strings.TrimRight(raw, ".,;:!?)，。；：！？）")
		trailing := strings.TrimPrefix(raw, trimmed)
		parsed, err := url.Parse(trimmed)
		if err != nil {
			return raw
		}
		sensitive := parsed.User != nil
		for key := range parsed.Query() {
			key = strings.ToLower(strings.TrimSpace(key))
			if strings.Contains(key, "token") || strings.Contains(key, "signature") || strings.Contains(key, "credential") || strings.Contains(key, "secret") || strings.Contains(key, "auth") || strings.Contains(key, "x-amz-") || key == "sig" || key == "expires" || key == "key" {
				sensitive = true
				break
			}
		}
		if !sensitive {
			return raw
		}
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.Fragment = ""
		count++
		return parsed.String() + trailing
	})
	return redacted, count
}

func (t *dianaRepositoryIssuesTool) audit(result repositoryIssueResult) {
	if t == nil || t.runtime == nil {
		return
	}
	writer := t.runtime.appLogWriter()
	if writer == nil {
		return
	}
	kind := applog.KindOperation
	level := applog.LevelInfo
	message := "GitHub Issue 操作完成"
	if !result.OK {
		kind = applog.KindError
		level = applog.LevelError
		message = "GitHub Issue 操作失败"
	}
	metadata := map[string]any{
		"repository":   result.Repository,
		"operation":    result.Operation,
		"outcome":      result.Outcome,
		"failure_code": result.FailureCode,
		"fingerprint":  result.Fingerprint,
		"idempotent":   result.Idempotent,
		"reconciled":   result.Reconciled,
		"redactions":   result.Redactions,
	}
	target := result.Repository
	if result.RequestedNumber > 0 {
		metadata["issue_number"] = result.RequestedNumber
	}
	if len(result.RequestedNumbers) > 0 {
		metadata["issue_numbers"] = result.RequestedNumbers
		metadata["failed_count"] = len(result.Failures)
	}
	if result.Issue != nil {
		metadata["issue_number"] = result.Issue.Number
		metadata["issue_url"] = result.Issue.URL
		target = result.Issue.URL
	}
	if result.CommentURL != "" {
		metadata["comment_url"] = result.CommentURL
	}
	logCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = writer.AppendLog(logCtx, applog.Entry{
		Kind:     kind,
		Level:    level,
		Action:   "diana.repository_issue",
		Message:  message,
		Actor:    oneBotEventActor(t.event),
		Target:   target,
		Metadata: metadata,
	})
}
