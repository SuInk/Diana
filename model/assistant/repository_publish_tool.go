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

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/applog"
)

const (
	dianaRepositoryIssuesToolName  = "diana.repository_issues"
	repositoryIssueBodyLimit       = 60_000
	repositoryIssueTitleLimit      = 256
	repositoryIssueCommentLimit    = 60_000
	repositoryIssueListLimit       = 100
	repositoryIssueRecentWindow    = 90 * 24 * time.Hour
	repositoryIssueCommentMaxPages = 100
	repositoryIssueListMaxPages    = 10
	repositoryIssueConfirmationTTL = 15 * time.Minute
	repositoryIssueResponseLimit   = 16 << 20
	repositoryIssueCredentialKey   = `(?:[a-z0-9]+[_-])*(?:authorization|api[_-]?key|access[_-]?key|access[_-]?token|refresh[_-]?token|client[_-]?secret|private[_-]?key|token|secret|password|passwd)(?:[_-][a-z0-9]+)*`
)

var (
	repositoryIssueCreateMarkerPattern          = regexp.MustCompile(`<!--\s*diana-operation:create:([a-f0-9]{64})(?::[a-f0-9]{64})?\s*-->`)
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
}

type repositoryIssueResult struct {
	OK                   bool                     `json:"ok"`
	Operation            string                   `json:"operation"`
	Outcome              string                   `json:"outcome,omitempty"`
	Repository           string                   `json:"repository,omitempty"`
	RequestedNumber      int                      `json:"requested_number,omitempty"`
	FailureCode          string                   `json:"failure_code,omitempty"`
	Message              string                   `json:"message"`
	Issue                *repositoryIssueSummary  `json:"issue,omitempty"`
	Items                []repositoryIssueSummary `json:"items,omitempty"`
	CommentURL           string                   `json:"comment_url,omitempty"`
	Fingerprint          string                   `json:"fingerprint,omitempty"`
	Idempotent           bool                     `json:"idempotent,omitempty"`
	Reconciled           bool                     `json:"reconciled,omitempty"`
	RequiresConfirmation bool                     `json:"requires_confirmation,omitempty"`
	ConfirmationToken    string                   `json:"confirmation_token,omitempty"`
	Redactions           int                      `json:"redactions,omitempty"`
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
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
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
	return `搜索和管理显式选定 GitHub 仓库的 Issues，仅主人或插件设置中获授权的用户可用。operation=search 是只读操作：{"operation":"search","repository":"owner/repo","query":"关键词","state":"open|closed|all"}。写操作的当前用户消息必须明确写出同一个 owner/repo；update/comment/close/reopen 还必须点名同一 Issue 编号。create 传 title/body 及用户明确要求的 labels/assignees/milestone/operation_id；update 只传当前消息点名且包含新值的 title/body/labels/assignees/milestone；comment 的 body 必须来自当前消息；close 或 reopen 传 number。create 会查找 open 和近期 closed Issue；发现候选时返回 requires_confirmation、候选和 confirmation_token。先向用户展示候选，只有下一条用户消息点名候选编号并明确坚持另行新建时，才把原 token 连同 allow_duplicate=true 传回。不要把完整聊天记录、运行时 ID、凭据或私密原文放进 title/body/comment。写入只允许插件设置白名单及用户授权范围内的精确 owner/repo，并返回结构化结果。`
}

func (t *dianaRepositoryIssuesTool) ExplicitUserRequestKind(input map[string]any) string {
	switch normalizeRepositoryIssueOperation(configToolString(input, "operation"), configToolString(input, "state")) {
	case "create":
		if boolInput(input, "allow_duplicate") {
			return "repository_issue_create_duplicate"
		}
		return "repository_issue_create"
	case "update":
		return "repository_issue_update"
	case "comment":
		return "repository_issue_comment"
	case "close":
		return "repository_issue_close"
	case "reopen":
		return "repository_issue_reopen"
	default:
		return ""
	}
}

