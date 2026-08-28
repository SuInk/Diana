// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/SuInk/diana/model/ghmirror"
)

// GitHubMirrorSelector 是下载线路选择器。webui 只需要「按当前策略挑一条」和
// 「实测一遍给用户看」两件事，具体实现在 model/ghmirror。
type GitHubMirrorSelector interface {
	SetMode(mode string)
	Mode() string
	Base(ctx context.Context, probeURL string) string
	Probe(ctx context.Context, probeURL string) []ghmirror.ProbeResult
	LastProbe() []ghmirror.ProbeResult
}

var (
	errUnavailableMirrorSelector = errors.New("当前部署没有启用下载加速")
	errMissingMirrorProbeTarget  = errors.New("最新版本没有可用于测速的下载地址")
)

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

// testMirrors 实测每条线路。
func (h *SystemUpdateHandler) testMirrors(c *gin.Context) {
	if h.mirror == nil {
		writeError(c, http.StatusServiceUnavailable, errUnavailableMirrorSelector)
		return
	}
	probeURL, err := h.mirrorProbeURL(c.Request.Context())
	if err != nil {
		writeError(c, http.StatusBadGateway, err)
		return
	}
	results := h.mirror.Probe(c.Request.Context(), probeURL)
	c.JSON(http.StatusOK, githubMirrorResponse{
		Mode:      h.currentPolicy().GitHubMirror,
		Mirrors:   ghmirror.Builtin(),
		Resolved:  h.mirror.Base(c.Request.Context(), probeURL),
		LastProbe: results,
	})
}

// mirrorProbeURL 找一个真实的下载地址当测速样本。
//
// 优先取安装包本身：测速要真的拉一段数据下来才算得出速率，而校验清单只有
// 几 KB，还没进入稳定传输就读完了，测出来的其实还是延时。拿不到安装包时才
// 退回清单——那种情况下只剩连通性可测，总比什么都不测强。
func (h *SystemUpdateHandler) mirrorProbeURL(ctx context.Context) (string, error) {
	latest, err := h.latestStableRelease(ctx, "")
	if err != nil {
		return "", err
	}
	if h.releaseUpdater != nil {
		if archive, ok := latest.asset(h.releaseUpdater.ExpectedAssetName()); ok && strings.TrimSpace(archive.URL) != "" {
			return archive.URL, nil
		}
	}
	if checksums, ok := latest.asset("SHA256SUMS"); ok && strings.TrimSpace(checksums.URL) != "" {
		return checksums.URL, nil
	}
	if strings.TrimSpace(latest.ChecksumURL) != "" {
		return latest.ChecksumURL, nil
	}
	return "", errMissingMirrorProbeTarget
}
