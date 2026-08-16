// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	repositoryPublishPluginID = "official.repository-publish"
	// RepositoryPublishPluginID is shared with authenticated WebUI actions.
	RepositoryPublishPluginID = repositoryPublishPluginID

	repositoryPublishSettingToken       = "github_token"
	repositoryPublishSettingAuthMode    = "github_auth_mode"
	repositoryPublishSettingAllowlist   = "allowed_repositories"
	repositoryPublishSettingUserAccess  = "user_repository_access"
	repositoryPublishSettingGroupAccess = "group_repository_access"
	repositoryPublishSettingUserTokens  = "user_github_tokens"
	repositoryPublishSettingTokenUsers  = "user_github_token_users"
	repositoryPublishSettingUserAuth    = "user_github_auth_modes"
	repositoryPublishSettingTimeout     = "timeout_seconds"
	defaultRepositoryPublishTimeoutSecs = 20
	repositoryPublishAuthToken          = "token"
	repositoryPublishAuthGH             = "gh"
	repositoryPublishAuthAuto           = "auto"
	repositoryPublishUserAuthInherit    = "inherit"
)

var (
	errRepositoryPublishGHUnavailable = errors.New("gh executable unavailable")
	errRepositoryPublishGHAuth        = errors.New("gh authentication unavailable")
)

// RepositoryPublishPlugin keeps Issue publishing isolated from repository
// watches and git remotes. It may use either its own token or an explicit gh mode.
type RepositoryPublishPlugin struct {
	client          *http.Client
	baseURL         string
	confirmationKey [32]byte
	confirmationOK  bool
	locksMu         sync.Mutex
	locks           map[string]*repositoryPublishOperationLock
	uncertainMu     sync.Mutex
	uncertain       map[string]time.Time
	draftsMu        sync.Mutex
	drafts          map[string]repositoryIssueDraft
	draftStore      RepositoryIssueDraftStore
	ghAuthToken     func(context.Context) (string, error)
}

type repositoryPublishOperationLock struct {
	mutex sync.Mutex
	refs  int
}

func NewRepositoryPublishPlugin(client *http.Client) *RepositoryPublishPlugin {
	return newRepositoryPublishPlugin(client, defaultGitHubAPIURL)
}

func newRepositoryPublishPlugin(client *http.Client, baseURL string) *RepositoryPublishPlugin {
	if client == nil {
		client = &http.Client{}
	}
	clientCopy := *client
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	plugin := &RepositoryPublishPlugin{
		client:      &clientCopy,
		baseURL:     strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		locks:       map[string]*repositoryPublishOperationLock{},
		uncertain:   map[string]time.Time{},
		drafts:      map[string]repositoryIssueDraft{},
		ghAuthToken: repositoryPublishGHAuthToken,
	}
	if count, err := rand.Read(plugin.confirmationKey[:]); err == nil && count == len(plugin.confirmationKey) {
		plugin.confirmationOK = true
	}
	return plugin
}

func (p *RepositoryPublishPlugin) markOperationUncertain(key string) {
	p.uncertainMu.Lock()
	defer p.uncertainMu.Unlock()
	p.uncertain[key] = time.Now().Add(24 * time.Hour)
}

func (p *RepositoryPublishPlugin) clearOperationUncertain(key string) {
	p.uncertainMu.Lock()
	defer p.uncertainMu.Unlock()
	delete(p.uncertain, key)
}

func (p *RepositoryPublishPlugin) operationUncertain(key string) bool {
	p.uncertainMu.Lock()
	defer p.uncertainMu.Unlock()
	now := time.Now()
	for candidate, expires := range p.uncertain {
		if !expires.After(now) {
			delete(p.uncertain, candidate)
		}
	}
	_, ok := p.uncertain[key]
	return ok
}

func (p *RepositoryPublishPlugin) setDraftStore(store RepositoryIssueDraftStore) {
	p.draftsMu.Lock()
	p.draftStore = store
	p.draftsMu.Unlock()
}