func (t *dianaRepositoryIssuesTool) Run(ctx context.Context, input map[string]any) (string, error) {
	operation := normalizeRepositoryIssueOperation(configToolString(input, "operation"), configToolString(input, "state"))
	result := repositoryIssueResult{Operation: operation, Message: "GitHub Issue 操作未执行。"}
	if operation == "" {
		return t.finish(ctx, result.fail("invalid_operation", "operation 必须是 search、create、update、comment、close 或 reopen。"))
	}
	if t == nil || t.runtime == nil || t.plugin == nil || t.plugin.client == nil {
		return t.finish(ctx, result.fail("plugin_unavailable", "仓库 Issue 发布插件未正确配置。"))
	}
	repository, err := normalizeGitHubRepository(configToolString(input, "repository"))
	if err != nil {
		return t.finish(ctx, result.fail("invalid_repository", err.Error()))
	}
	result.Repository = repository
	owner := t.runtime.relationshipPolicy(ctx, t.event).Owner
	if code, message := repositoryPublishValidateUserAccess(t.event.UserID, repository, owner, t.settings); code != "" {
		return t.finish(ctx, result.fail(code, message))
	}
	if operation != "create" && operation != "search" {
		result.RequestedNumber = repositoryIssueNumber(input)
	}
	if operation == "search" {
		return t.finish(ctx, t.search(ctx, repository, input))
	}
	requestText := repositoryIssueCurrentRequestText(t.event)
	requestKind := t.ExplicitUserRequestKind(input)
	if !agent.ExplicitUserMutationRequested(requestText, requestKind) {
		return t.finish(ctx, result.fail("explicit_request_required", "当前用户消息没有明确要求执行这项 Issue 写操作。"))
	}
	if code, message := validateRepositoryIssueWriteRequest(requestText, operation, repository, input); code != "" {
		return t.finish(ctx, result.fail(code, message))
	}
	if code, message := t.validateWriteAccess(repository); code != "" {
		return t.finish(ctx, result.fail(code, message))
	}

	switch operation {
	case "create":
		result = t.create(ctx, repository, input)
	case "update":
		result = t.update(ctx, repository, input)
	case "comment":
		result = t.comment(ctx, repository, input)
	case "close", "reopen":
		result = t.setState(ctx, repository, input, operation)
	}
	return t.finish(ctx, result)
}

