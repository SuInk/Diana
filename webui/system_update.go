// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/ghmirror"
	"github.com/SuInk/diana/model/updater"

	"github.com/gin-gonic/gin"
	"golang.org/x/sync/singleflight"
)

const (
	defaultReleaseOwner = "SuInk"
	defaultReleaseRepo  = "Diana"
	maxRollbackReleases = 5
	updateCheckInterval = 30 * time.Minute
	updateCheckDelay    = 30 * time.Second

	// buildTypeRelease/buildTypeSource 区分正式构建和源码构建。
	// 源码构建不参与更新提示和自动更新，只能显式切换到正式 Release 包。
	buildTypeRelease = "release"
	buildTypeSource  = "source"
)

var errInvalidUpdateVersion = errors.New("更新版本号无效")

type releaseRefreshContextKey struct{}

type systemUpdateCheckResponse struct {
	DeploymentMode    string `json:"deployment_mode"`
	CurrentVersion    string `json:"current_version"`
	LatestVersion     string `json:"latest_version,omitempty"`
	LatestPublishedAt string `json:"latest_published_at,omitempty"`
	CheckedAt         string `json:"checked_at"`
	UpdateAvailable   bool   `json:"update_available"`
	UpdateSupported   bool   `json:"update_supported"`
	BuildType         string `json:"build_type"`
	// UpdateUnsupportedReason 在 UpdateSupported 为 false 时说明原因。界面必须
	// 把它显示出来，否则「升不了级」会被渲染成「已经是最新」。
	UpdateUnsupportedReason string `json:"update_unsupported_reason,omitempty"`
	// SwitchToReleaseAvailable 表示当前是源码构建，可以显式切换到正式 Release 包。
	SwitchToReleaseAvailable bool                 `json:"switch_to_release_available"`
	IntegrityMode            string               `json:"integrity_mode"`
	ChecksumAvailable        bool                 `json:"checksum_available"`
	ChecksumURL              string               `json:"checksum_url,omitempty"`
	Status                   *updater.Status      `json:"status,omitempty"`
	Policy                   updater.UpdatePolicy `json:"policy"`
}

type ReleasePackageUpdater interface {
	Supported() bool
	// UnsupportedReason 说明为什么这台机器升不了级；支持时返回空。
	UnsupportedReason() string
	ExpectedAssetName() string
	Status(context.Context) (updater.Status, error)
	Download(context.Context, updater.ReleasePackage, bool) (updater.Result, error)
	InstallDownloaded(context.Context) (updater.Result, error)
	Install(context.Context, updater.ReleasePackage, bool) (updater.Result, error)
}

type UpdatePolicyStore interface {
	LoadUpdatePolicy(context.Context) (updater.UpdatePolicy, bool, error)
	SaveUpdatePolicy(context.Context, updater.UpdatePolicy) error
}

type UpdateGitHubTokenStore interface {
	LoadUpdateGitHubToken(context.Context) (string, bool, error)
	SaveUpdateGitHubToken(context.Context, string) error
}

type SystemUpdater interface {
	Status(context.Context) (updater.Status, error)
	Check(context.Context) (updater.Status, error)
	UpdateToRelease(context.Context, string) (updater.Result, error)
	ForceUpdateToRelease(context.Context, string) (updater.Result, error)
	Rollback(ctx context.Context, ref string) (updater.Result, error)
}

type SystemUpdateHandler struct {
	updater               SystemUpdater
	releaseUpdater        ReleasePackageUpdater
	logs                  AppLogWriter
	buildVersion          string
	buildType             string
	httpClient            *http.Client
	githubAPIBase         string
	changelog             changelogCache
	policyStore           UpdatePolicyStore
	policyMu              sync.RWMutex
	policy                updater.UpdatePolicy
	githubTokenStore      UpdateGitHubTokenStore
	githubTokenMu         sync.RWMutex
	githubToken           string
	staticReleaseURL      string
	autoUpdateMu          sync.Mutex
	updateSchedulerOnce   sync.Once
	releaseCacheStore     ReleaseCacheStore
	releaseCacheMu        sync.RWMutex
	releaseCachePersistMu sync.Mutex
	releaseCache          persistedReleaseCache
	releaseFetch          singleflight.Group
	mirror                GitHubMirrorSelector
	now                   func() time.Time
}

// SetReleasePackageUpdater enables self-update for complete Release packages.
// Source checkouts continue to use the Git updater.
func (h *SystemUpdateHandler) SetReleasePackageUpdater(releaseUpdater ReleasePackageUpdater) {
	h.releaseUpdater = releaseUpdater
}

// NewSystemUpdateHandler 创建系统更新接口处理器。
func NewSystemUpdateHandler(systemUpdater SystemUpdater) *SystemUpdateHandler {
	return &SystemUpdateHandler{
		updater:          systemUpdater,
		buildType:        buildTypeRelease,
		httpClient:       &http.Client{Timeout: 10 * time.Second},
		policy:           updater.DefaultUpdatePolicy(),
		staticReleaseURL: "https://github.com/SuInk/Diana/releases/latest/download/latest.json",
	}
}

func (h *SystemUpdateHandler) SetUpdatePolicyStore(ctx context.Context, store UpdatePolicyStore) error {
	h.policyStore = store
	if tokenStore, ok := store.(UpdateGitHubTokenStore); ok {
		h.githubTokenStore = tokenStore
		if token, found, tokenErr := tokenStore.LoadUpdateGitHubToken(ctx); tokenErr != nil {
			return tokenErr
		} else if found {
			h.githubToken = strings.TrimSpace(token)
		}
	}
	if store == nil {
		return nil
	}
	policy, ok, err := store.LoadUpdatePolicy(ctx)
	if err != nil {
		return err
	}
	if ok {
		policy = normalizeUpdatePolicy(policy)
		h.policyMu.Lock()
		h.policy = policy
		h.policyMu.Unlock()
		if h.mirror != nil {
			h.mirror.SetMode(policy.GitHubMirror)
		}
	}
	return nil
}

