package webui

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/SuInk/diana/model/updater"

	"github.com/gin-gonic/gin"
)

const (
	defaultReleaseOwner = "SuInk"
	defaultReleaseRepo  = "Diana"
)

type systemUpdateCheckResponse struct {
	DeploymentMode    string          `json:"deployment_mode"`
	CurrentVersion    string          `json:"current_version"`
	LatestVersion     string          `json:"latest_version,omitempty"`
	UpdateAvailable   bool            `json:"update_available"`
	IntegrityMode     string          `json:"integrity_mode"`
	ChecksumAvailable bool            `json:"checksum_available"`
	ChecksumURL       string          `json:"checksum_url,omitempty"`
	Status            *updater.Status `json:"status,omitempty"`
}

type SystemUpdater interface {
	Status(context.Context) (updater.Status, error)
	Check(context.Context) (updater.Status, error)
	Update(context.Context) (updater.Result, error)
	ForceUpdate(context.Context) (updater.Result, error)
	Rollback(ctx context.Context, ref string) (updater.Result, error)
}

type SystemUpdateHandler struct {
	updater       SystemUpdater
	logs          AppLogWriter
	auto          *AutoUpdater
	buildVersion  string
	httpClient    *http.Client
	githubAPIBase string
	changelog     changelogCache
}