func (p *RepositoryPublishPlugin) saveDraft(ctx context.Context, draft repositoryIssueDraft) (repositoryIssueDraft, error) {
	var idBytes [12]byte
	if _, err := rand.Read(idBytes[:]); err == nil {
		draft.ID = hex.EncodeToString(idBytes[:])
	} else {
		draft.ID = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	if draft.Input != nil {
		draft.Input["operation_id"] = "draft-" + draft.ID
	}
	now := time.Now()
	draft.CreatedAt = now
	draft.UpdatedAt = now
	draft.Status = "pending"
	p.draftsMu.Lock()
	p.drafts[draft.ID] = draft
	store := p.draftStore
	p.draftsMu.Unlock()
	if store != nil {
		if err := store.SaveRepositoryIssueDraft(ctx, draft); err != nil {
			p.draftsMu.Lock()
			delete(p.drafts, draft.ID)
			p.draftsMu.Unlock()
			return repositoryIssueDraft{}, err
		}
	}
	return draft, nil
}

func (p *RepositoryPublishPlugin) findDraft(ctx context.Context, groupID, draftID string) (repositoryIssueDraft, bool, error) {
	if p == nil {
		return repositoryIssueDraft{}, false, nil
	}
	groupID, draftID = strings.TrimSpace(groupID), strings.TrimSpace(draftID)
	p.draftsMu.Lock()
	store := p.draftStore
	p.draftsMu.Unlock()
	if store != nil {
		if draftID != "" {
			draft, ok, err := store.RepositoryIssueDraft(ctx, draftID)
			if err != nil || !ok || draft.GroupID != groupID || draft.Status != "pending" {
				return repositoryIssueDraft{}, false, err
			}
			return draft, true, nil
		}
		items, err := store.ListRepositoryIssueDrafts(ctx, groupID, "pending")
		if err != nil || len(items) == 0 {
			return repositoryIssueDraft{}, false, err
		}
		return items[0], true, nil
	}
	p.draftsMu.Lock()
	defer p.draftsMu.Unlock()
	var latest repositoryIssueDraft
	for id, draft := range p.drafts {
		if draft.GroupID != groupID || draft.Status != "pending" {
			continue
		}
		if draftID != "" {
			if id == draftID {
				return draft, true, nil
			}
			continue
		}
		if latest.ID == "" || draft.CreatedAt.After(latest.CreatedAt) {
			latest = draft
		}
	}
	return latest, latest.ID != "", nil
}

func (p *RepositoryPublishPlugin) updateDraft(ctx context.Context, draft repositoryIssueDraft) error {
	if p == nil {
		return nil
	}
	draft.UpdatedAt = time.Now()
	p.draftsMu.Lock()
	p.drafts[draft.ID] = draft
	store := p.draftStore
	p.draftsMu.Unlock()
	if store != nil {
		return store.SaveRepositoryIssueDraft(ctx, draft)
	}
	return nil
}

func (p *RepositoryPublishPlugin) listDrafts(ctx context.Context, groupID, status string) ([]repositoryIssueDraft, error) {
	if p == nil {
		return nil, nil
	}
	p.draftsMu.Lock()
	store := p.draftStore
	p.draftsMu.Unlock()
	if store != nil {
		return store.ListRepositoryIssueDrafts(ctx, strings.TrimSpace(groupID), strings.TrimSpace(status))
	}
	p.draftsMu.Lock()
	defer p.draftsMu.Unlock()
	out := make([]repositoryIssueDraft, 0, len(p.drafts))
	for _, draft := range p.drafts {
		if groupID != "" && draft.GroupID != groupID || status != "" && status != "all" && draft.Status != status {
			continue
		}
		out = append(out, draft)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (p *RepositoryPublishPlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID:          repositoryPublishPluginID,
		Name:        "Issue 发布",
		Version:     "0.5.0",
		Description: "群成员可生成 Issue 草稿，由群内具备仓库权限的授权用户确认后创建。",
		Official:    true,
		BuiltIn:     true,
		Permissions: []string{"network:https", "github:issues:read", "github:issues:write", "audit:write", "llm:tool"},
		Settings: []PluginSettingSpec{
			{
				Key:         repositoryPublishSettingAuthMode,
				Label:       "GitHub 认证方式",
				Description: "Token 使用下方独立凭据；gh 使用当前系统的 GitHub CLI 登录，可访问已授权给该账号的协作仓库；自动优先使用 Token，未配置时再使用 gh。",
				Type:        PluginSettingTypeSelect,
				Default:     repositoryPublishAuthToken,
				Options: []PluginSettingOption{
					{Value: repositoryPublishAuthToken, Label: "独立 Token"},
					{Value: repositoryPublishAuthGH, Label: "GitHub CLI (gh)"},
					{Value: repositoryPublishAuthAuto, Label: "自动选择"},
				},
			},
			{
				Key:         repositoryPublishSettingToken,
				Label:       "GitHub Issues Token",
				Description: "在“独立 Token”或“自动选择”模式下用于 Issue 读写；Fine-grained token 只授予白名单仓库的 Issues: read and write，Classic token 适合需要跨仓库或更多 GitHub API 权限的场景，保存后不回显。",
				Type:        PluginSettingTypeString,
				Default:     "",
				Secret:      true,
			},
			{
				Key:         repositoryPublishSettingAllowlist,
				Label:       "允许操作的仓库",
				Description: "Issue 的读写操作白名单，精确填写 owner/repo；多个仓库用逗号或换行分隔。留空时拒绝所有 Issue 操作。",
				Type:        PluginSettingTypeString,
				Default:     "",
			},
			{
				Key:         repositoryPublishSettingUserAccess,
				Label:       "用户仓库授权",
				Description: "允许特定用户审批和操作特定仓库；每个用户使用自己的 GitHub Token。",
				Type:        PluginSettingTypeString,
				Default:     "",
			},
			{
				Key:         repositoryPublishSettingGroupAccess,
				Label:       "群聊草稿范围",
				Description: "群内所有成员可为这些仓库生成 Issue 草稿；只有群内授权用户确认后才会创建。",
				Type:        PluginSettingTypeString,
				Default:     "",
			},
			{
				Key:         repositoryPublishSettingUserTokens,
				Label:       "用户 GitHub Token",
				Description: "由用户授权编辑器维护；每个用户的 Token 独立保存且不会回显。",
				Type:        PluginSettingTypeString,
				Default:     "",
				Secret:      true,
			},
			{
				Key:         repositoryPublishSettingTokenUsers,
				Label:       "已配置 Token 的用户",
				Description: "由用户授权编辑器维护。",
				Type:        PluginSettingTypeString,
				Default:     "",
			},
			{
				Key:         repositoryPublishSettingUserAuth,
				Label:       "用户 GitHub 认证来源",
				Description: "由用户授权编辑器维护；可为每个用户选择独立 Token、服务器 gh 或沿用插件全局认证。",
				Type:        PluginSettingTypeString,
				Default:     "",
			},
			{
				Key:         repositoryPublishSettingTimeout,
				Label:       "GitHub 请求超时",
				Description: "单次 GitHub API 请求的最长等待时间。创建或评论超时后只执行只读对账，不盲目重试写入。",
				Type:        PluginSettingTypeNumber,
				Default:     defaultRepositoryPublishTimeoutSecs,
				Min:         settingRange(5),
				Max:         settingRange(60),
				Step:        1,
				Unit:        "秒",
			},
		},
	}
}

func (p *RepositoryPublishPlugin) MergeSecretSetting(key, previous, submitted string) (string, error) {
	if key != repositoryPublishSettingUserTokens {
		return submitted, nil
	}
	current, err := repositoryPublishUserTokens(previous)
	if err != nil {
		return "", err
	}
	var updates map[string]*string
	if err := json.Unmarshal([]byte(submitted), &updates); err != nil {
		return "", fmt.Errorf("qqbot: invalid user token update")
	}
	for rawUserID, token := range updates {
		userID := strings.TrimSpace(rawUserID)
		if userID == "" {
			return "", fmt.Errorf("qqbot: invalid user token update")
		}
		if token == nil || strings.TrimSpace(*token) == "" {
			delete(current, userID)
			continue
		}
		current[userID] = strings.TrimSpace(*token)
	}
	if len(current) == 0 {
		return "", nil
	}
	body, err := json.Marshal(current)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func repositoryPublishUserTokens(raw string) (map[string]string, error) {
	tokens := map[string]string{}
	if strings.TrimSpace(raw) == "" {
		return tokens, nil
	}
	if err := json.Unmarshal([]byte(raw), &tokens); err != nil {
		return nil, fmt.Errorf("qqbot: invalid stored user tokens")
	}
	for userID, token := range tokens {
		trimmedID, trimmedToken := strings.TrimSpace(userID), strings.TrimSpace(token)
		if trimmedID == "" || trimmedToken == "" {
			delete(tokens, userID)
			continue
		}
		if trimmedID != userID {
			delete(tokens, userID)
			tokens[trimmedID] = trimmedToken
		}
	}
	return tokens, nil
}

func repositoryPublishUserAuthModes(raw string) (map[string]string, error) {
	modes := map[string]string{}
	if strings.TrimSpace(raw) == "" {
		return modes, nil
	}
	if err := json.Unmarshal([]byte(raw), &modes); err != nil {
		return nil, fmt.Errorf("qqbot: invalid stored user auth modes")
	}
	for rawUserID, rawMode := range modes {
		userID := strings.TrimSpace(rawUserID)
		mode := strings.ToLower(strings.TrimSpace(rawMode))
		delete(modes, rawUserID)
		if userID == "" {
			continue
		}
		switch mode {
		case repositoryPublishUserAuthInherit, repositoryPublishAuthGH, repositoryPublishAuthToken:
			modes[userID] = mode
		default:
			return nil, fmt.Errorf("qqbot: invalid stored user auth mode")
		}
	}
	return modes, nil
}

func repositoryPublishGHAuthToken(ctx context.Context) (string, error) {
	path, err := exec.LookPath("gh")
	if err != nil {
		return "", errRepositoryPublishGHUnavailable
	}
	cmd := exec.CommandContext(ctx, path, "auth", "token", "--hostname", "github.com")
	cmd.Env = repositoryPublishGHEnvironment(os.Environ())
	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errRepositoryPublishGHAuth
	}
	token := strings.TrimSpace(string(output))
	if token == "" {
		return "", errRepositoryPublishGHAuth
	}
	return token, nil
}

func repositoryPublishGHEnvironment(environment []string) []string {
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, "GH_TOKEN") || strings.EqualFold(key, "GITHUB_TOKEN") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func (*RepositoryPublishPlugin) Handle(context.Context, PluginRequest) (*PluginResponse, error) {
	return nil, nil
}

func (p *RepositoryPublishPlugin) operationLock(key string) func() {
	p.locksMu.Lock()
	lock := p.locks[key]
	if lock == nil {
		lock = &repositoryPublishOperationLock{}
		p.locks[key] = lock
	}
	lock.refs++
	p.locksMu.Unlock()

	lock.mutex.Lock()
	return func() {
		lock.mutex.Unlock()
		p.locksMu.Lock()
		lock.refs--
		if lock.refs == 0 && p.locks[key] == lock {
			delete(p.locks, key)
		}
		p.locksMu.Unlock()
	}
}