// SetLogStore 注入系统更新操作日志写入器。
func (h *SystemUpdateHandler) SetLogStore(store AppLogWriter) {
	h.logs = store
}

// SetBuildVersion 注入编译期版本号，git 不可用（如容器部署）时兜底展示。
func (h *SystemUpdateHandler) SetBuildVersion(version string) {
	h.buildVersion = version
}

// SetBuildType 注入构建类型；只有构建期注入了正式版本号才算 Release 构建。
func (h *SystemUpdateHandler) SetBuildType(buildType string) {
	if strings.EqualFold(strings.TrimSpace(buildType), buildTypeSource) {
		h.buildType = buildTypeSource
		return
	}
	h.buildType = buildTypeRelease
}

// sourceBuild 判断当前是否是运行在 Release 包目录下的源码构建。
// Git 部署本身就靠重新构建来更新，不受这个判断影响。
func (h *SystemUpdateHandler) sourceBuild(gitAvailable bool) bool {
	return !gitAvailable && h.buildType == buildTypeSource
}

// Register 注册系统更新状态和执行接口。
func (h *SystemUpdateHandler) Register(router gin.IRouter) {
	router.GET("/api/system/version", h.version)
	router.GET("/api/system/update", h.status)
	router.POST("/api/system/update", h.update)
	router.POST("/api/system/update/download", h.download)
	router.POST("/api/system/update/install", h.installDownloaded)
	router.GET("/api/system/update/policy", h.getPolicy)
	router.GET("/api/system/update/mirrors", h.mirrors)
	router.POST("/api/system/update/mirrors/test", h.testMirrors)
	router.PUT("/api/system/update/policy", h.savePolicy)
	router.GET("/api/system/update/github-token", h.getGitHubToken)
	router.PUT("/api/system/update/github-token", h.saveGitHubToken)
	router.POST("/api/system/update/check", h.check)
	router.POST("/api/system/update/rollback", h.rollback)
	router.GET("/api/system/update/changelog", h.changelogList)
}

// version 返回版本信息；git 状态可选，容器等非 git 部署时只有编译版本。
func (h *SystemUpdateHandler) version(c *gin.Context) {
	payload := gin.H{"build_version": h.buildVersion, "build_type": h.buildType, "update_supported": false}
	label := h.buildVersion
	if h.releaseUpdater != nil && h.releaseUpdater.Supported() {
		payload["git_available"] = false
		payload["deployment_mode"] = "release"
		payload["update_supported"] = true
		if status, statusErr := h.releaseUpdater.Status(c.Request.Context()); statusErr == nil {
			if v := status.VersionLabel(); v != "" {
				label = v
			}
		}
	} else if status, err := h.updater.Status(c.Request.Context()); err == nil {
		gitAvailable := status.RemoteURL != ""
		payload["git_available"] = gitAvailable
		payload["deployment_mode"] = deploymentMode(gitAvailable)
		payload["head_commit"] = status.HeadCommit
		payload["head_subject"] = status.HeadSubject
		payload["branch"] = status.Branch
		payload["behind"] = status.Behind
		label = preferredVersionLabel(label, status.VersionLabel())
		payload["update_supported"] = gitAvailable
		if !gitAvailable {
			payload["update_unsupported_reason"] = gitUpdateUnsupportedReason
		}
	} else {
		payload["git_available"] = false
		payload["deployment_mode"] = "release"
		payload["update_unsupported_reason"] = h.releaseUpdateSupport(false, false, false).Reason
	}
	payload["version_label"] = label
	c.JSON(http.StatusOK, payload)
}

// status 处理系统更新状态查询请求。
func (h *SystemUpdateHandler) status(c *gin.Context) {
	var status updater.Status
	var err error
	if h.releaseUpdater != nil && h.releaseUpdater.Supported() {
		status, err = h.releaseUpdater.Status(c.Request.Context())
	} else {
		status, err = h.updater.Status(c.Request.Context())
	}
	if err != nil {
		// 状态页会频繁轮询，查询失败只返回 HTTP 错误，避免把日志中心刷满。
		writeUpdateHTTPError(c, err)
		return
	}
	c.JSON(http.StatusOK, status)
}

// check 始终以最新稳定 GitHub Release 判断版本；Git 只负责源码状态和安装传输。
// releaseCheckFailure 记录一次检查失败该用哪个 HTTP 状态码回应。聊天里的
// diana.version 用不上状态码，只看错误本身。
type releaseCheckFailure struct {
	status int
	err    error
	// updateError 表示要走 writeUpdateError 的分类逻辑，而不是直接写状态码。
	updateError bool
}

func (h *SystemUpdateHandler) check(c *gin.Context) {
	// 明确点击“检查更新”才强制验证；请求标记不会破坏旧缓存和 ETag。
	ctx := context.WithValue(c.Request.Context(), releaseRefreshContextKey{}, true)
	response, failure := h.runReleaseCheck(ctx)
	if failure != nil {
		if failure.updateError {
			h.writeUpdateError(c, "system.update.check", failure.err)
			return
		}
		writeError(c, failure.status, failure.err)
		return
	}
	h.changelog.mu.Lock()
	h.changelog.fetchedAt = time.Time{}
	h.changelog.mu.Unlock()
	c.JSON(http.StatusOK, response)
}

