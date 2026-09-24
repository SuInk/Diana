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

	"github.com/SuInk/diana/internal/procgroup"
)

const (
	repositoryPublishPluginID = "official.repository-publish"
	// RepositoryPublishPluginID is shared with authenticated WebUI actions.
	RepositoryPublishPluginID = repositoryPublishPluginID

	repositoryPublishSettingToken          = "github_token"
	repositoryPublishSettingAuthMode       = "github_auth_mode"
	repositoryPublishSettingAllowlist      = "allowed_repositories"
	repositoryPublishSettingUserAccess     = "user_repository_access"
	repositoryPublishSettingGroupAccess    = "group_repository_access"
	repositoryPublishSettingDraftUsers     = "issue_draft_user_access"
	repositoryPublishSettingDraftGroups    = "issue_draft_group_access"
	repositoryPublishSettingManagerUsers   = "issue_manager_user_access"
	repositoryPublishSettingManagerGroups  = "issue_manager_group_access"
	repositoryPublishSettingApproverGroups = "issue_approver_group_access"
	repositoryPublishSettingCodeUsers      = "code_reader_user_access"
	repositoryPublishSettingUserTokens     = "user_github_tokens"
	repositoryPublishSettingTokenUsers     = "user_github_token_users"
	repositoryPublishSettingUserAuth       = "user_github_auth_modes"
	repositoryPublishSettingTimeout        = "timeout_seconds"
	defaultRepositoryPublishTimeoutSecs    = 90
	repositoryPublishAuthToken             = "token"
	repositoryPublishAuthGH                = "gh"
	repositoryPublishAuthAuto              = "auto"
	repositoryPublishUserAuthInherit       = "inherit"
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
	// 不再往草稿里塞合成的 operation_id。它会让「探测重复候选」和「确认后重试」落到
	// 两个不同的指纹上，确认令牌因此永远校验不过。重复审批由草稿状态阻断，写入层
	// 自己也有指纹去重，这里不需要再兜一层。调用方自己带的 operation_id 原样保留。
	now := time.Now()
	draft.CreatedAt = now
	draft.UpdatedAt = now
	draft.ExpiresAt = now.Add(repositoryIssueDraftTTL)
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
			privateApproval := strings.HasPrefix(groupID, "private:")
			if err != nil || !ok || (!privateApproval && draft.GroupID != groupID) || draft.Status != "pending" || draft.Expired(time.Now()) {
				return repositoryIssueDraft{}, false, err
			}
			return draft, true, nil
		}
		items, err := store.ListRepositoryIssueDrafts(ctx, groupID, "pending")
		if err != nil {
			return repositoryIssueDraft{}, false, err
		}
		now := time.Now()
		for _, item := range items {
			if !item.Expired(now) {
				return item, true, nil
			}
		}
		return repositoryIssueDraft{}, false, nil
	}
	p.draftsMu.Lock()
	defer p.draftsMu.Unlock()
	var latest repositoryIssueDraft
	now := time.Now()
	for id, draft := range p.drafts {
		privateApproval := strings.HasPrefix(groupID, "private:")
		if (!privateApproval && draft.GroupID != groupID) || draft.Status != "pending" || draft.Expired(now) {
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

// findResolvedDraft 按 ID 查找已经处理过的草稿，不做 pending 过滤。
// 审批消息并发到达时，第一条已经把草稿消费掉并写进了 GitHub，第二条按 pending
// 查就查不到——如果照直报「找不到草稿」，用户看到的就是「其实建好了却说失败」。
func (p *RepositoryPublishPlugin) findResolvedDraft(ctx context.Context, groupID, draftID string) (repositoryIssueDraft, bool, error) {
	if p == nil {
		return repositoryIssueDraft{}, false, nil
	}
	groupID, draftID = strings.TrimSpace(groupID), strings.TrimSpace(draftID)
	if draftID == "" {
		return repositoryIssueDraft{}, false, nil
	}
	privateApproval := strings.HasPrefix(groupID, "private:")
	p.draftsMu.Lock()
	store := p.draftStore
	cached, cachedOK := p.drafts[draftID]
	p.draftsMu.Unlock()
	if store != nil {
		draft, ok, err := store.RepositoryIssueDraft(ctx, draftID)
		if err != nil || !ok || (!privateApproval && draft.GroupID != groupID) {
			return repositoryIssueDraft{}, false, err
		}
		return draft, true, nil
	}
	if !cachedOK || (!privateApproval && cached.GroupID != groupID) {
		return repositoryIssueDraft{}, false, nil
	}
	return cached, true, nil
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

// draftByID 按 ID 读一份草稿，不限会话范围：WebUI 的调用者不属于任何群。
func (p *RepositoryPublishPlugin) draftByID(ctx context.Context, id string) (RepositoryIssueDraft, bool, error) {
	p.draftsMu.Lock()
	store := p.draftStore
	cached, cachedOK := p.drafts[id]
	p.draftsMu.Unlock()
	if store != nil {
		return store.RepositoryIssueDraft(ctx, id)
	}
	return cached, cachedOK, nil
}

// RestoreDraft 让一份过期或已取消的草稿重新回到待审批，并重新计时。
//
// 后台的调用者已经登录过控制台，是这条链路上的授权人；群里那套确认码只是聊天
// 窗口没有身份验证时的替代。已经写进 GitHub 的草稿不能还原：再提交一次就是重复
// 建 Issue。
func (p *RepositoryPublishPlugin) RestoreDraft(ctx context.Context, id string) (RepositoryIssueDraft, error) {
	draft, err := p.pendingDraftForEdit(ctx, id, true)
	if err != nil {
		return RepositoryIssueDraft{}, err
	}
	draft.Status = "pending"
	draft.ResolvedBy = ""
	draft.ExpiresAt = time.Now().Add(repositoryIssueDraftTTL)
	// 换一个确认码：旧码在群里公开过，又躺了至少七天，照原样放回去等于把一个
	// 人人都见过的口令重新激活。
	draft.ConfirmationCode = newRepositoryIssueConfirmationCode(draftConfirmationCode(draft))
	if err := p.updateDraft(ctx, draft); err != nil {
		return RepositoryIssueDraft{}, err
	}
	return draft, nil
}

// EditDraftFromWeb 改写草稿的标题、正文和标签。只改还没写进 GitHub 的草稿：
// 已创建的改了也不会同步到线上，只会让记录和实际内容对不上。
func (p *RepositoryPublishPlugin) EditDraftFromWeb(ctx context.Context, id, title, body string, labels []string) (RepositoryIssueDraft, error) {
	draft, err := p.pendingDraftForEdit(ctx, id, false)
	if err != nil {
		return RepositoryIssueDraft{}, err
	}
	if draft.Input == nil {
		draft.Input = map[string]any{}
	}
	if title = strings.TrimSpace(title); title != "" {
		draft.Input["title"] = title
	}
	draft.Input["body"] = body
	cleaned := make([]string, 0, len(labels))
	for _, label := range labels {
		if label = strings.TrimSpace(label); label != "" {
			cleaned = append(cleaned, label)
		}
	}
	if len(cleaned) > 0 {
		draft.Input["labels"] = cleaned
	} else {
		delete(draft.Input, "labels")
	}
	if err := p.updateDraft(ctx, draft); err != nil {
		return RepositoryIssueDraft{}, err
	}
	return draft, nil
}

// DeleteDraft 删掉一条草稿记录。删的只是记录：已经写进 GitHub 的 Issue 还在。
func (p *RepositoryPublishPlugin) DeleteDraft(ctx context.Context, id string) error {
	if p == nil {
		return fmt.Errorf("仓库发布插件不可用")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("缺少草稿 ID")
	}
	p.draftsMu.Lock()
	store := p.draftStore
	_, cached := p.drafts[id]
	delete(p.drafts, id)
	p.draftsMu.Unlock()
	if store == nil {
		if !cached {
			return fmt.Errorf("草稿不存在")
		}
		return nil
	}
	deleted, err := store.DeleteRepositoryIssueDraft(ctx, id)
	if err != nil {
		return err
	}
	if !deleted && !cached {
		return fmt.Errorf("草稿不存在")
	}
	return nil
}

// pendingDraftForEdit 取出一份还没写进 GitHub 的草稿。restoring 为真时允许
// 已取消的草稿，其余场景只接受待审批的。
func (p *RepositoryPublishPlugin) pendingDraftForEdit(ctx context.Context, id string, restoring bool) (RepositoryIssueDraft, error) {
	if p == nil {
		return RepositoryIssueDraft{}, fmt.Errorf("仓库发布插件不可用")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return RepositoryIssueDraft{}, fmt.Errorf("缺少草稿 ID")
	}
	draft, ok, err := p.draftByID(ctx, id)
	if err != nil {
		return RepositoryIssueDraft{}, err
	}
	if !ok {
		return RepositoryIssueDraft{}, fmt.Errorf("草稿不存在")
	}
	switch draft.Status {
	case "pending":
		return draft, nil
	case "cancelled":
		if restoring {
			return draft, nil
		}
		return RepositoryIssueDraft{}, fmt.Errorf("草稿已取消，先还原再改")
	default:
		return RepositoryIssueDraft{}, fmt.Errorf("草稿已经写进 GitHub，不能再改动；要改就去改那个 Issue")
	}
}

// newRepositoryIssueConfirmationCode 生成一个不同于 previous 的确认码。
func newRepositoryIssueConfirmationCode(previous string) string {
	previous = strings.TrimSpace(previous)
	for attempt := 0; attempt < 8; attempt++ {
		var raw [4]byte
		if _, err := rand.Read(raw[:]); err != nil {
			break
		}
		code := hex.EncodeToString(raw[:])[:repositoryIssueConfirmationCodeLength]
		if !strings.EqualFold(code, previous) {
			return code
		}
	}
	// 随机源不可用时退回时间戳，宁可码不够随机也不能把旧码原样留着。
	return fmt.Sprintf("%0*x", repositoryIssueConfirmationCodeLength, time.Now().UnixNano()&0xffffff)
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
		Name:        "GitHub Issue 与 PR",
		Version:     "0.7.0",
		Description: "搜索和管理 GitHub Issue；读取 Pull Request 的描述、改动文件和 patch，并在 PR 上发表评论或提交 review（只评论，不批准、不合并）。read_file 读取仓库文件：公开仓库全员可查，私有仓库仅主人与授权用户可读。群成员可生成草稿，由具备仓库权限的授权用户用确认码确认后写入。",
		Official:    true,
		BuiltIn:     true,
		Permissions: []string{"network:https", "github:issues:read", "github:issues:write", "github:pull_requests:read", "github:pull_requests:write", "github:contents:read", "audit:write", "llm:tool"},
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
				Label:       "GitHub Token",
				Description: "在“独立 Token”或“自动选择”模式下用于 Issue 与 PR 读写；Fine-grained token 只授予白名单仓库的 Issues: read and write 和 Pull requests: read and write（只用 Issue 功能时可不给后者），Classic token 适合需要跨仓库或更多 GitHub API 权限的场景，保存后不回显。",
				Type:        PluginSettingTypeString,
				Default:     "",
				Secret:      true,
			},
			{
				Key:         repositoryPublishSettingAllowlist,
				Label:       "允许操作的仓库",
				Description: "Issue 与 PR 写操作的仓库白名单，精确填写 owner/repo；多个仓库用逗号或换行分隔，留空时拒绝非主人的所有写操作。只对非主人生效：主人的写入不受此名单限制；公开仓库的读取也不受此影响，全员可查。",
				Type:        PluginSettingTypeString,
				Default:     "",
			},
			{
				Key:         repositoryPublishSettingUserAccess,
				Label:       "用户仓库授权",
				Description: "允许特定用户在私聊里审批和操作特定仓库；每个用户使用自己的 GitHub Token。群聊用“群聊草稿范围”那项。",
				Type:        PluginSettingTypeString,
				Default:     "",
			},
			{
				Key:   repositoryPublishSettingGroupAccess,
				Label: "群聊草稿范围",
				Description: "群内成员可为这些仓库生成 Issue 草稿；只有群内授权用户确认后才会创建。默认群里所有人都算数，" +
					"可在仓库后面加 #group_admin 或 #group_owner 收窄到群主和群管理员。",
				Type:    PluginSettingTypeString,
				Default: "",
			},
			{
				Key: repositoryPublishSettingDraftUsers, Label: "Issue 草稿提交者（按用户）",
				Description: "按“用户 ID = owner/repo, owner/repo”填写；这些用户在**私聊**里可以提交草稿，但不能直接写入 Issue。只在私聊生效——要放开群聊用“按群”那项。",
				Type:        PluginSettingTypeString, Default: "",
			},
			{
				Key: repositoryPublishSettingDraftGroups, Label: "Issue 草稿提交者（按群）",
				Description: "按“群 ID = owner/repo, owner/repo”填写；该群成员可以提交草稿，但不能直接写入 Issue。默认群里所有人都算数；" +
					"要收窄就在仓库后面加身份要求——owner/repo#group_admin 只对群主和群管理员生效，owner/repo#group_owner 只对群主生效。" +
					"这里说的是发言人在群里的身份，和本插件的“Issue 管理人员”不是一回事。只想放开个别人时改用“按用户”那项。",
				Type: PluginSettingTypeString, Default: "",
			},
			{
				Key: repositoryPublishSettingManagerUsers, Label: "Issue 管理人员（按用户）",
				Description: "按“用户 ID = owner/repo, owner/repo”填写；这些用户在私聊里可以直接创建和管理 Issue。" +
					"只在私聊生效——群聊里的权限一律由下面两项“按群”授权决定（那两项还能按群身份收窄），个人授权不会带进群。" +
					"想让某个群友能拍板推动本群草稿，用“群聊草稿审批人（按群）”。",
				Type: PluginSettingTypeString, Default: "",
			},
			{
				Key: repositoryPublishSettingManagerGroups, Label: "Issue 管理人员（按群）",
				Description: "按“群 ID = owner/repo, owner/repo”填写；该群成员可以直接创建和管理 Issue，请谨慎授予。默认群里所有人都算数；" +
					"要收窄就在仓库后面加身份要求——owner/repo#group_admin 只对群主和群管理员生效，owner/repo#group_owner 只对群主生效。" +
					"这里说的是发言人在群里的身份，和本项授予的“Issue 管理人员”不是一回事：前者由聊天平台决定，后者是这份名单。" +
					"身份不达标的调用会被直接拒绝并说明原因。只想授权个别人时改用“按用户”那项——按用户的授权私聊和群聊都生效，不受身份要求限制。",
				Type: PluginSettingTypeString, Default: "",
			},
			{
				Key: repositoryPublishSettingApproverGroups, Label: "群聊草稿审批人（按群）",
				Description: "按“群 ID = 用户 ID, 用户 ID”填写；这些人可以在该群里审批或取消本群提交的 Issue 草稿，但不能绕过草稿直接写入。" +
					"群里谁能拍板由群自己定，不靠个人把私聊里的权限带进来；留空则该群的草稿只有机器人主人能批。",
				Type: PluginSettingTypeString, Default: "",
			},
			{
				Key: repositoryPublishSettingCodeUsers, Label: "私有仓库源码读取授权（按用户）",
				Description: "按“用户 ID = owner/repo, owner/repo”填写；公开仓库默认全员可查，私有仓库仅主人与这里授权的用户（以及 Issue 管理人员、用户仓库授权名单）可以读取代码。只在私聊生效——群聊里除主人外读不到私有仓库源码。",
				Type:        PluginSettingTypeString, Default: "",
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
				Label:       "Issue 操作超时",
				Description: "单次 GitHub API 请求的最长等待时间。创建或评论超时后只执行只读对账，不盲目重试写入。",
				Type:        PluginSettingTypeNumber,
				Default:     defaultRepositoryPublishTimeoutSecs,
				Min:         settingRange(5),
				Max:         settingRange(300),
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
		return "", fmt.Errorf("diana: invalid user token update")
	}
	for rawUserID, token := range updates {
		userID := strings.TrimSpace(rawUserID)
		if userID == "" {
			return "", fmt.Errorf("diana: invalid user token update")
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
		return nil, fmt.Errorf("diana: invalid stored user tokens")
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
		return nil, fmt.Errorf("diana: invalid stored user auth modes")
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
			return nil, fmt.Errorf("diana: invalid stored user auth mode")
		}
	}
	return modes, nil
}

func repositoryPublishGHAuthToken(ctx context.Context) (string, error) {
	path, err := exec.LookPath("gh")
	if err != nil {
		return "", errRepositoryPublishGHUnavailable
	}
	cmd := procgroup.CommandContext(ctx, path, "auth", "token", "--hostname", "github.com")
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
