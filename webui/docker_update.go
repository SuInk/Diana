// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/SuInk/diana/model/updater"
)

// DockerUpdateTrigger talks only to the private update helper on the Compose
// network. The application container never receives the Docker socket.
type DockerUpdateTrigger struct {
	Image  string
	Token  string
	URL    string
	Client *http.Client
}

type dockerUpdateRejected struct{ message string }

func (e *dockerUpdateRejected) Error() string { return e.message }

func NewDockerUpdateTrigger(image, token string) *DockerUpdateTrigger {
	if strings.TrimSpace(token) == "" {
		return nil
	}
	return &DockerUpdateTrigger{
		Image: strings.TrimSpace(image),
		Token: strings.TrimSpace(token),
		URL:   "http://diana-updater:8080/v1/update",
		Client: &http.Client{
			Timeout:       10 * time.Minute,
			Transport:     &http.Transport{Proxy: nil},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func (d *DockerUpdateTrigger) Trigger(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+d.Token)
	resp, err := d.Client.Do(req)
	if err != nil {
		return fmt.Errorf("Docker 更新助手不可用：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &dockerUpdateRejected{message: fmt.Sprintf("Docker 更新助手返回 %d：%s", resp.StatusCode, strings.TrimSpace(string(body)))}
	}
	return nil
}

func (d *DockerUpdateTrigger) Ready(ctx context.Context) error {
	readyCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	url := strings.TrimSuffix(d.URL, "/v1/update") + "/readyz"
	req, err := http.NewRequestWithContext(readyCtx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := d.Client.Do(req)
	if err != nil {
		return fmt.Errorf("Docker 更新助手未就绪：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Docker 更新助手未就绪：HTTP %d", resp.StatusCode)
	}
	return nil
}

// The mutable Docker tag must point to the same release family selected in
// the WebUI. Watchtower cannot change a container's image reference.
func dockerImageChannel(image string) string {
	const prefix = "ghcr.io/suink/diana:"
	if !strings.HasPrefix(image, prefix) {
		return ""
	}
	tag := strings.TrimPrefix(image, prefix)
	tag = strings.TrimSuffix(tag, "-slim")
	switch tag {
	case "latest":
		return "release"
	case "beta", "canary":
		return tag
	default:
		return ""
	}
}

func dockerTagMatchesRelease(channel, release string) bool {
	switch channel {
	case "release":
		return !strings.Contains(release, "-")
	case "beta":
		return strings.Contains(release, "-beta.") || strings.Contains(release, "-rc.")
	case "canary":
		return strings.Contains(release, "-canary.")
	default:
		return false
	}
}

func (h *SystemUpdateHandler) dockerUpdateSupport(channel, latestTag string) updateSupportSummary {
	if h.dockerUpdater == nil {
		return updateSupportSummary{Reason: "Docker 自更新助手未启用；请在原部署目录重新运行 Docker 安装脚本。"}
	}
	imageChannel := dockerImageChannel(h.dockerUpdater.Image)
	if imageChannel == "" {
		return updateSupportSummary{Reason: "当前 Docker 镜像不是可更新的官方滚动标签；请将 DIANA_IMAGE 设为 latest、beta 或 canary 后重建容器。"}
	}
	if imageChannel != channel {
		return updateSupportSummary{Reason: fmt.Sprintf("当前 Docker 镜像使用 %s 通道，版本面板选的是 %s；请在部署目录修改 DIANA_IMAGE 并重建容器。", imageChannel, channel)}
	}
	if latestTag != "" && !dockerTagMatchesRelease(channel, latestTag) {
		return updateSupportSummary{Reason: "当前通道最新 Release 与 Docker 滚动标签不一致；请在部署主机切换镜像标签后重建容器。"}
	}
	return updateSupportSummary{Supported: true}
}

func (h *SystemUpdateHandler) startDockerUpdate(target string) error {
	if h.dockerUpdater == nil {
		return updater.ErrReleaseUpdateUnsupported
	}
	h.dockerUpdateMu.Lock()
	if h.dockerUpdateRunning || (h.dockerUpdateTarget == target && time.Since(h.dockerUpdateAt) < 30*time.Minute) {
		h.dockerUpdateMu.Unlock()
		return updater.ErrUpdateInProgress
	}
	h.dockerUpdateRunning = true
	h.dockerUpdateTarget = target
	h.dockerUpdateAt = time.Now()
	h.dockerUpdateError = ""
	h.dockerUpdateMu.Unlock()
	go func() {
		defer recoverGoroutinePanic("docker_update.go:startDockerUpdate")
		// Let the HTTP handler deliver its accepted response before the helper
		// replaces this very container and closes the connection.
		time.Sleep(500 * time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		err := h.dockerUpdater.Trigger(ctx)
		h.dockerUpdateMu.Lock()
		h.dockerUpdateRunning = false
		if err != nil {
			var rejected *dockerUpdateRejected
			if errors.As(err, &rejected) {
				h.dockerUpdateError = err.Error()
			}
			h.dockerUpdateAt = time.Time{}
		}
		h.dockerUpdateMu.Unlock()
		h.recordBackgroundUpdate("system_update_docker", "Docker 更新助手已检查镜像并重建容器", err, map[string]any{"target": target})
	}()
	return nil
}

func (h *SystemUpdateHandler) applyDockerUpdate(ctx context.Context) (updater.Result, error) {
	latest, err := h.latestChannelRelease(ctx, "")
	if err != nil {
		return updater.Result{}, err
	}
	support := h.dockerUpdateSupport(h.currentPolicy().Channel, latest.Tag)
	if !support.Supported {
		return updater.Result{}, fmt.Errorf("%w: %s", updater.ErrReleaseUpdateUnsupported, support.Reason)
	}
	status := updater.Status{NearestTag: h.buildVersion, ApplySupported: true}
	available, err := updateAvailableAgainst(h.buildVersion, latest.Tag)
	if err != nil {
		return updater.Result{}, err
	}
	if !available {
		return updater.Result{Status: status, TargetCommit: h.buildVersion, Output: "当前镜像已是所选通道最新版本。", At: time.Now()}, nil
	}
	if err := h.dockerUpdater.Ready(ctx); err != nil {
		return updater.Result{}, err
	}
	if err := h.startDockerUpdate(latest.Tag); err != nil {
		return updater.Result{}, err
	}
	return updater.Result{Status: status, Updated: true, TargetCommit: latest.Tag, Output: "已请求 Docker 更新助手拉取镜像并重建容器。", At: time.Now()}, nil
}

func (h *SystemUpdateHandler) runAutoDockerUpdate(target string) {
	policy := h.currentPolicy()
	if !policy.DockerAutoInstall || !h.dockerUpdateSupport(policy.Channel, target).Supported {
		return
	}
	available, err := updateAvailableAgainst(h.buildVersion, target)
	if err != nil {
		h.recordBackgroundUpdate("system_update_docker", "比较 Docker 镜像版本失败", err, nil)
		return
	}
	if available {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err := h.dockerUpdater.Ready(ctx)
		cancel()
		if err != nil {
			h.recordBackgroundUpdate("system_update_docker", "Docker 更新助手未就绪", err, nil)
			return
		}
		if err := h.startDockerUpdate(target); err != nil && err != updater.ErrUpdateInProgress {
			h.recordBackgroundUpdate("system_update_docker", "请求 Docker 自动更新失败", err, map[string]any{"target": target})
		}
	}
}