// runReleaseCheck 汇总「当前什么版本、最新什么版本、能不能升」这一组结论。
// HTTP 接口和聊天里的 diana.version 共用同一份判断，免得两处各写一套然后慢慢漂移。
func (h *SystemUpdateHandler) runReleaseCheck(requestCtx context.Context) (systemUpdateCheckResponse, *releaseCheckFailure) {
	status, statusErr := h.updater.Status(requestCtx)
	releaseAvailable := h.releaseUpdater != nil && h.releaseUpdater.Supported()
	gitAvailable := !releaseAvailable && statusErr == nil && status.RemoteURL != ""
	if gitAvailable {
		var err error
		status, err = h.updater.Check(requestCtx)
		if err != nil {
			return systemUpdateCheckResponse{}, &releaseCheckFailure{err: err, updateError: true}
		}
	}

	remoteURL := ""
	if gitAvailable {
		remoteURL = status.RemoteURL
	}
	latest, err := h.latestStableRelease(requestCtx, remoteURL)
	if err != nil {
		return systemUpdateCheckResponse{}, &releaseCheckFailure{status: http.StatusBadGateway, err: err}
	}
	current := strings.TrimSpace(h.buildVersion)
	mode := "release"
	integrity := "sha256"
	var gitStatus *updater.Status
	if gitAvailable {
		current = status.VersionLabel()
		mode = "git"
		integrity = "git-object-hash"
		gitStatus = &status
	} else if releaseAvailable {
		if packageStatus, packageErr := h.releaseUpdater.Status(requestCtx); packageErr == nil {
			status = packageStatus
			if value := packageStatus.VersionLabel(); value != "" {
				current = value
			}
			gitStatus = &packageStatus
		}
	}
	packageReady := false
	if releaseAvailable && latest.ChecksumAvailable {
		_, packageReady = latest.asset(h.releaseUpdater.ExpectedAssetName())
	}
	updateAvailable, versionErr := updateAvailableAgainst(current, latest.Tag)
	if versionErr != nil {
		return systemUpdateCheckResponse{}, &releaseCheckFailure{status: http.StatusInternalServerError, err: versionErr}
	}
	support := h.releaseUpdateSupport(gitAvailable, packageReady, statusErr == nil && status.Root != "")
	// 源码构建不提示更新，避免把用户自己编译的版本当成落后版本自动换掉；
	// 改为提供一个显式的“切换到正式 Release”入口。
	switchToRelease := false
	if h.sourceBuild(gitAvailable) {
		switchToRelease = packageReady
		updateAvailable = false
	}
	checkedAt := h.currentTime().UTC()
	latestPublishedAt := ""
	if !latest.Date.IsZero() {
		latestPublishedAt = latest.Date.UTC().Format(time.RFC3339)
	}
	return systemUpdateCheckResponse{
		DeploymentMode:    mode,
		CurrentVersion:    current,
		LatestVersion:     latest.Tag,
		LatestPublishedAt: latestPublishedAt,
		CheckedAt:         checkedAt.Format(time.RFC3339),
		UpdateAvailable:   updateAvailable || releaseApplyPending(status, gitAvailable),
		UpdateSupported:   support.Supported,
		BuildType:         h.buildType,

		UpdateUnsupportedReason: support.Reason,

		SwitchToReleaseAvailable: switchToRelease,

		IntegrityMode:     integrity,
		ChecksumAvailable: latest.ChecksumAvailable,
		ChecksumURL:       latest.ChecksumURL,
		Status:            gitStatus,
		Policy:            h.currentPolicy(),
	}, nil
}

func (h *SystemUpdateHandler) getPolicy(c *gin.Context) {
	c.JSON(http.StatusOK, h.currentPolicy())
}

func (h *SystemUpdateHandler) savePolicy(c *gin.Context) {
	var policy updater.UpdatePolicy
	if err := c.ShouldBindJSON(&policy); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	policy = normalizeUpdatePolicy(policy)
	if h.policyStore != nil {
		if err := h.policyStore.SaveUpdatePolicy(c.Request.Context(), policy); err != nil {
			h.writeUpdateError(c, "system.update.policy", err)
			return
		}
	}
	h.policyMu.Lock()
	h.policy = policy
	h.policyMu.Unlock()
	if h.mirror != nil {
		h.mirror.SetMode(policy.GitHubMirror)
	}
	recordRequestOperation(c, h.logs, "system.update.policy", "系统更新策略已保存", "", map[string]any{"auto_download": policy.AutoDownload, "auto_install": policy.AutoInstall, "github_mirror": policy.GitHubMirror})
	c.JSON(http.StatusOK, policy)
}

func (h *SystemUpdateHandler) currentPolicy() updater.UpdatePolicy {
	h.policyMu.RLock()
	defer h.policyMu.RUnlock()
	return h.policy
}

func (h *SystemUpdateHandler) currentGitHubToken() string {
	if token := strings.TrimSpace(os.Getenv("DIANA_GITHUB_TOKEN")); token != "" {
		return token
	}
	if token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); token != "" {
		return token
	}
	h.githubTokenMu.RLock()
	defer h.githubTokenMu.RUnlock()
	return strings.TrimSpace(h.githubToken)
}

func (h *SystemUpdateHandler) getGitHubToken(c *gin.Context) {
	configured := h.currentGitHubToken() != ""
	source := ""
	if strings.TrimSpace(os.Getenv("DIANA_GITHUB_TOKEN")) != "" || strings.TrimSpace(os.Getenv("GITHUB_TOKEN")) != "" {
		source = "environment"
	} else if configured {
		source = "stored"
	}
	c.JSON(http.StatusOK, gin.H{"configured": configured, "source": source})
}

