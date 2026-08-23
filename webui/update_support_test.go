// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/updater"
)

// 升不了级时必须给出原因。以前这种情况和「已经是最新」返回同样的结果，
// 界面只能渲染成「已是最新」，用户会以为自己已经升过了。
func TestSystemUpdateCheckExplainsWhyUpdateIsUnsupported(t *testing.T) {
	github := releaseTestServer(t, "v9.9.9")
	defer github.Close()

	releaseUpdater := &recordingReleasePackageUpdater{
		unsupportedReason: "frontend directory is outside the package root",
	}
	handler := NewSystemUpdateHandler(fakeSystemUpdater{err: updater.ErrRepositoryNotFound})
	handler.SetReleasePackageUpdater(releaseUpdater)
	handler.SetBuildVersion("v0.1.0")
	handler.githubAPIBase = github.URL
	router := systemUpdateTestRouter(handler)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/system/update/check", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"update_supported":false`) {
		t.Fatalf("update should be unsupported: %s", body)
	}
	if !strings.Contains(body, "前端目录不在可执行文件所在目录之内") {
		t.Fatalf("reason was not explained to the user: %s", body)
	}
	// 版本比较照常进行：界面要能同时说出「有新版本」和「但你升不了」。
	if !strings.Contains(body, `"latest_version":"v9.9.9"`) {
		t.Fatalf("latest version should still be reported: %s", body)
	}
}

// 内部英文原因要翻译成用户能照着做的说明，翻译不了的也不能吞掉。
func TestDescribeUpdateUnsupported(t *testing.T) {
	cases := map[string]string{
		"":                                   "",
		"SQLite database is missing":         "找不到 SQLite 数据库文件，自更新前无法备份数据。",
		"unsupported operating system plan9": "当前操作系统没有对应的 Release 包，只能手动部署。",
		`running executable "diana" is not the packaged binary "diana-webui"`: "正在运行的可执行文件不是 Release 包里的那个（可能是自行构建或改过名），自更新无法确认要替换谁。",
	}
	for reason, want := range cases {
		if got := describeUpdateUnsupported(reason); got != want {
			t.Fatalf("describeUpdateUnsupported(%q) = %q, want %q", reason, got, want)
		}
	}
	if got := describeUpdateUnsupported("something entirely new"); !strings.Contains(got, "something entirely new") {
		t.Fatalf("unknown reasons must not be swallowed: %q", got)
	}
}

// 仓库没打过 tag 时，VersionLabel() 会退回提交短号。把提交号当版本号显示既看不出
// 新旧也没法和 Release 对比，这种时候要用编译期注入的版本号。
func TestPreferredVersionLabelIgnoresBareCommits(t *testing.T) {
	if got := preferredVersionLabel("v0.8.54-dev", "3095b85"); got != "v0.8.54-dev" {
		t.Fatalf("label = %q, want the build version", got)
	}
	if got := preferredVersionLabel("v0.8.54-dev", "v0.8.60+3"); got != "v0.8.60+3" {
		t.Fatalf("label = %q, want the repository tag", got)
	}
	if got := preferredVersionLabel("dev", "3095b85"); got != "3095b85" {
		t.Fatalf("label = %q, want the commit when nothing else is comparable", got)
	}
	if got := preferredVersionLabel("v0.8.54", ""); got != "v0.8.54" {
		t.Fatalf("label = %q, want the build version", got)
	}
}