func normalizeRepositoryIssueOperation(operation, state string) string {
	switch strings.ToLower(strings.TrimSpace(operation)) {
	case "search", "find", "list":
		return "search"
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

func validateRepositoryIssueWriteRequest(text, operation, repository string, input map[string]any) (string, string) {
	repositories := repositoryIssueMentionedRepositories(text)
	if len(repositories) != 1 || !repositories[strings.ToLower(repository)] {
		return "explicit_target_required", "当前用户消息必须明确且无歧义地写出唯一目标 owner/repo，不能包含其他候选仓库。"
	}
	if operation != "create" {
		number := repositoryIssueNumber(input)
		numbers := repositoryIssueMentionedNumbers(text)
		if number <= 0 || len(numbers) != 1 || !numbers[number] {
			return "explicit_target_required", "当前用户消息必须明确且无歧义地写出唯一 Issue 编号。"
		}
	}
	if operation == "create" {
		if !repositoryIssueRequestContainsFieldValue(text, "title", configToolString(input, "title")) {
			return "explicit_fields_required", "创建 Issue 时，当前用户消息必须包含要发布的 title 内容。"
		}
		if body := configToolString(input, "body"); body != "" && !repositoryIssueRequestContainsFieldValue(text, "body", body) {
			return "explicit_fields_required", "创建 Issue 时，当前用户消息必须包含要发布的 body 内容。"
		}
	}
	if operation == "update" {
		for _, field := range []string{"title", "body"} {
			if _, present := input[field]; !present {
				continue
			}
			if !repositoryIssueRequestContainsFieldValue(text, field, configToolString(input, field)) {
				return "explicit_fields_required", "更新 Issue 时，当前用户消息必须明确点名字段并包含要写入的 title/body 内容。"
			}
		}
	}
	if operation == "comment" && !repositoryIssueRequestContainsCommentBody(text, configToolString(input, "body")) {
		return "explicit_fields_required", "添加评论时，当前用户消息必须包含要发布的评论内容。"
	}
	if code, message := validateRepositoryIssueMetadataRequest(text, operation, input); code != "" {
		return code, message
	}
	return "", ""
}

func repositoryIssueRequestMentionsRepository(text, repository string) bool {
	repositories := repositoryIssueMentionedRepositories(text)
	return len(repositories) == 1 && repositories[strings.ToLower(repository)]
}

func repositoryIssueRequestMentionsNumber(text string, number int) bool {
	mentions := repositoryIssueMentionedNumbers(text)
	return len(mentions) == 1 && mentions[number]
}

func repositoryIssueMentionedRepositories(text string) map[string]bool {
	result := map[string]bool{}
	collect := func(pattern *regexp.Regexp) {
		for _, match := range pattern.FindAllStringSubmatchIndex(text, -1) {
			if len(match) < 4 || match[2] < 0 {
				continue
			}
			repository, err := normalizeGitHubRepository(text[match[2]:match[3]])
			if err != nil {
				continue
			}
			key := strings.ToLower(repository)
			nonNegated := !repositoryIssuePrefixNegates(text[:match[2]])
			if previous, seen := result[key]; seen {
				result[key] = previous && nonNegated
			} else {
				result[key] = nonNegated
			}
		}
	}
	collect(repositoryIssueGitHubRepositoryPattern)
	collect(repositoryIssuePlainRepositoryPattern)
	return result
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
		nonNegated := !repositoryIssuePrefixNegates(text[:match[2]])
		if previous, seen := result[number]; seen {
			result[number] = previous && nonNegated
		} else {
			result[number] = nonNegated
		}
	}
	return result
}

func repositoryIssuePrefixNegates(prefix string) bool {
	window := strings.ToLower(prefix)
	cut := 0
	for _, separator := range []string{";", "；", "。", ".", "!", "！", "?", "？", "\n"} {
		if index := strings.LastIndex(window, separator); index >= 0 && index+len(separator) > cut {
			cut = index + len(separator)
		}
	}
	window = window[cut:]
	trimmed := strings.TrimSpace(window)
	for _, marker := range []string{"do not", "don't", "dont", "not", "never", "avoid", "without", "but", "except", "exclude", "excluding", "rather than"} {
		if strings.HasSuffix(trimmed, marker) {
			return true
		}
	}
	for _, marker := range []string{" do not ", "do not ", " don't ", "don't ", " dont ", "dont ", " not ", "not ", " never ", "never ", " avoid ", "avoid ", " without ", "without ", " but ", "but ", "except ", "exclude ", "excluding ", "rather than ", "不要", "请勿", "勿", "不得", "禁止", "严禁", "不是", "别", "而非", "排除", "不用", "不选", "除外", "除了"} {
		if strings.Contains(window, marker) {
			return true
		}
	}
	return false
}

func repositoryIssueRequestMentionsField(text, field string) bool {
	lower := strings.ToLower(text)
	for _, term := range repositoryIssueFieldTerms(field) {
		if strings.Contains(lower, term) {
			return true
		}
	}
	return false
}

func repositoryIssueRequestContainsValue(text, value string) bool {
	value = strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
	if value == "" {
		lower := strings.ToLower(text)
		for _, term := range []string{"clear", "remove", "delete", "empty", "清空", "删除", "移除", "置空"} {
			if strings.Contains(lower, term) {
				return true
			}
		}
		return false
	}
	normalizedText := strings.Join(strings.Fields(strings.ToLower(text)), " ")
	if !repositoryIssueASCIIIdentifier(value) {
		searchFrom := 0
		for {
			index := strings.Index(normalizedText[searchFrom:], value)
			if index < 0 {
				return false
			}
			index += searchFrom
			if !repositoryIssuePrefixNegates(normalizedText[:index]) {
				return true
			}
			searchFrom = index + len(value)
		}
	}
	pattern := regexp.MustCompile(`(?i)(?:^|[^a-z0-9_.-])` + regexp.QuoteMeta(value) + `(?:[^a-z0-9_.-]|$)`)
	for _, match := range pattern.FindAllStringIndex(normalizedText, -1) {
		if !repositoryIssuePrefixNegates(normalizedText[:match[0]]) {
			return true
		}
	}
	return false
}

func repositoryIssueRequestContainsPayloadValue(text, value string) bool {
	text = repositoryIssueGitHubRepositoryPattern.ReplaceAllString(text, " ")
	text = repositoryIssuePlainRepositoryPattern.ReplaceAllString(text, " ")
	text = repositoryIssueNumberMentionPattern.ReplaceAllString(text, " ")
	return repositoryIssueRequestContainsValue(text, value)
}

func repositoryIssueRequestContainsFieldValue(text, field, value string) bool {
	if strings.TrimSpace(value) == "" {
		return repositoryIssueRequestExplicitlyClearsField(text, field)
	}
	activeField := ""
	assignmentActive := false
	var selectedPayloads []string
	selected := false
	for _, clause := range repositoryIssueRequestClauses(text) {
		fields := repositoryIssueFieldsMentioned(clause)
		switch len(fields) {
		case 1:
			activeField = fields[0]
			payloads := repositoryIssueFieldPayloads(clause, activeField)
			assignmentActive = len(payloads) > 0
			if activeField == field {
				selectedPayloads = payloads
				selected = len(payloads) > 0
			}
		case 0:
			if activeField == field && assignmentActive {
				if payload, ok := repositoryIssueFieldContinuationPayload(clause); ok {
					selectedPayloads = []string{payload}
					selected = true
				}
			}
		default:
			for _, mentioned := range fields {
				if mentioned == field {
					selectedPayloads = nil
					selected = false
					break
				}
			}
			activeField = ""
			assignmentActive = false
		}
	}
	if !selected {
		return false
	}
	for _, payload := range selectedPayloads {
		if field == "title" || field == "body" || field == "milestone" {
			if repositoryIssuePayloadEquals(payload, value) {
				return true
			}
			continue
		}
		if repositoryIssueRequestContainsPayloadValue(payload, value) {
			return true
		}
	}
	return false
}

func repositoryIssuePayloadEquals(payload, value string) bool {
	normalize := func(text string) string {
		text = strings.TrimSpace(text)
		text = strings.Trim(text, "\"'`“”‘’「」『』")
		return strings.Join(strings.Fields(strings.ToLower(text)), " ")
	}
	return normalize(payload) != "" && normalize(payload) == normalize(value)
}

func repositoryIssueRequestContainsCommentBody(text, value string) bool {
	targetEnd := -1
	for _, match := range repositoryIssueNumberMentionPattern.FindAllStringIndex(text, -1) {
		if match[1] > targetEnd {
			targetEnd = match[1]
		}
	}
	if targetEnd < 0 {
		return false
	}
	tail := text[targetEnd:]
	lower := strings.ToLower(tail)
	bestIndex := -1
	bestLength := 0
	for _, marker := range []string{"：", ":", " saying ", " that ", "内容为", "评论为"} {
		if index := strings.Index(lower, marker); index >= 0 && (bestIndex < 0 || index < bestIndex) {
			bestIndex = index
			bestLength = len(marker)
		}
	}
	if bestIndex < 0 {
		return false
	}
	return repositoryIssuePayloadEquals(tail[bestIndex+bestLength:], value)
}

func repositoryIssueFieldTerms(field string) []string {
	return map[string][]string{
		"title":     {"title", "标题"},
		"body":      {"description", "details", "content", "body", "正文", "描述", "内容"},
		"labels":    {"labels", "label", "标签", "标记"},
		"assignees": {"assignees", "assignee", "assign", "负责人", "经办人", "指派"},
		"milestone": {"milestone", "里程碑"},
	}[field]
}

func repositoryIssueFieldPayloads(clause, field string) []string {
	lower := strings.ToLower(clause)
	fieldIndex := -1
	fieldLength := 0
	for _, term := range repositoryIssueFieldTerms(field) {
		if index := strings.Index(lower, term); index >= 0 && (fieldIndex < 0 || index < fieldIndex || index == fieldIndex && len(term) > fieldLength) {
			fieldIndex = index
			fieldLength = len(term)
		}
	}
	if fieldIndex < 0 {
		return nil
	}
	before := strings.TrimSpace(clause[:fieldIndex])
	after := strings.TrimSpace(clause[fieldIndex+fieldLength:])
	payloads := make([]string, 0, 2)
	afterMarkers := []string{"内容改为", "内容为", "设置为", "改成", "改为", "：", ":", "=", "为", "是", "to ", "as "}
	if field == "assignees" {
		afterMarkers = append([]string{"给", "to "}, afterMarkers...)
	}
	if field == "milestone" {
		afterMarkers = append([]string{"设为"}, afterMarkers...)
	}
	for _, marker := range afterMarkers {
		if strings.HasPrefix(strings.ToLower(after), marker) {
			if payload := strings.TrimSpace(after[len(marker):]); payload != "" {
				payloads = append(payloads, payload)
			}
			break
		}
	}
	if field == "milestone" && len(payloads) == 0 && after != "" {
		if first := []rune(after)[0]; first >= '0' && first <= '9' {
			payloads = append(payloads, after)
		}
	}
	if field == "labels" {
		lowerBefore := strings.ToLower(before)
		for _, marker := range []string{"add ", "with ", "加上", "添加", "加", "设置"} {
			if index := strings.LastIndex(lowerBefore, marker); index >= 0 {
				if payload := strings.TrimSpace(before[index+len(marker):]); payload != "" {
					payloads = append(payloads, payload)
				}
				break
			}
		}
	}
	return payloads
}

func repositoryIssueFieldContinuationPayload(clause string) (string, bool) {
	clause = strings.TrimSpace(clause)
	lower := strings.ToLower(clause)
	for _, marker := range []string{"use ", "set to ", "改成", "改为", "而是", "最终用"} {
		if strings.HasPrefix(lower, marker) {
			payload := strings.TrimSpace(clause[len(marker):])
			return payload, payload != ""
		}
	}
	return "", false
}

func repositoryIssueRequestExplicitlyClearsField(text, field string) bool {
	activeField := ""
	selected := false
	clears := false
	for _, clause := range repositoryIssueRequestClauses(text) {
		fields := repositoryIssueFieldsMentioned(clause)
		switch len(fields) {
		case 1:
			activeField = fields[0]
			if activeField == field {
				selected = true
				clears = repositoryIssueClauseExplicitlyClearsField(clause, field)
			}
		case 0:
		default:
			for _, mentioned := range fields {
				if mentioned == field {
					selected = false
					clears = false
					break
				}
			}
			activeField = ""
		}
	}
	return selected && clears
}

func repositoryIssueRequestClauses(text string) []string {
	return strings.FieldsFunc(text, func(char rune) bool {
		switch char {
		case ',', '，', ';', '；', '。', '!', '！', '?', '？', '\n', '\r':
			return true
		default:
			return false
		}
	})
}

func repositoryIssueFieldsMentioned(text string) []string {
	fields := make([]string, 0, 5)
	for _, field := range []string{"title", "body", "labels", "assignees", "milestone"} {
		if repositoryIssueRequestMentionsField(text, field) {
			fields = append(fields, field)
		}
	}
	return fields
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

func validateRepositoryIssueMetadataRequest(text, operation string, input map[string]any) (string, string) {
	for _, field := range []string{"labels", "assignees"} {
		raw, present := input[field]
		if !present {
			continue
		}
		items, err := stringSliceValue(raw)
		if err != nil {
			continue
		}
		if operation == "create" && len(items) == 0 {
			continue
		}
		if !repositoryIssueRequestMentionsField(text, field) {
			return "explicit_fields_required", "labels、assignees 和 milestone 只有在当前用户消息明确要求时才能写入。"
		}
		if operation == "update" && len(items) == 0 && !repositoryIssueRequestExplicitlyClearsField(text, field) {
			return "explicit_fields_required", "清空 labels 或 assignees 必须由当前用户消息明确要求。"
		}
		for _, item := range items {
			if !repositoryIssueRequestContainsFieldValue(text, field, item) {
				return "explicit_fields_required", "当前用户消息必须包含每个要写入的 label 或 assignee。"
			}
		}
	}
	if raw, present := input["milestone"]; present && raw != nil {
		if !repositoryIssueRequestMentionsField(text, "milestone") {
			return "explicit_fields_required", "labels、assignees 和 milestone 只有在当前用户消息明确要求时才能写入。"
		}
		if value, ok := numberValue(raw); ok && value == float64(int(value)) && !repositoryIssueRequestContainsFieldValue(text, "milestone", strconv.Itoa(int(value))) {
			return "explicit_fields_required", "当前用户消息必须包含要设置的 milestone 编号。"
		}
	}
	if raw, present := input["milestone"]; present && raw == nil && !repositoryIssueRequestExplicitlyClearsField(text, "milestone") {
		return "explicit_fields_required", "清除 milestone 必须由当前用户消息明确要求。"
	}
	return "", ""
}

func repositoryIssueClauseExplicitlyClearsField(clause, field string) bool {
	lower := strings.ToLower(clause)
	for _, term := range repositoryIssueFieldTerms(field) {
		index := strings.Index(lower, term)
		if index < 0 {
			continue
		}
		before := strings.TrimSpace(lower[:index])
		after := strings.TrimSpace(lower[index+len(term):])
		words := strings.Fields(before)
		for len(words) > 0 && (words[len(words)-1] == "all" || words[len(words)-1] == "the") {
			words = words[:len(words)-1]
		}
		if len(words) > 0 {
			switch words[len(words)-1] {
			case "clear", "remove", "delete", "empty":
				return true
			}
		}
		for _, marker := range []string{"清空", "删除", "移除", "置空", "取消"} {
			if strings.HasSuffix(before, marker) {
				return true
			}
		}
		after = strings.TrimSpace(strings.TrimLeft(after, " :=："))
		for _, marker := range []string{"clear", "remove", "delete", "empty", "清空", "删除", "移除", "置空", "取消"} {
			if after == marker || after == marker+" all" {
				return true
			}
		}
	}
	return false
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
	body, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (t *dianaRepositoryIssuesTool) validateWriteAccess(repository string) (string, string) {
	mode := repositoryPublishAuthMode(t.settings)
	if mode == repositoryPublishAuthToken && strings.TrimSpace(t.settings.String(repositoryPublishSettingToken, "")) == "" {
		return "token_required", "当前认证方式要求配置独立 GitHub Issues Token。"
	}
	allowed, err := repositoryPublishAllowlist(t.settings.String(repositoryPublishSettingAllowlist, ""))
	if err != nil {
		return "invalid_allowlist", "仓库写入白名单配置无效，请使用逗号或换行分隔的精确 owner/repo。"
	}
	if !allowed[strings.ToLower(repository)] {
		return "repository_not_allowed", "目标仓库不在“仓库 Issue 发布”插件的精确写入白名单中。"
	}
	return "", ""
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

func repositoryPublishValidateUserAccess(userID, repository string, owner bool, settings SettingValues) (string, string) {
	if owner {
		return "", ""
	}
	access, err := repositoryPublishUserAccess(settings.String(repositoryPublishSettingUserAccess, ""))
	if err != nil {
		return "invalid_user_repository_access", "用户仓库授权配置无效，请按每行“用户ID = owner/repo, owner/repo”填写。"
	}
	if !access[strings.TrimSpace(userID)][strings.ToLower(repository)] {
		return "permission_denied", "当前用户未获授权操作该 GitHub 仓库。"
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
	access, err := repositoryPublishUserAccess(settings.String(repositoryPublishSettingUserAccess, ""))
	return err == nil && len(access[strings.TrimSpace(userID)]) > 0
}

func repositoryPublishUserAccess(raw string) (map[string]map[string]bool, error) {
	access := map[string]map[string]bool{}
	for _, line := range strings.FieldsFunc(raw, func(char rune) bool { return char == '\n' || char == '\r' || char == ';' || char == '；' }) {
		userID, repositories, ok := strings.Cut(line, "=")
		userID = strings.TrimSpace(userID)
		if !ok || userID == "" || strings.Contains(repositories, "=") {
			return nil, fmt.Errorf("invalid user repository rule")
		}
		if access[userID] == nil {
			access[userID] = map[string]bool{}
		}
		for _, item := range strings.Split(repositories, ",") {
			repository, err := normalizeGitHubRepository(item)
			if err != nil {
				return nil, err
			}
			access[userID][strings.ToLower(repository)] = true
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
		return result.fail(apiErr.Code, repositoryIssueFailureMessage(apiErr.Code))
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
		return result.fail(apiErr.Code, repositoryIssueFailureMessage(apiErr.Code))
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
			agent.ExplicitUserMutationRequested(repositoryIssueCurrentRequestText(t.event), "repository_issue_create_duplicate") &&
			repositoryIssueRequestMentionsCandidate(repositoryIssueCurrentRequestText(t.event), candidates) &&
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
	return result.fail(apiErr.Code, repositoryIssueFailureMessage(apiErr.Code))
}

func (t *dianaRepositoryIssuesTool) update(ctx context.Context, repository string, input map[string]any) repositoryIssueResult {
	result := repositoryIssueResult{Operation: "update", Repository: repository, RequestedNumber: repositoryIssueNumber(input)}
	number := repositoryIssueNumber(input)
	if number <= 0 {
		return result.fail("invalid_input", "update 必须提供有效的 Issue number。")
	}
	current, apiErr := t.getIssue(ctx, repository, number)
	if apiErr != nil {
		return result.fail(apiErr.Code, repositoryIssueFailureMessage(apiErr.Code))
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
		return result.fail("invalid_input", "update 至少要提供 title、body、labels、assignees 或 milestone 中的一项。")
	}
	var updated githubRepositoryIssue
	if apiErr := t.doJSON(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/issues/%d", repository, number), payload, &updated); apiErr != nil {
		return result.fail(apiErr.Code, repositoryIssueFailureMessage(apiErr.Code))
	}
	result.OK = true
	result.Outcome = "updated"
	result.Message = "GitHub 已更新 Issue。"
	result.Issue = ptrRepositoryIssueSummary(repositoryIssueSummaryFromGitHub(updated))
	return result
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
		return result.fail(apiErr.Code, repositoryIssueFailureMessage(apiErr.Code))
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
		return result.fail(apiErr.Code, repositoryIssueFailureMessage(apiErr.Code))
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
	return result.fail(apiErr.Code, repositoryIssueFailureMessage(apiErr.Code))
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
		return result.fail(apiErr.Code, repositoryIssueFailureMessage(apiErr.Code))
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
		return result.fail(apiErr.Code, repositoryIssueFailureMessage(apiErr.Code))
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
	for _, candidate := range candidates {
		if repositoryIssueRequestMentionsNumber(text, candidate.Number) {
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
	token, credentialErr := t.repositoryPublishCredential(requestCtx)
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

func (t *dianaRepositoryIssuesTool) repositoryPublishCredential(ctx context.Context) (string, *repositoryIssueAPIError) {
	token := strings.TrimSpace(t.settings.String(repositoryPublishSettingToken, ""))
	mode := repositoryPublishAuthMode(t.settings)
	if mode == repositoryPublishAuthToken || mode == repositoryPublishAuthAuto && token != "" {
		if token == "" {
			return "", &repositoryIssueAPIError{Code: "token_required"}
		}
		return token, nil
	}
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

func repositoryIssueFailureMessage(code string) string {
	switch code {
	case "unauthorized":
		return "当前 GitHub 凭据无效或已过期。"
	case "permission_denied":
		return "GitHub 拒绝了操作；请确认当前凭据对目标仓库具有 Issues 所需权限。"
	case "token_required":
		return "当前认证方式要求配置独立 GitHub Issues Token。"
	case "gh_unavailable":
		return "当前系统未安装 gh，无法使用 GitHub CLI 认证。"
	case "gh_auth_required":
		return "gh 尚未登录 github.com 或登录凭据不可用，请先执行 gh auth login。"
	case "rate_limited":
		return "GitHub API 已限流，请稍后再试。"
	case "not_found":
		return "仓库或 Issue 不存在，或当前凭据无权访问。"
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
		Action:   "qqbot.repository_issue",
		Message:  message,
		Actor:    qqEventActor(t.event),
		Target:   target,
		Metadata: metadata,
	})
}