// NewSystemUpdateHandler 创建系统更新接口处理器。
func NewSystemUpdateHandler(updater SystemUpdater) *SystemUpdateHandler {
	return &SystemUpdateHandler{
		updater:    updater,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// SetLogStore 注入系统更新操作日志写入器。
func (h *SystemUpdateHandler) SetLogStore(store AppLogWriter) {
	h.logs = store
}

// SetAutoUpdater 注入自动更新循环，启用相关设置接口。
func (h *SystemUpdateHandler) SetAutoUpdater(auto *AutoUpdater) {
	h.auto = auto
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
	router.POST("/api/system/update/check", h.check)
	router.POST("/api/system/update/rollback", h.rollback)
	router.GET("/api/system/update/changelog", h.changelogList)
	router.GET("/api/system/update/settings", h.getSettings)
	router.POST("/api/system/update/settings", h.saveSettings)
}

// version 返回版本信息；git 状态可选，容器等非 git 部署时只有编译版本。
func (h *SystemUpdateHandler) version(c *gin.Context) {
	payload := gin.H{"build_version": h.buildVersion}
	label := h.buildVersion
	if status, err := h.updater.Status(c.Request.Context()); err == nil {
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
	} else {
		payload["git_available"] = false
		payload["deployment_mode"] = "release"
	}
	payload["version_label"] = label
	c.JSON(http.StatusOK, payload)
}

// status 处理系统更新状态查询请求。
func (h *SystemUpdateHandler) status(c *gin.Context) {
	status, err := h.updater.Status(c.Request.Context())
	if err != nil {
		// 状态页会频繁轮询，查询失败只返回 HTTP 错误，避免把日志中心刷满。
		writeUpdateHTTPError(c, err)
		return
	}
	c.JSON(http.StatusOK, status)
}

// check 在源码部署中 fetch Git，在 Release/Docker 部署中查询 GitHub Release。
func (h *SystemUpdateHandler) check(c *gin.Context) {
	status, statusErr := h.updater.Status(c.Request.Context())
	if statusErr == nil && status.RemoteURL != "" {
		status, err := h.updater.Check(c.Request.Context())
		if err != nil {
			h.writeUpdateError(c, "system.update.check", err)
			return
		}
		current := status.VersionLabel()
		c.JSON(http.StatusOK, systemUpdateCheckResponse{
			DeploymentMode:  "git",
			CurrentVersion:  current,
			UpdateAvailable: status.Behind > 0,
			IntegrityMode:   "git-object-hash",
			Status:          &status,
		})
		return
	}

	releases, err := fetchGitHubReleases(c.Request.Context(), h.httpClient, h.githubAPIBase, defaultReleaseOwner, defaultReleaseRepo, 10)
	if err != nil {
		writeError(c, http.StatusBadGateway, err)
		return
	}
	latest := latestStableRelease(releases)
	current := strings.TrimSpace(h.buildVersion)
	c.JSON(http.StatusOK, systemUpdateCheckResponse{
		DeploymentMode:    "release",
		CurrentVersion:    current,
		LatestVersion:     latest.Tag,
		UpdateAvailable:   isNewerVersion(current, latest.Tag),
		IntegrityMode:     "sha256",
		ChecksumAvailable: latest.ChecksumAvailable,
		ChecksumURL:       latest.ChecksumURL,
	})
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
	if request.Force && request.Confirmation != "force-update" {
		writeError(c, http.StatusBadRequest, errors.New("强制更新需要明确确认"))
		return
	}

	var (
		result updater.Result
		err    error
	)
	action := "system.update.pull"
	if request.Force {
		action = "system.update.force"
		result, err = h.updater.ForceUpdate(c.Request.Context())
	} else {
		result, err = h.updater.Update(c.Request.Context())
	}
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

// rollback 回退到指定版本；成功后自动关闭自动更新，避免下个周期又被拉回最新。
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
	result, err := h.updater.Rollback(c.Request.Context(), payload.Ref)
	if err != nil {
		h.writeUpdateError(c, "system.update.rollback", err)
		return
	}
	autoDisabled := false
	if h.auto != nil && h.auto.Settings().AutoUpdateEnabled {
		if _, err := h.auto.SaveSettings(c.Request.Context(), updater.Settings{
			AutoUpdateEnabled: false,
			IntervalMinutes:   h.auto.Settings().IntervalMinutes,
		}); err == nil {
			autoDisabled = true
		}
	}
	recordRequestOperation(c, h.logs, "system.update.rollback", "系统已回退到 "+result.Status.HeadCommit, result.Status.Root, map[string]any{
		"ref":                payload.Ref,
		"auto_update_paused": autoDisabled,
	})
	c.JSON(http.StatusOK, gin.H{"result": result, "auto_update_disabled": autoDisabled})
}

// changelogList 返回 GitHub 更新日志：源码部署使用 origin，Release/Docker 使用官方仓库。
// 优先返回 Release；仓库尚未发布 Release 时回退为最近提交，带短缓存。
func (h *SystemUpdateHandler) changelogList(c *gin.Context) {
	status, _ := h.updater.Status(c.Request.Context())
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
	releases, err := fetchGitHubReleases(c.Request.Context(), h.httpClient, h.githubAPIBase, owner, repo, 10)
	if err == nil && len(releases) > 0 {
		payload["kind"] = "releases"
		payload["releases"] = releases
	} else {
		// 没有正式 Release（或列表拉取失败）时退回提交记录，前端会标注来源。
		entries, commitErr := fetchGitHubChangelog(c.Request.Context(), h.httpClient, h.githubAPIBase, owner, repo, branch, 20)
		if commitErr != nil {
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

// getSettings 返回自动更新设置与最近一次自动更新结果。
func (h *SystemUpdateHandler) getSettings(c *gin.Context) {
	if h.auto == nil {
		writeError(c, http.StatusNotFound, errors.New("自动更新未启用"))
		return
	}
	lastRunAt, lastResult, lastError := h.auto.LastRun()
	payload := gin.H{
		"settings":        h.auto.Settings(),
		"deployment_mode": h.currentDeploymentMode(c.Request.Context()),
	}
	if !lastRunAt.IsZero() {
		payload["last_run_at"] = lastRunAt
		payload["last_result"] = lastResult
		payload["last_error"] = lastError
	}
	c.JSON(http.StatusOK, payload)
}

// saveSettings 校验并保存自动更新设置。
func (h *SystemUpdateHandler) saveSettings(c *gin.Context) {
	if h.auto == nil {
		writeError(c, http.StatusNotFound, errors.New("自动更新未启用"))
		return
	}
	var settings updater.Settings
	if err := c.ShouldBindJSON(&settings); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	saved, err := h.auto.SaveSettings(c.Request.Context(), settings)
	if err != nil {
		logAndWriteError(c, h.logs, http.StatusInternalServerError, "system.update.settings", err, "", nil)
		return
	}
	state := "关闭"
	if saved.AutoUpdateEnabled {
		state = "开启"
	}
	recordRequestOperation(c, h.logs, "system.update.settings", "自动更新已"+state, "", map[string]any{
		"interval_minutes": saved.IntervalMinutes,
	})
	c.JSON(http.StatusOK, gin.H{
		"settings":        saved,
		"deployment_mode": h.currentDeploymentMode(c.Request.Context()),
	})
}

// writeUpdateError 记录系统更新错误并返回响应。
func (h *SystemUpdateHandler) writeUpdateError(c *gin.Context, action string, err error) {
	if errors.Is(err, updater.ErrRemoteNotConfigured) {
		writeError(c, http.StatusBadRequest, errors.New("当前为 Release/Docker 部署，更新由部署环境的镜像更新器管理"))
		return
	}
	logAndWriteError(c, h.logs, http.StatusBadRequest, action, err, "", nil)
}

func (h *SystemUpdateHandler) currentDeploymentMode(ctx context.Context) string {
	status, err := h.updater.Status(ctx)
	return deploymentMode(err == nil && status.RemoteURL != "")
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

func isNewerVersion(current, latest string) bool {
	currentParts, currentOK := versionParts(current)
	latestParts, latestOK := versionParts(latest)
	if !currentOK || !latestOK {
		return false
	}
	for i := range currentParts {
		if latestParts[i] != currentParts[i] {
			return latestParts[i] > currentParts[i]
		}
	}
	return false
}

func versionParts(value string) ([3]int, bool) {
	var parts [3]int
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	value = strings.SplitN(value, "-", 2)[0]
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