func (h *SystemUpdateHandler) saveGitHubToken(c *gin.Context) {
	var request struct {
		Token string `json:"token"`
		Clear bool   `json:"clear"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	token := strings.TrimSpace(request.Token)
	if request.Clear {
		token = ""
	}
	if h.githubTokenStore == nil {
		writeError(c, http.StatusServiceUnavailable, errors.New("GitHub Token 存储不可用"))
		return
	}
	if err := h.githubTokenStore.SaveUpdateGitHubToken(c.Request.Context(), token); err != nil {
		h.writeUpdateError(c, "system.update.github_token", err)
		return
	}
	h.githubTokenMu.Lock()
	h.githubToken = token
	h.githubTokenMu.Unlock()
	// Token 变化后允许立即重新验证，旧 Release 数据仍保留作失败兜底。
	h.releaseCacheMu.Lock()
	h.releaseCache.RateLimitResetAt = time.Time{}
	h.releaseCache.FetchedAt = time.Time{}
	h.releaseCacheMu.Unlock()
	recordRequestOperation(c, h.logs, "system.update.github_token", "GitHub 更新凭据已保存", "", map[string]any{"configured": token != ""})
	h.getGitHubToken(c)
}

func normalizeUpdatePolicy(policy updater.UpdatePolicy) updater.UpdatePolicy {
	if policy.AutoInstall {
		policy.AutoDownload = true
	}
	// 坏的镜像地址不落库：否则每次下载都会先撞一次 404 再回落。
	policy.GitHubMirror = ghmirror.NormalizeMode(policy.GitHubMirror)
	return policy
}

func (h *SystemUpdateHandler) download(c *gin.Context) {
	var request struct {
		Force        bool   `json:"force"`
		Confirmation string `json:"confirmation"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	if request.Confirmation != "download-update" {
		writeError(c, http.StatusBadRequest, errors.New("下载更新需要明确确认"))
		return
	}
	result, err := h.downloadLatestRelease(c.Request.Context(), request.Force)
	if err != nil {
		h.writeUpdateError(c, "system.update.download", err)
		return
	}
	message := "更新包已下载并校验"
	if result.Status.Updating {
		message = "更新包下载任务正在进行"
	}
	recordRequestOperation(c, h.logs, "system.update.download", message, result.TargetCommit, map[string]any{"forced": request.Force})
	c.JSON(http.StatusOK, result)
}

func (h *SystemUpdateHandler) installDownloaded(c *gin.Context) {
	var request struct {
		Confirmation string `json:"confirmation"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	if request.Confirmation != "install-restart" {
		writeError(c, http.StatusBadRequest, errors.New("安装并重启需要明确确认"))
		return
	}
	if h.releaseUpdater == nil || !h.releaseUpdater.Supported() {
		writeError(c, http.StatusBadRequest, updater.ErrReleaseUpdateUnsupported)
		return
	}
	status, err := h.releaseUpdater.Status(c.Request.Context())
	if err != nil {
		h.writeUpdateError(c, "system.update.install", err)
		return
	}
	latest, err := h.latestStableRelease(c.Request.Context(), "")
	if err != nil {
		h.writeUpdateError(c, "system.update.install", err)
		return
	}
	if !downloadReadyForRelease(status, latest.Tag) {
		downloaded := strings.TrimSpace(status.DownloadedVersion)
		if downloaded != "" && !strings.EqualFold(downloaded, latest.Tag) {
			writeError(c, http.StatusConflict, fmt.Errorf("已下载版本 %s 已过期，最新版本是 %s，请先重新下载", downloaded, latest.Tag))
			return
		}
		writeError(c, http.StatusConflict, errors.New("没有可安装的最新版本，请先下载并校验"))
		return
	}
	result, err := h.releaseUpdater.InstallDownloaded(c.Request.Context())
	if err != nil {
		h.writeUpdateError(c, "system.update.install", err)
		return
	}
	recordRequestOperation(c, h.logs, "system.update.install", "已开始安装更新并重启", result.TargetCommit, nil)
	c.JSON(http.StatusOK, result)
}

// update 执行系统更新并记录操作日志。
func (h *SystemUpdateHandler) update(c *gin.Context) {
	var request struct {
		Force        bool   `json:"force"`
		Confirmation string `json:"confirmation"`
	}
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&request); err != nil {
			writeError(c, http.StatusBadRequest, err)
			return
		}
	}
	wantConfirmation := "apply-update"
	confirmationError := "更新需要明确确认"
	if request.Force {
		wantConfirmation = "force-update"
		confirmationError = "强制更新需要明确确认"
	}
	if request.Confirmation != wantConfirmation {
		writeError(c, http.StatusBadRequest, errors.New(confirmationError))
		return
	}

	var result updater.Result
	action := "system.update.pull"
	if request.Force {
		action = "system.update.force"
	}
	result, err := h.applyLatestUpdate(c.Request.Context(), request.Force)
	if err != nil {
		h.writeUpdateError(c, action, err)
		return
	}
	message := "系统更新已执行"
	if request.Force {
		message = "系统已强制同步到远端"
	} else if !result.Updated {
		message = "系统更新已检查"
	}
	recordRequestOperation(c, h.logs, action, message, result.Status.Root, map[string]any{
		"branch":  result.Status.Branch,
		"remote":  result.Status.RemoteURL,
		"updated": result.Updated,
		"fetched": result.Fetched,
		"forced":  result.Forced,
	})
	c.JSON(http.StatusOK, result)
}

// applyLatestUpdate applies the newest stable Release after the HTTP layer has
// verified the user's explicit confirmation.
func (h *SystemUpdateHandler) applyLatestUpdate(ctx context.Context, force bool) (updater.Result, error) {
	status, err := h.updater.Status(ctx)
	releaseAvailable := h.releaseUpdater != nil && h.releaseUpdater.Supported()
	gitAvailable := !releaseAvailable && err == nil && status.RemoteURL != ""
	if !gitAvailable && !releaseAvailable {
		return updater.Result{}, updater.ErrReleaseUpdateUnsupported
	}
	remoteURL := ""
	if gitAvailable {
		remoteURL = status.RemoteURL
	} else {
		status, err = h.releaseUpdater.Status(ctx)
		if err != nil {
			return updater.Result{}, err
		}
	}
	latest, err := h.latestStableRelease(ctx, remoteURL)
	if err != nil {
		return updater.Result{}, err
	}
	if !force {
		updateAvailable, versionErr := updateAvailableAgainst(status.VersionLabel(), latest.Tag)
		if versionErr != nil {
			return updater.Result{}, versionErr
		}
		if !updateAvailable && !releaseApplyPending(status, gitAvailable) {
			return updater.Result{
				Status:       status,
				TargetCommit: status.HeadCommit,
				Output:       "Already at the latest stable release.",
				At:           time.Now(),
			}, nil
		}
	}
	if releaseAvailable {
		archive, ok := latest.asset(h.releaseUpdater.ExpectedAssetName())
		if !ok {
			return updater.Result{}, fmt.Errorf("%w: %s", updater.ErrReleaseAssetMissing, h.releaseUpdater.ExpectedAssetName())
		}
		checksums, ok := latest.asset("SHA256SUMS")
		if !ok {
			return updater.Result{}, updater.ErrChecksumMissing
		}
		return h.releaseUpdater.Download(ctx, updater.ReleasePackage{
			Tag:       latest.Tag,
			Archive:   updater.ReleaseAsset{Name: archive.Name, URL: archive.URL, Size: archive.Size},
			Checksums: updater.ReleaseAsset{Name: checksums.Name, URL: checksums.URL, Size: checksums.Size},
		}, force)
	}
	if force {
		return h.updater.ForceUpdateToRelease(ctx, latest.Tag)
	}
	return h.updater.UpdateToRelease(ctx, latest.Tag)
}

func (h *SystemUpdateHandler) downloadLatestRelease(ctx context.Context, force bool) (updater.Result, error) {
	if h.releaseUpdater == nil || !h.releaseUpdater.Supported() {
		return updater.Result{}, updater.ErrReleaseUpdateUnsupported
	}
	status, err := h.releaseUpdater.Status(ctx)
	if err != nil {
		return updater.Result{}, err
	}
	if status.Updating {
		return releaseOperationInProgressResult(status, status.DownloadedVersion), nil
	}
	latest, err := h.latestStableRelease(ctx, "")
	if err != nil {
		return updater.Result{}, err
	}
	if !force && status.DownloadReady && status.DownloadedVersion == latest.Tag {
		return updater.Result{Status: status, Downloaded: true, TargetCommit: latest.Tag, Output: "Release package is already downloaded and verified.", At: time.Now()}, nil
	}
	if !force {
		updateAvailable, versionErr := updateAvailableAgainst(status.VersionLabel(), latest.Tag)
		if versionErr != nil {
			return updater.Result{}, versionErr
		}
		if !updateAvailable {
			return updater.Result{Status: status, TargetCommit: status.VersionLabel(), Output: "Already at the latest stable release.", At: time.Now()}, nil
		}
	}
	archive, ok := latest.asset(h.releaseUpdater.ExpectedAssetName())
	if !ok {
		return updater.Result{}, fmt.Errorf("%w: %s", updater.ErrReleaseAssetMissing, h.releaseUpdater.ExpectedAssetName())
	}
	checksums, ok := latest.asset("SHA256SUMS")
	if !ok {
		return updater.Result{}, updater.ErrChecksumMissing
	}
	result, err := h.releaseUpdater.Download(ctx, updater.ReleasePackage{
		Tag:       latest.Tag,
		Archive:   updater.ReleaseAsset{Name: archive.Name, URL: archive.URL, Size: archive.Size},
		Checksums: updater.ReleaseAsset{Name: checksums.Name, URL: checksums.URL, Size: checksums.Size},
	}, force)
	if errors.Is(err, updater.ErrUpdateInProgress) {
		current, statusErr := h.releaseUpdater.Status(ctx)
		if statusErr == nil && current.Updating {
			return releaseOperationInProgressResult(current, latest.Tag), nil
		}
	}
	return result, err
}

// StartAutoUpdate owns the process-wide Release check schedule. Browsers only
// edit the persisted policy; the process performs verified download/install.
func (h *SystemUpdateHandler) StartAutoUpdate(ctx context.Context) {
	h.updateSchedulerOnce.Do(func() {
		go func() {
			timer := time.NewTimer(updateCheckDelay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			ticker := time.NewTicker(updateCheckInterval)
			defer ticker.Stop()
			for {
				h.runScheduledUpdate(ctx)
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	})
}

func (h *SystemUpdateHandler) runScheduledUpdate(ctx context.Context) {
	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	remoteURL := ""
	if h.releaseUpdater == nil || !h.releaseUpdater.Supported() {
		if status, err := h.updater.Status(checkCtx); err == nil {
			remoteURL = status.RemoteURL
		}
	}
	if _, err := h.latestStableRelease(checkCtx, remoteURL); err != nil {
		h.recordBackgroundUpdate("system.update.background_check", "后台检查更新失败", err, nil)
		return
	}
	if h.releaseUpdater != nil && h.releaseUpdater.Supported() {
		h.runAutoUpdate(ctx)
	}
}

func (h *SystemUpdateHandler) runAutoUpdate(ctx context.Context) {
	if !h.autoUpdateMu.TryLock() {
		return
	}
	defer h.autoUpdateMu.Unlock()
	// 源码构建只能由用户显式切换到正式版本，后台不会自动替换正在运行的二进制。
	if h.buildType == buildTypeSource {
		return
	}
	policy := h.currentPolicy()
	if !policy.AutoDownload {
		return
	}
	runCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	if policy.AutoInstall {
		latest, latestErr := h.latestStableRelease(runCtx, "")
		status, statusErr := h.releaseUpdater.Status(runCtx)
		if latestErr != nil {
			h.recordBackgroundUpdate("system.update.auto_install", "检查自动安装熔断状态失败", latestErr, nil)
			return
		}
		if statusErr != nil {
			h.recordBackgroundUpdate("system.update.auto_install", "读取自动安装熔断状态失败", statusErr, nil)
			return
		}
		if status.LastUpdateVersion == latest.Tag && status.LastUpdateFailures >= 3 {
			h.recordBackgroundUpdate("system.update.auto_install_blocked", "本版本连续安装失败 3 次，已停止自动重试", nil, map[string]any{
				"target": latest.Tag, "failure_count": status.LastUpdateFailures,
			})
			return
		}
	}
	result, err := h.downloadLatestRelease(runCtx, false)
	if err != nil {
		h.recordBackgroundUpdate("system.update.auto_download", "自动下载更新失败", err, nil)
		return
	}
	if result.Fetched {
		h.recordBackgroundUpdate("system.update.auto_download", "更新包已自动下载并校验", nil, map[string]any{"target": result.TargetCommit})
	}
	if !policy.AutoInstall || !result.Downloaded && !result.Status.DownloadReady {
		return
	}
	installed, err := h.releaseUpdater.InstallDownloaded(runCtx)
	if err != nil {
		h.recordBackgroundUpdate("system.update.auto_install", "自动安装更新失败", err, map[string]any{"target": result.TargetCommit})
		return
	}
	h.recordBackgroundUpdate("system.update.auto_install", "已自动安装更新并开始重启", nil, map[string]any{"target": installed.TargetCommit})
}

func (h *SystemUpdateHandler) recordBackgroundUpdate(action, message string, err error, metadata map[string]any) {
	if h.logs == nil {
		return
	}
	entry := applog.Entry{Kind: applog.KindOperation, Level: applog.LevelInfo, Action: action, Message: message, Metadata: metadata, CreatedAt: time.Now()}
	if err != nil {
		entry.Kind = applog.KindError
		entry.Level = applog.LevelError
		entry.Detail = err.Error()
	}
	_ = h.logs.AppendLog(context.Background(), entry)
}

func releaseOperationInProgressResult(status updater.Status, target string) updater.Result {
	return updater.Result{
		Status:       status,
		TargetCommit: strings.TrimSpace(target),
		Output:       "Release update operation is already in progress.",
		At:           time.Now(),
	}
}

func downloadReadyForRelease(status updater.Status, target string) bool {
	return status.DownloadReady && strings.TrimSpace(status.DownloadedVersion) != "" && strings.EqualFold(strings.TrimSpace(status.DownloadedVersion), strings.TrimSpace(target))
}

func releaseApplyPending(status updater.Status, gitAvailable bool) bool {
	if !gitAvailable || !status.ApplySupported || status.RunningCommit == "" || status.HeadCommit == "" || strings.EqualFold(status.RunningCommit, "dev") {
		return false
	}
	return !sameCommitID(status.RunningCommit, status.HeadCommit)
}

func sameCommitID(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" || right == "" {
		return false
	}
	return left == right || strings.HasPrefix(left, right) || strings.HasPrefix(right, left)
}

func (h *SystemUpdateHandler) latestStableRelease(ctx context.Context, remoteURL string) (ReleaseEntry, error) {
	releases, err := h.recentStableReleases(ctx, remoteURL)
	if err != nil {
		return ReleaseEntry{}, err
	}
	if len(releases) == 0 {
		return ReleaseEntry{}, errors.New("没有可用的稳定 Release")
	}
	return releases[0], nil
}

// recentStableReleases returns the newest stable releases. Rollback applies
// the five-version, older-than-current allowlist after this fetch so a current
// version or a newer release cannot consume one of the rollback slots.
func (h *SystemUpdateHandler) recentStableReleases(ctx context.Context, remoteURL string) ([]ReleaseEntry, error) {
	owner, repo, ok := githubRepoFromRemote(remoteURL)
	if !ok {
		owner, repo = defaultReleaseOwner, defaultReleaseRepo
	}
	releases, err := h.githubReleases(ctx, owner, repo, 30)
	if err != nil {
		return nil, err
	}
	stable := make([]ReleaseEntry, 0, len(releases))
	for _, release := range releases {
		if release.Prerelease || strings.TrimSpace(release.Tag) == "" {
			continue
		}
		stable = append(stable, release)
	}
	return stable, nil
}

// rollback 回退到指定版本。
func (h *SystemUpdateHandler) rollback(c *gin.Context) {
	var payload struct {
		Ref          string `json:"ref"`
		Confirmation string `json:"confirmation"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	if payload.Confirmation != "rollback-version" {
		writeError(c, http.StatusBadRequest, errors.New("版本回退需要明确确认"))
		return
	}
	payload.Ref = strings.TrimSpace(payload.Ref)
	if payload.Ref == "" {
		writeError(c, http.StatusBadRequest, errors.New("版本回退目标不能为空"))
		return
	}

	releaseAvailable := h.releaseUpdater != nil && h.releaseUpdater.Supported()
	remoteURL := ""
	currentVersion := strings.TrimSpace(h.buildVersion)
	if !releaseAvailable {
		status, err := h.updater.Status(c.Request.Context())
		if err != nil {
			h.writeUpdateError(c, "system.update.rollback", err)
			return
		}
		remoteURL = status.RemoteURL
		currentVersion = status.VersionLabel()
	} else if status, err := h.releaseUpdater.Status(c.Request.Context()); err == nil {
		currentVersion = status.VersionLabel()
	}
	releases, err := h.recentStableReleases(c.Request.Context(), remoteURL)
	if err != nil {
		h.writeUpdateError(c, "system.update.rollback", err)
		return
	}
	var target ReleaseEntry
	rollbackSlots := 0
	_, currentSemver := versionParts(currentVersion)
	for _, release := range releases {
		if currentSemver {
			older, versionErr := isNewerVersion(release.Tag, currentVersion)
			if versionErr != nil || !older {
				continue
			}
		} else if release.Tag == currentVersion {
			continue
		}
		if rollbackSlots == maxRollbackReleases {
			break
		}
		rollbackSlots++
		if release.Tag == payload.Ref {
			target = release
			break
		}
	}
	if strings.TrimSpace(target.Tag) == "" {
		writeError(c, http.StatusBadRequest, fmt.Errorf("只能回退最近 %d 个稳定版本", maxRollbackReleases))
		return
	}

	if releaseAvailable {
		archive, ok := target.asset(h.releaseUpdater.ExpectedAssetName())
		if !ok {
			writeError(c, http.StatusBadRequest, fmt.Errorf("目标版本缺少完整 Release 包：%s", h.releaseUpdater.ExpectedAssetName()))
			return
		}
		checksums, ok := target.asset("SHA256SUMS")
		if !ok {
			writeError(c, http.StatusBadRequest, updater.ErrChecksumMissing)
			return
		}
		if _, err := h.releaseUpdater.Download(c.Request.Context(), updater.ReleasePackage{
			Tag:       target.Tag,
			Archive:   updater.ReleaseAsset{Name: archive.Name, URL: archive.URL, Size: archive.Size},
			Checksums: updater.ReleaseAsset{Name: checksums.Name, URL: checksums.URL, Size: checksums.Size},
		}, true); err != nil {
			h.writeUpdateError(c, "system.update.rollback", err)
			return
		}
		result, err := h.releaseUpdater.InstallDownloaded(c.Request.Context())
		if err != nil {
			h.writeUpdateError(c, "system.update.rollback", err)
			return
		}
		recordRequestOperation(c, h.logs, "system.update.rollback", "已开始回退到 "+target.Tag+" 并重启", target.Tag, map[string]any{
			"ref":             payload.Ref,
			"deployment_mode": "release",
		})
		c.JSON(http.StatusOK, gin.H{"result": result})
		return
	}

	result, err := h.updater.Rollback(c.Request.Context(), payload.Ref)
	if err != nil {
		h.writeUpdateError(c, "system.update.rollback", err)
		return
	}
	recordRequestOperation(c, h.logs, "system.update.rollback", "系统已回退到 "+result.Status.HeadCommit, result.Status.Root, map[string]any{
		"ref": payload.Ref,
	})
	c.JSON(http.StatusOK, gin.H{"result": result})
}

// changelogList 返回 GitHub 更新日志：源码部署使用 origin，Release/Docker 使用官方仓库。
// 优先返回 Release；仓库尚未发布 Release 时回退为最近提交，带短缓存。
func (h *SystemUpdateHandler) changelogList(c *gin.Context) {
	status := updater.Status{}
	if h.releaseUpdater == nil || !h.releaseUpdater.Supported() {
		status, _ = h.updater.Status(c.Request.Context())
	}
	owner, repo, ok := githubRepoFromRemote(status.RemoteURL)
	if !ok {
		owner, repo = defaultReleaseOwner, defaultReleaseRepo
	}
	branch := strings.TrimSpace(status.Branch)
	if branch == "" {
		branch = "main"
	}
	cacheKey := owner + "/" + repo + "@" + branch

	h.changelog.mu.Lock()
	if h.changelog.key == cacheKey && time.Since(h.changelog.fetchedAt) < changelogCacheTTL {
		payload := h.changelog.payload
		h.changelog.mu.Unlock()
		payload["cached"] = true
		c.JSON(http.StatusOK, payload)
		return
	}
	h.changelog.mu.Unlock()

	payload := gin.H{"repo": owner + "/" + repo}
	cacheKeyReleases := fmt.Sprintf("%s/%s?per_page=%d", strings.TrimSpace(owner), strings.TrimSpace(repo), 10)
	releases, cached := h.cachedReleases(cacheKeyReleases, h.currentTime(), true)
	if !cached {
		h.releaseCacheMu.RLock()
		shared := h.releaseCache
		h.releaseCacheMu.RUnlock()
		prefix := strings.TrimSpace(owner) + "/" + strings.TrimSpace(repo) + "?"
		if strings.HasPrefix(shared.Key, prefix) && !shared.FetchedAt.IsZero() {
			releases = releaseEntriesFromCache(shared.Releases)
			if len(releases) > 10 {
				releases = releases[:10]
			}
			cached = true
		}
	}
	var err error
	if !cached && strings.TrimSpace(h.githubAPIBase) != "" {
		releases, err = h.githubReleases(c.Request.Context(), owner, repo, 10)
	} else if !cached && strings.EqualFold(owner, defaultReleaseOwner) && strings.EqualFold(repo, defaultReleaseRepo) {
		releases, err = h.fetchStaticReleaseManifest(c.Request.Context())
	}
	if !cached && len(releases) == 0 && err == nil {
		err = errors.New("尚无版本历史缓存，请点击检查更新")
	}
	if err == nil && len(releases) > 0 {
		payload["kind"] = "releases"
		payload["releases"] = releases
	} else {
		var rateLimitErr *githubRateLimitError
		if errors.As(err, &rateLimitErr) {
			writeError(c, http.StatusBadGateway, rateLimitErr)
			return
		}
		if resetAt, limited := h.activeGitHubRateLimit(h.currentTime()); limited {
			writeError(c, http.StatusBadGateway, &githubRateLimitError{StatusCode: http.StatusForbidden, ResetAt: resetAt})
			return
		}
		// 没有正式 Release（或列表拉取失败）时退回提交记录，前端会标注来源。
		entries, commitErr := fetchGitHubChangelog(c.Request.Context(), h.httpClient, h.githubAPIBase, owner, repo, branch, 20)
		if commitErr != nil {
			if errors.As(commitErr, &rateLimitErr) {
				h.rememberGitHubRateLimit(c.Request.Context(), rateLimitErr.ResetAt)
			}
			writeError(c, http.StatusBadGateway, commitErr)
			return
		}
		payload["kind"] = "commits"
		payload["entries"] = entries
	}
	h.changelog.mu.Lock()
	h.changelog.key = cacheKey
	h.changelog.payload = payload
	h.changelog.fetchedAt = time.Now()
	h.changelog.mu.Unlock()
	c.JSON(http.StatusOK, payload)
}

// writeUpdateError 记录系统更新错误并返回响应。
func (h *SystemUpdateHandler) writeUpdateError(c *gin.Context, action string, err error) {
	if errors.Is(err, updater.ErrRemoteNotConfigured) || errors.Is(err, updater.ErrReleaseUpdateUnsupported) {
		writeError(c, http.StatusBadRequest, errors.New("当前为 Release/Docker 部署，更新由部署环境的镜像更新器管理"))
		return
	}
	if errors.Is(err, errInvalidUpdateVersion) {
		logAndWriteError(c, h.logs, http.StatusInternalServerError, action, err, "", nil)
		return
	}
	logAndWriteError(c, h.logs, http.StatusBadRequest, action, err, "", nil)
}

// preferredVersionLabel 在编译版本号和仓库版本标签之间挑一个像版本号的。
// 仓库没打过 tag 时 VersionLabel() 会退回提交短号，把它当版本号显示等于告诉
// 用户「你的版本是 3095b85」——既看不出新旧，也没法和 Release 对比。这种时候
// 宁可用编译期注入的版本号。
func preferredVersionLabel(buildVersion, repositoryLabel string) string {
	repositoryLabel = strings.TrimSpace(repositoryLabel)
	if repositoryLabel == "" {
		return buildVersion
	}
	if _, ok := versionParts(repositoryLabel); ok {
		return repositoryLabel
	}
	if _, ok := versionParts(buildVersion); ok {
		return buildVersion
	}
	return repositoryLabel
}

func deploymentMode(gitAvailable bool) string {
	if gitAvailable {
		return "git"
	}
	return "release"
}

func latestStableRelease(releases []ReleaseEntry) ReleaseEntry {
	for _, release := range releases {
		if !release.Prerelease && strings.TrimSpace(release.Tag) != "" {
			return release
		}
	}
	return ReleaseEntry{}
}

// updateAvailableAgainst 判断当前运行版本是否落后于目标 Release。
// 当前版本无法解析成语义化版本时（例如没有注入版本号的本地构建），
// 视为落后于任何正式 Release，让用户可以直接装回正式版本，
// 而不是因为版本号不可比较就报错或永远不提示更新。
func updateAvailableAgainst(current, latest string) (bool, error) {
	latestParts, latestOK := versionParts(latest)
	if !latestOK {
		return false, fmt.Errorf("%w：最新版本 %q 无法解析，要求格式为 vX.Y.Z", errInvalidUpdateVersion, latest)
	}
	currentParts, currentOK := versionParts(current)
	if !currentOK {
		return true, nil
	}
	for i := range currentParts {
		if latestParts[i] != currentParts[i] {
			return latestParts[i] > currentParts[i], nil
		}
	}
	return false, nil
}

func isNewerVersion(current, latest string) (bool, error) {
	currentParts, currentOK := versionParts(current)
	if !currentOK {
		return false, fmt.Errorf("%w：当前版本 %q 无法解析，要求格式为 vX.Y.Z", errInvalidUpdateVersion, current)
	}
	latestParts, latestOK := versionParts(latest)
	if !latestOK {
		return false, fmt.Errorf("%w：最新版本 %q 无法解析，要求格式为 vX.Y.Z", errInvalidUpdateVersion, latest)
	}
	for i := range currentParts {
		if latestParts[i] != currentParts[i] {
			return latestParts[i] > currentParts[i], nil
		}
	}
	return false, nil
}

func versionParts(value string) ([3]int, bool) {
	var parts [3]int
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	if index := strings.IndexAny(value, "+-"); index >= 0 {
		value = value[:index]
	}
	raw := strings.Split(value, ".")
	if len(raw) != len(parts) {
		return parts, false
	}
	for i, item := range raw {
		parsed, err := strconv.Atoi(item)
		if err != nil || parsed < 0 {
			return parts, false
		}
		parts[i] = parsed
	}
	return parts, true
}

// writeUpdateHTTPError 返回系统更新状态查询错误。
func writeUpdateHTTPError(c *gin.Context, err error) {
	if errors.Is(err, updater.ErrRemoteNotConfigured) {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	writeError(c, http.StatusBadRequest, err)
}

// ReleaseStatus 让机器人在聊天里答得出「有没有新版本」。它复用 HTTP 检查那一套
// 结论，判断逻辑（源码构建不提示更新、平台包缺失算不支持）只有一份。
//
// 实现 assistant.ReleaseStatusProvider。
func (h *SystemUpdateHandler) ReleaseStatus(ctx context.Context) (assistant.ReleaseStatus, error) {
	response, failure := h.runReleaseCheck(ctx)
	if failure != nil {
		return assistant.ReleaseStatus{}, failure.err
	}
	status := assistant.ReleaseStatus{
		RepositoryURL:            h.repositoryURL(ctx),
		DeploymentMode:           response.DeploymentMode,
		CurrentVersion:           response.CurrentVersion,
		LatestVersion:            response.LatestVersion,
		UpdateAvailable:          response.UpdateAvailable,
		UpdateSupported:          response.UpdateSupported,
		UnsupportedReason:        response.UpdateUnsupportedReason,
		SwitchToReleaseAvailable: response.SwitchToReleaseAvailable,
	}
	if publishedAt, err := time.Parse(time.RFC3339, response.LatestPublishedAt); err == nil {
		status.LatestPublishedAt = publishedAt
	}
	if checkedAt, err := time.Parse(time.RFC3339, response.CheckedAt); err == nil {
		status.CheckedAt = checkedAt
	}
	return status, nil
}

// repositoryURL 返回这个部署实际跟随的仓库地址：源码部署跟着 git 远端走，
// 其余回落到官方仓库。机器人被问到「源码在哪」时用它，免得自己编一个链接。
func (h *SystemUpdateHandler) repositoryURL(ctx context.Context) string {
	remoteURL := ""
	if h.updater != nil {
		if status, err := h.updater.Status(ctx); err == nil {
			remoteURL = status.RemoteURL
		}
	}
	owner, repo, ok := githubRepoFromRemote(remoteURL)
	if !ok {
		owner, repo = defaultReleaseOwner, defaultReleaseRepo
	}
	return "https://github.com/" + owner + "/" + repo
}
