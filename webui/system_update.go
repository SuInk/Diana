package webui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/updater"

	"github.com/gin-gonic/gin"
	"golang.org/x/sync/singleflight"
)

const (
	defaultReleaseOwner = "SuInk"
	defaultReleaseRepo  = "Diana"
)

var errInvalidUpdateVersion = errors.New("更新版本号无效")

type systemUpdateCheckResponse struct {
	DeploymentMode    string          `json:"deployment_mode"`
	CurrentVersion    string          `json:"current_version"`
	LatestVersion     string          `json:"latest_version,omitempty"`
	UpdateAvailable   bool            `json:"update_available"`
	UpdateSupported   bool            `json:"update_supported"`
	IntegrityMode     string          `json:"integrity_mode"`
	ChecksumAvailable bool            `json:"checksum_available"`
	ChecksumURL       string          `json:"checksum_url,omitempty"`
	Status            *updater.Status `json:"status,omitempty"`
}

type ReleasePackageUpdater interface {
	Supported() bool
	ExpectedAssetName() string
	Status(context.Context) (updater.Status, error)
	Download(context.Context, updater.ReleasePackage, bool) (updater.Result, error)
	InstallDownloaded(context.Context) (updater.Result, error)
	Install(context.Context, updater.ReleasePackage, bool) (updater.Result, error)
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
	httpClient            *http.Client
	githubAPIBase         string
	changelog             changelogCache
	releaseCacheStore     ReleaseCacheStore
	releaseCacheMu        sync.RWMutex
	releaseCachePersistMu sync.Mutex
	releaseCache          persistedReleaseCache
	releaseFetch          singleflight.Group
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
		updater:    systemUpdater,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// SetLogStore 注入系统更新操作日志写入器。
func (h *SystemUpdateHandler) SetLogStore(store AppLogWriter) {
	h.logs = store
}

// SetBuildVersion 注入编译期版本号，git 不可用（如容器部署）时兜底展示。
func (h *SystemUpdateHandler) SetBuildVersion(version string) {
	h.buildVersion = version
}

// Register 注册系统更新状态和执行接口。
func (h *SystemUpdateHandler) Register(router gin.IRouter) {
	router.GET("/api/system/version", h.version)
	router.GET("/api/system/update", h.status)
	router.POST("/api/system/update", h.update)
	router.POST("/api/system/update/download", h.download)
	router.POST("/api/system/update/install", h.installDownloaded)
	router.POST("/api/system/update/check", h.check)
	router.POST("/api/system/update/rollback", h.rollback)
	router.GET("/api/system/update/changelog", h.changelogList)
}

// version 返回版本信息；git 状态可选，容器等非 git 部署时只有编译版本。
func (h *SystemUpdateHandler) version(c *gin.Context) {
	payload := gin.H{"build_version": h.buildVersion, "update_supported": false}
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
		if v := status.VersionLabel(); v != "" {
			// 侧栏展示语义化版本（tag 或 tag+N），只有仓库完全没有 tag 时才退回提交短号。
			label = v
		}
		payload["update_supported"] = gitAvailable
	} else {
		payload["git_available"] = false
		payload["deployment_mode"] = "release"
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
func (h *SystemUpdateHandler) check(c *gin.Context) {
	status, statusErr := h.updater.Status(c.Request.Context())
	releaseAvailable := h.releaseUpdater != nil && h.releaseUpdater.Supported()
	gitAvailable := !releaseAvailable && statusErr == nil && status.RemoteURL != ""
	if gitAvailable {
		var err error
		status, err = h.updater.Check(c.Request.Context())
		if err != nil {
			h.writeUpdateError(c, "system.update.check", err)
			return
		}
	}

	remoteURL := ""
	if gitAvailable {
		remoteURL = status.RemoteURL
	}
	latest, err := h.latestStableRelease(c.Request.Context(), remoteURL)
	if err != nil {
		writeError(c, http.StatusBadGateway, err)
		return
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
		if packageStatus, packageErr := h.releaseUpdater.Status(c.Request.Context()); packageErr == nil {
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
	updateAvailable, versionErr := isNewerVersion(current, latest.Tag)
	if versionErr != nil {
		writeError(c, http.StatusInternalServerError, versionErr)
		return
	}
	c.JSON(http.StatusOK, systemUpdateCheckResponse{
		DeploymentMode:    mode,
		CurrentVersion:    current,
		LatestVersion:     latest.Tag,
		UpdateAvailable:   updateAvailable || releaseApplyPending(status, gitAvailable),
		UpdateSupported:   gitAvailable || packageReady,
		IntegrityMode:     integrity,
		ChecksumAvailable: latest.ChecksumAvailable,
		ChecksumURL:       latest.ChecksumURL,
		Status:            gitStatus,
	})
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
	recordRequestOperation(c, h.logs, "system.update.download", "更新包已下载并校验", result.TargetCommit, map[string]any{"forced": request.Force})
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
		updateAvailable, versionErr := isNewerVersion(status.VersionLabel(), latest.Tag)
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
	latest, err := h.latestStableRelease(ctx, "")
	if err != nil {
		return updater.Result{}, err
	}
	if !force && status.DownloadReady && status.DownloadedVersion == latest.Tag {
		return updater.Result{Status: status, Downloaded: true, TargetCommit: latest.Tag, Output: "Release package is already downloaded and verified.", At: time.Now()}, nil
	}
	if !force {
		updateAvailable, versionErr := isNewerVersion(status.VersionLabel(), latest.Tag)
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
	return h.releaseUpdater.Download(ctx, updater.ReleasePackage{
		Tag:       latest.Tag,
		Archive:   updater.ReleaseAsset{Name: archive.Name, URL: archive.URL, Size: archive.Size},
		Checksums: updater.ReleaseAsset{Name: checksums.Name, URL: checksums.URL, Size: checksums.Size},
	}, force)
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
	owner, repo, ok := githubRepoFromRemote(remoteURL)
	if !ok {
		owner, repo = defaultReleaseOwner, defaultReleaseRepo
	}
	releases, err := h.githubReleases(ctx, owner, repo, 10)
	if err != nil {
		return ReleaseEntry{}, err
	}
	latest := latestStableRelease(releases)
	if strings.TrimSpace(latest.Tag) == "" {
		return ReleaseEntry{}, errors.New("没有可用的稳定 Release")
	}
	return latest, nil
}

// rollback 回退到指定版本。
func (h *SystemUpdateHandler) rollback(c *gin.Context) {
	if h.releaseUpdater != nil && h.releaseUpdater.Supported() {
		writeError(c, http.StatusBadRequest, errors.New("Release 包仅在健康检查失败时自动回退；手动回退请重新安装目标完整包"))
		return
	}
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
	releases, err := h.githubReleases(c.Request.Context(), owner, repo, 10)
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
