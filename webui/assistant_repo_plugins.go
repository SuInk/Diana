// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/SuInk/diana/model/assistant"

	"github.com/gin-gonic/gin"
)

// repoPluginURLPayload 是预览/安装请求体：一个 GitHub 仓库链接。
type repoPluginURLPayload struct {
	URL string `json:"url"`
}

// repoPluginInstallPayload 是安装请求体。AcceptRisk 必须由前端在用户勾选
// 「我已了解风险」后显式置 true，服务端不接受默认值——确认框不点勾不能装。
// Commit 是预览时读到的提交，安装时必须一致，保证装的就是确认框里那一版。
type repoPluginInstallPayload struct {
	URL        string `json:"url"`
	AcceptRisk bool   `json:"accept_risk"`
	Commit     string `json:"commit"`
	// Replace 必须由前端在用户看过「将覆盖已装的 x.y.z」之后显式置 true。
	// 同 ID 覆盖会连带接管已有的设置与凭据，不能静默发生。
	Replace bool `json:"replace,omitempty"`
}

// repoPluginPreviewResponse 在预览之外补一条「这个 ID 已经被谁占着」。用嵌入而
// 不是手写字段表：预览结构以后加字段，这里不会悄悄漏掉。
type repoPluginPreviewResponse struct {
	assistant.RepoPluginPreview
	Installed *assistant.RepoPluginInstalledVersion `json:"installed,omitempty"`
}

// repoPluginInstalledVersion 查这个 ID 现在被谁占着，返回 nil 表示没被占用。
func (h *BotHandler) repoPluginInstalledVersion(id, next string) *assistant.RepoPluginInstalledVersion {
	manager := h.runtime.Plugins()
	if manager == nil {
		return nil
	}
	state, ok := manager.Get(id)
	if !ok {
		return nil
	}
	if state.Manifest.BuiltIn {
		return &assistant.RepoPluginInstalledVersion{Version: state.Manifest.Version, BuiltIn: true}
	}
	if !state.Installed {
		return nil
	}
	return &assistant.RepoPluginInstalledVersion{
		Version: state.Manifest.Version,
		Change:  assistant.CompareRepoPluginVersions(next, state.Manifest.Version),
	}
}

// repoPluginOccupancyGuard 在落盘之前拦住「顶掉内置」和「静默覆盖同 ID」。
func (h *BotHandler) repoPluginOccupancyGuard(replace bool) func(assistant.PluginManifest) error {
	return func(incoming assistant.PluginManifest) error {
		occupied := h.repoPluginInstalledVersion(incoming.ID, incoming.Version)
		if occupied == nil {
			return nil
		}
		if occupied.BuiltIn {
			return fmt.Errorf("%w: %s 是内置插件，不能被第三方插件替换",
				assistant.ErrBuiltInPluginAction, incoming.ID)
		}
		if !replace {
			return fmt.Errorf("diana: %s 已安装 %s 版，这次是 %s 版；确认覆盖后再安装",
				incoming.ID, occupied.Version, incoming.Version)
		}
		return nil
	}
}

// SetRepoPluginInstaller 注入第三方插件安装器；未注入时相关接口返回 501，
// 老的纯内置插件部署不受影响。
func (h *BotHandler) SetRepoPluginInstaller(installer *assistant.RepoPluginInstaller) {
	h.repoPlugins = installer
}

// SetRepoPluginSourceStore 注入第三方插件来源记录存储。
func (h *BotHandler) SetRepoPluginSourceStore(store *assistant.RepoPluginStore) {
	h.repoPluginSources = store
}

func (h *BotHandler) repoPluginInstaller(c *gin.Context) (*assistant.RepoPluginInstaller, bool) {
	if h.repoPlugins == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "第三方插件安装器未配置"})
		return nil, false
	}
	return h.repoPlugins, true
}

// previewRepoPlugin 拉取并校验仓库清单，返回渲染安装确认框所需的
// 权限、设置与风险信息。只触清单和声明文件，不下载仓库归档。
func (h *BotHandler) previewRepoPlugin(c *gin.Context) {
	installer, ok := h.repoPluginInstaller(c)
	if !ok {
		return
	}
	var payload repoPluginURLPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "plugin_repo_preview", err, "", nil)
		return
	}
	preview, err := installer.Preview(c.Request.Context(), payload.URL)
	if err != nil {
		h.writeRepoPluginError(c, "plugin_repo_preview", err, payload.URL)
		return
	}
	c.JSON(http.StatusOK, repoPluginPreviewResponse{
		RepoPluginPreview: preview,
		Installed:         h.repoPluginInstalledVersion(preview.Manifest.ID, preview.Manifest.Version),
	})
}

