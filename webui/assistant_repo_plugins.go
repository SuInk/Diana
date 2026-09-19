// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"errors"
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
type repoPluginInstallPayload struct {
	URL        string `json:"url"`
	AcceptRisk bool   `json:"accept_risk"`
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
		h.writeError(c, http.StatusBadRequest, "assistant.plugin.repo.preview", err, "", nil)
		return
	}
	preview, err := installer.Preview(c.Request.Context(), payload.URL)
	if err != nil {
		h.writeRepoPluginError(c, "assistant.plugin.repo.preview", err, payload.URL)
		return
	}
	c.JSON(http.StatusOK, preview)
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
		h.writeError(c, http.StatusBadRequest, "assistant.plugin.repo.install", err, "", nil)
		return
	}
	if !payload.AcceptRisk {
		h.writeError(c, http.StatusBadRequest, "assistant.plugin.repo.install", assistant.ErrRepoPluginRisk, payload.URL, nil)
		return
	}
	plugin, source, err := installer.Install(c.Request.Context(), payload.URL)
	if err != nil {
		h.writeRepoPluginError(c, "assistant.plugin.repo.install", err, payload.URL)
		return
	}
	manager := h.runtime.Plugins()
	if err := manager.RegisterPlugin(plugin); err != nil {
		h.writeError(c, http.StatusConflict, "assistant.plugin.repo.install", err, plugin.Manifest().ID, nil)
		return
	}
	if state, ok := manager.Get(plugin.Manifest().ID); !ok || !state.Installed {
		if _, err := manager.Install(plugin.Manifest().ID); err != nil {
			h.writePluginError(c, "assistant.plugin.repo.install", err, plugin.Manifest().ID)
			return
		}
	}
	if h.repoPluginSources != nil {
		if err := h.repoPluginSources.Save(source); err != nil {
			h.writeError(c, http.StatusInternalServerError, "assistant.plugin.repo.install", err, plugin.Manifest().ID, nil)
			return
		}
	}
	h.persistState()
	recordRequestOperation(c, h.logs, "assistant.plugin.repo.install", "第三方插件已安装", plugin.Manifest().ID, map[string]any{
		"plugin_id": plugin.Manifest().ID,
		"version":   plugin.Manifest().Version,
		"source":    source.Owner + "/" + source.Repo,
		"ref":       source.Ref,
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
		h.writeError(c, http.StatusBadRequest, "assistant.plugin.repo.update", err, c.Param("id"), nil)
		return
	}
	if !payload.AcceptRisk {
		h.writeError(c, http.StatusBadRequest, "assistant.plugin.repo.update", assistant.ErrRepoPluginRisk, c.Param("id"), nil)
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
	plugin, next, err := installer.Install(c.Request.Context(), installURL)
	if err != nil {
		h.writeRepoPluginError(c, "assistant.plugin.repo.update", err, source.ID)
		return
	}
	if plugin.Manifest().ID != source.ID {
		h.writeError(c, http.StatusConflict, "assistant.plugin.repo.update",
			errors.New("diana: 更新后的插件 ID 发生变化，拒绝替换"), source.ID, nil)
		return
	}
	manager := h.runtime.Plugins()
	if err := manager.RegisterPlugin(plugin); err != nil {
		h.writeError(c, http.StatusConflict, "assistant.plugin.repo.update", err, source.ID, nil)
		return
	}
	if err := h.repoPluginSources.Save(next); err != nil {
		h.writeError(c, http.StatusInternalServerError, "assistant.plugin.repo.update", err, source.ID, nil)
		return
	}
	h.persistState()
	recordRequestOperation(c, h.logs, "assistant.plugin.repo.update", "第三方插件已更新", source.ID, map[string]any{
		"plugin_id": source.ID,
		"version":   next.Version,
		"source":    next.Owner + "/" + next.Repo,
		"ref":       next.Ref,
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
// 链接与格式问题归 400（用户可修正），上游拉取失败归 502。
func (h *BotHandler) writeRepoPluginError(c *gin.Context, action string, err error, target string) {
	status := http.StatusBadGateway
	switch {
	case errors.Is(err, assistant.ErrRepoPluginURL),
		errors.Is(err, assistant.ErrRepoPluginManifest),
		errors.Is(err, assistant.ErrRepoPluginFormat),
		errors.Is(err, assistant.ErrRepoPluginSkill),
		errors.Is(err, assistant.ErrRepoPluginRisk):
		status = http.StatusBadRequest
	}
	h.writeError(c, status, action, err, target, map[string]any{"plugin_id": target})
}
