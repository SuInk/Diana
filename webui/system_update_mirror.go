// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
)

// GitHubMirrorSelector 是下载线路选择器。界面上只有「自动」和「直连」两档，
// webui 这边就只需要下发策略和按策略挑一条，具体实现在 model/ghmirror。
type GitHubMirrorSelector interface {
	SetMode(mode string)
	Mode() string
	Base(ctx context.Context, probeURL string) string
}

// SetGitHubMirrorSelector 注入下载线路选择器，并把已保存的策略同步给它。
func (h *SystemUpdateHandler) SetGitHubMirrorSelector(selector GitHubMirrorSelector) {
	h.mirror = selector
	if selector == nil {
		return
	}
	selector.SetMode(h.currentPolicy().GitHubMirror)
}
