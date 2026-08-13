package assistant

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"os"
	"os/exec"
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
	repositoryPublishSettingTimeout     = "timeout_seconds"
	defaultRepositoryPublishTimeoutSecs = 20
	repositoryPublishAuthToken          = "token"
	repositoryPublishAuthGH             = "gh"
	repositoryPublishAuthAuto           = "auto"
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

func (p *RepositoryPublishPlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID:          repositoryPublishPluginID,
		Name:        "仓库 Issue 发布",
		Version:     "0.2.0",
		Description: "允许主人及指定用户搜索或写入各自获授权的 GitHub 仓库 Issues；支持独立 Token 与 GitHub CLI 认证。",
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
				Description: "在“独立 Token”或“自动选择”模式下用于 Issue 读写；fine-grained token 仅授予白名单仓库的 Issues: read and write，保存后不回显。",
				Type:        PluginSettingTypeString,
				Default:     "",
				Secret:      true,
			},
			{
				Key:         repositoryPublishSettingAllowlist,
				Label:       "允许写入的仓库",
				Description: "精确填写 owner/repo；多个仓库用逗号或换行分隔。留空时拒绝所有写操作。",
				Type:        PluginSettingTypeString,
				Default:     "",
			},
			{
				Key:         repositoryPublishSettingUserAccess,
				Label:       "用户仓库授权",
				Description: "允许特定用户操作特定仓库。每行填写：用户ID = owner/repo, owner/repo。仓库还必须存在于上方全局白名单；留空时仍仅主人可用。",
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
