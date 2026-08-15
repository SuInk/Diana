package assistant

import (
	"context"
	"crypto/rand"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	repositoryPublishPluginID = "official.repository-publish"
	// RepositoryPublishPluginID is shared with authenticated WebUI actions.
	RepositoryPublishPluginID = repositoryPublishPluginID

	repositoryPublishSettingToken       = "github_token"
	repositoryPublishSettingAllowlist   = "allowed_repositories"
	repositoryPublishSettingTimeout     = "timeout_seconds"
	defaultRepositoryPublishTimeoutSecs = 20
)

// RepositoryPublishPlugin owns a GitHub credential that is intentionally
// separate from repository watches, git remotes, and command-line login state.
type RepositoryPublishPlugin struct {
	client          *http.Client
	baseURL         string
	confirmationKey [32]byte
	confirmationOK  bool
	locksMu         sync.Mutex
	locks           map[string]*repositoryPublishOperationLock
	uncertainMu     sync.Mutex
	uncertain       map[string]time.Time
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
		client:    &clientCopy,
		baseURL:   strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		locks:     map[string]*repositoryPublishOperationLock{},
		uncertain: map[string]time.Time{},
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
		Version:     "0.1.0",
		Description: "在主人明确要求时搜索或写入白名单 GitHub 仓库的 Issues；写权限与 Git push、仓库订阅完全隔离。",
		Official:    true,
		BuiltIn:     true,
		Permissions: []string{"network:https", "github:issues:read", "github:issues:write", "audit:write", "llm:tool"},
		Settings: []PluginSettingSpec{
			{
				Key:         repositoryPublishSettingToken,
				Label:       "GitHub Issues Token",
				Description: "独立用于 Issue 读写；fine-grained token 仅授予白名单仓库的 Issues: read and write。不会复用 git 或仓库订阅凭据，保存后不回显。",
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