// installRepoPlugin 安装第三方插件：校验、下载归档、落盘、登记进插件管理器
// 并持久化状态。更新同 ID 插件走同一接口，RegisterPlugin 保留既有开关与设置。
func (h *BotHandler) installRepoPlugin(c *gin.Context) {
	installer, ok := h.repoPluginInstaller(c)
	if !ok {
		return
	}
	var payload repoPluginInstallPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "plugin_repo_install", err, "", nil)
		return
	}
	if !payload.AcceptRisk {
		h.writeError(c, http.StatusBadRequest, "plugin_repo_install", assistant.ErrRepoPluginRisk, payload.URL, nil)
		return
	}
	if strings.TrimSpace(payload.Commit) == "" {
		h.writeError(c, http.StatusBadRequest, "plugin_repo_install", errors.New("diana: 请先预览插件，确认后再安装"), payload.URL, nil)
		return
	}
	plugin, source, err := installer.InstallGuarded(c.Request.Context(), payload.URL, payload.Commit,
		h.repoPluginOccupancyGuard(payload.Replace))
	if err != nil {
		h.writeRepoPluginError(c, "plugin_repo_install", err, payload.URL)
		return
	}
	manager := h.runtime.Plugins()
	if err := manager.RegisterPlugin(plugin); err != nil {
		h.writeError(c, http.StatusConflict, "plugin_repo_install", err, plugin.Manifest().ID, nil)
		return
	}
	if state, ok := manager.Get(plugin.Manifest().ID); !ok || !state.Installed {
		if _, err := manager.Install(plugin.Manifest().ID); err != nil {
			h.writePluginError(c, "plugin_repo_install", err, plugin.Manifest().ID)
			return
		}
	}
	if h.repoPluginSources != nil {
		if err := h.repoPluginSources.Save(source); err != nil {
			h.writeError(c, http.StatusInternalServerError, "plugin_repo_install", err, plugin.Manifest().ID, nil)
			return
		}
	}
	h.persistState()
	recordRequestOperation(c, h.logs, "plugin_repo_install", "第三方插件已安装", plugin.Manifest().ID, map[string]any{
		"plugin_id": plugin.Manifest().ID,
		"version":   plugin.Manifest().Version,
		"source":    source.Owner + "/" + source.Repo,
		"ref":       source.Ref,
		"commit":    source.Commit,
	})
	state, _ := manager.Get(plugin.Manifest().ID)
	c.JSON(http.StatusOK, h.withRepoSource(state).Redacted())
}

// updateRepoPlugin 按安装时记录的出处重新拉取安装，用于插件更新。
// 开关与设置由 RegisterPlugin 保留；未确认风险同样不能更新。
func (h *BotHandler) updateRepoPlugin(c *gin.Context) {
	installer, ok := h.repoPluginInstaller(c)
	if !ok {
		return
	}
	if h.repoPluginSources == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "第三方插件来源记录未配置"})
		return
	}
	var payload repoPluginInstallPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "plugin_repo_update", err, c.Param("id"), nil)
		return
	}
	if !payload.AcceptRisk {
		h.writeError(c, http.StatusBadRequest, "plugin_repo_update", assistant.ErrRepoPluginRisk, c.Param("id"), nil)
		return
	}
	source, ok := h.repoPluginSources.Get(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "该插件不是从仓库安装的"})
		return
	}
	installURL := source.URL
	if strings.TrimSpace(source.Ref) != "" {
		installURL += "/tree/" + source.Ref
	}
	plugin, next, err := installer.Install(c.Request.Context(), installURL, payload.Commit)
	if err != nil {
		h.writeRepoPluginError(c, "plugin_repo_update", err, source.ID)
		return
	}
	if plugin.Manifest().ID != source.ID {
		h.writeError(c, http.StatusConflict, "plugin_repo_update",
			errors.New("diana: 更新后的插件 ID 发生变化，拒绝替换"), source.ID, nil)
		return
	}
	manager := h.runtime.Plugins()
	if err := manager.RegisterPlugin(plugin); err != nil {
		h.writeError(c, http.StatusConflict, "plugin_repo_update", err, source.ID, nil)
		return
	}
	if err := h.repoPluginSources.Save(next); err != nil {
		h.writeError(c, http.StatusInternalServerError, "plugin_repo_update", err, source.ID, nil)
		return
	}
	h.persistState()
	recordRequestOperation(c, h.logs, "plugin_repo_update", "第三方插件已更新", source.ID, map[string]any{
		"plugin_id": source.ID,
		"version":   next.Version,
		"source":    next.Owner + "/" + next.Repo,
		"ref":       next.Ref,
		"commit":    next.Commit,
	})
	state, _ := manager.Get(source.ID)
	c.JSON(http.StatusOK, h.withRepoSource(state).Redacted())
}

// removeRepoPluginSources 卸载时清理第三方插件的落盘目录与来源记录。
// 不是仓库插件时静默返回，普通插件卸载不受影响。
func (h *BotHandler) removeRepoPluginSources(id string) {
	if h.repoPluginSources == nil || h.repoPlugins == nil {
		return
	}
	if _, ok := h.repoPluginSources.Get(id); !ok {
		return
	}
	if err := assistant.RemoveRepoPlugin(h.repoPlugins.DataDir, id); err != nil {
		return
	}
	_ = h.repoPluginSources.Remove(id)
}

// writeRepoPluginError 按第三方插件错误类型映射 HTTP 状态码：
// 链接与格式问题归 400（用户可修正），上游拉取失败归 statusUpstreamFailed。
func (h *BotHandler) writeRepoPluginError(c *gin.Context, action string, err error, target string) {
	status := statusUpstreamFailed
	switch {
	case errors.Is(err, assistant.ErrRepoPluginURL),
		errors.Is(err, assistant.ErrRepoPluginManifest),
		errors.Is(err, assistant.ErrRepoPluginFormat),
		errors.Is(err, assistant.ErrRepoPluginSkill),
		errors.Is(err, assistant.ErrRepoPluginRisk),
		errors.Is(err, assistant.ErrRepoPluginCommit):
		status = http.StatusBadRequest
	case errors.Is(err, assistant.ErrRepoPluginChanged):
		status = http.StatusConflict
	}
	h.writeError(c, status, action, err, target, map[string]any{"plugin_id": target})
}
