// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/updater"
)

func TestManualUpdateForHostPlatforms(t *testing.T) {
	tests := []struct {
		goos    string
		shell   string
		command string
	}{
		{goos: "darwin", shell: "sh", command: manualInstallShellCommand},
		{goos: "linux", shell: "sh", command: manualInstallShellCommand},
		{goos: "android", shell: "sh", command: manualInstallTermuxCommand},
		{goos: "windows", shell: "powershell", command: manualInstallPowerShellCommand},
	}
	for _, tt := range tests {
		t.Run(tt.goos, func(t *testing.T) {
			got := manualUpdateFor(false, tt.goos, "release", "")
			if got.Deployment != "host" || got.Shell != tt.shell || got.Command != tt.command || got.Image != "" {
				t.Fatalf("manualUpdateFor(%s) = %+v", tt.goos, got)
			}
			if strings.Contains(got.Note, "正式版") {
				t.Fatalf("正式版通道不该提示装回正式版: %q", got.Note)
			}
		})
	}
	if note := manualUpdateFor(false, "linux", "canary", "").Note; !strings.Contains(note, "一键命令安装的是最新正式版") {
		t.Fatalf("Canary 通道的宿主机命令要说明装的是正式版: %q", note)
	}
}

func TestManualUpdateForDockerFollowsChannel(t *testing.T) {
	tests := []struct {
		channel string
		current string
		image   string
	}{
		{channel: "release", current: "ghcr.io/suink/diana:latest", image: "ghcr.io/suink/diana:latest"},
		{channel: "", current: "", image: "ghcr.io/suink/diana:latest"},
		{channel: "beta", current: "ghcr.io/suink/diana:beta", image: "ghcr.io/suink/diana:beta"},
		{channel: "canary", current: "ghcr.io/suink/diana:canary-slim", image: "ghcr.io/suink/diana:canary-slim"},
		// 版本面板切了通道但镜像还没改：给出要改成的标签，保留基础版。
		{channel: "canary", current: "ghcr.io/suink/diana:latest-slim", image: "ghcr.io/suink/diana:canary-slim"},
		{channel: "beta", current: "ghcr.io/suink/diana:latest", image: "ghcr.io/suink/diana:beta"},
		// 自建镜像不猜基础版。
		{channel: "release", current: "registry.example.test/diana:v1-slim", image: "ghcr.io/suink/diana:latest"},
	}
	for _, tt := range tests {
		got := manualUpdateFor(true, "linux", tt.channel, tt.current)
		if got.Deployment != "docker" || got.Shell != "sh" || got.Command != manualInstallDockerCommand || got.Image != tt.image {
			t.Fatalf("manualUpdateFor(docker, %q, %q) = %+v, want image %s", tt.channel, tt.current, got, tt.image)
		}
		if !strings.Contains(got.Note, tt.image) {
			t.Fatalf("说明里要写出镜像 %s: %q", tt.image, got.Note)
		}
		// Docker 部署跑在 Linux 容器里，不能因为 GOOS 给出宿主机安装命令。
		if strings.Contains(got.Command, "install.sh") {
			t.Fatalf("Docker 部署给了宿主机命令: %q", got.Command)
		}
	}
}

// TestManualUpdateCommandsMatchREADME 保证兜底命令与 README 逐字一致：README 是发布
// 基线，这里改了或 README 改了，都要让两边重新对上。
func TestManualUpdateCommandsMatchREADME(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join("..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	lines := map[string]bool{}
	for _, line := range strings.Split(string(readme), "\n") {
		lines[strings.TrimSpace(line)] = true
	}
	for _, command := range []string{manualInstallShellCommand, manualInstallTermuxCommand, manualInstallPowerShellCommand, manualInstallDockerCommand} {
		if !lines[command] {
			t.Errorf("README 里没有这一整行命令：%s", command)
		}
	}
	compose, err := os.ReadFile(filepath.Join("..", "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(compose), dockerImageRepository+"latest") {
		t.Errorf("docker-compose.yml 的默认镜像不再是 %slatest", dockerImageRepository)
	}
}

func TestSystemUpdateDownloadFailureIncludesManualCommand(t *testing.T) {
	const assetName = "diana-macos-arm64.tar.gz"
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"tag_name":"v0.8.7","published_at":"2026-08-09T10:00:00Z","assets":[{"name":"SHA256SUMS","browser_download_url":"https://example.test/SHA256SUMS"},{"name":"` + assetName + `","browser_download_url":"https://example.test/package.tar.gz","size":1234}]}]`))
	}))
	defer github.Close()

	releaseUpdater := &recordingReleasePackageUpdater{
		expected:    assetName,
		status:      updater.Status{NearestTag: "v0.8.6", RunningCommit: "v0.8.6", ApplySupported: true},
		downloadErr: errors.New("SHA-256 不匹配"),
	}
	handler := NewSystemUpdateHandler(fakeSystemUpdater{err: updater.ErrRepositoryNotFound})
	handler.SetReleasePackageUpdater(releaseUpdater)
	handler.githubAPIBase = github.URL
	router := systemUpdateTestRouter(handler)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/system/update/download", strings.NewReader(`{"confirmation":"download-update"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error        string               `json:"error"`
		ManualUpdate *manualUpdateCommand `json:"manual_update"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error != "SHA-256 不匹配" || body.ManualUpdate == nil || body.ManualUpdate.Deployment != "host" || body.ManualUpdate.Command == "" {
		t.Fatalf("body = %s", rec.Body.String())
	}

	// 另一次更新正在进行不算失败，不劝用户手动重装。
	releaseUpdater.downloadErr = updater.ErrUpdateInProgress
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/system/update/download", strings.NewReader(`{"confirmation":"download-update","force":true}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "manual_update") {
		t.Fatalf("进行中的更新不该附手动命令: %s", rec.Body.String())
	}
}

func TestSystemVersionIncludesManualCommandForDocker(t *testing.T) {
	handler := NewSystemUpdateHandler(fakeSystemUpdater{err: updater.ErrRepositoryNotFound})
	handler.SetDockerDeployment(NewDockerUpdateTrigger("ghcr.io/suink/diana:latest-slim", "token"))
	router := systemUpdateTestRouter(handler)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/system/version", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		ManualUpdate *manualUpdateCommand `json:"manual_update"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ManualUpdate == nil || body.ManualUpdate.Command != manualInstallDockerCommand || body.ManualUpdate.Image != "ghcr.io/suink/diana:latest-slim" {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestSystemVersionOmitsManualCommandForGitCheckout(t *testing.T) {
	handler := NewSystemUpdateHandler(fakeSystemUpdater{status: updater.Status{RemoteURL: "https://github.com/SuInk/Diana.git"}})
	router := systemUpdateTestRouter(handler)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/system/version", nil))
	if strings.Contains(rec.Body.String(), "manual_update") {
		t.Fatalf("源码部署不该给一键安装命令: %s", rec.Body.String())
	}
}
