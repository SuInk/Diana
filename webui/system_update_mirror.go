// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/SuInk/diana/model/ghmirror"
)

// GitHubMirrorSelector 是下载线路选择器。webui 只需要「按当前策略挑一条」，
// 具体实现在 model/ghmirror。
type GitHubMirrorSelector interface {
	SetMode(mode string)
	Mode() string
	Base(ctx context.Context, probeURL string) string
	LastProbe() []ghmirror.ProbeResult
}

type githubMirrorResponse struct {
	Mode      string                 `json:"mode"`
	Mirrors   []ghmirror.Mirror      `json:"mirrors"`
	Resolved  string                 `json:"resolved,omitempty"`
	LastProbe []ghmirror.ProbeResult `json:"last_probe,omitempty"`
}

// SetGitHubMirrorSelector 注入下载线路选择器，并把已保存的策略同步给它。
func (h *SystemUpdateHandler) SetGitHubMirrorSelector(selector GitHubMirrorSelector) {
	h.mirror = selector
	if selector == nil {
		return
	}
	selector.SetMode(h.currentPolicy().GitHubMirror)
}

// mirrors 返回可选线路、当前策略和最近一次实测结果。
func (h *SystemUpdateHandler) mirrors(c *gin.Context) {
	response := githubMirrorResponse{
		Mode:    h.currentPolicy().GitHubMirror,
		Mirrors: ghmirror.Builtin(),
	}
	if h.mirror != nil {
		response.LastProbe = h.mirror.LastProbe()
	}
	c.JSON(http.StatusOK, response)
}
