package webui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/desktopctl"
)

func TestDesktopControlRequiresLoginAndHumanConfirmation(t *testing.T) {
	router, auth, _ := newAuthTestRouter(t)
	if err := auth.SetPassword("desktop-test-secret"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	registry := desktopctl.NewRegistry(ctx, nil)
	hub := desktopctl.NewHub(registry)
	local := desktopctl.NewLocalService(hub, "")
	defer local.Close()
	jobs := desktopctl.NewJobManager(ctx, nil)
	NewDesktopControlHandler(registry, hub, local, jobs).Register(router)
	request := func(method, path, body, cookie string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		router.ServeHTTP(rec, req)
		return rec
	}
	for _, tc := range []struct{ method, path string }{{"GET", "status"}, {"GET", "jobs"}, {"PUT", "policy"}, {"POST", "takeover"}, {"POST", "jobs/test/confirm"}, {"POST", "permissions/request"}, {"POST", "reconnect"}} {
		if rec := request(tc.method, "/api/desktop-control/"+tc.path, "{}", ""); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: %d", tc.path, rec.Code)
		}
	}
	login := request("POST", "/api/auth/login", fmt.Sprintf(`{"username":%q,"password":"desktop-test-secret"}`, auth.Username()), "")
	if login.Code != 200 {
		t.Fatalf("login: %s", login.Body.String())
	}
	cookie := strings.Split(login.Header().Get("Set-Cookie"), ";")[0]
	if rec := request("PUT", "/api/desktop-control/policy", `{"enabled":true,"write_enabled":false,"allowed_apps":["com.apple.TextEdit"]}`, cookie); rec.Code != 200 || !registry.Policy().Enabled {
		t.Fatalf("policy: %s", rec.Body.String())
	}
	job, err := jobs.Create(ctx, "draft", "owner", "", desktopctl.JobBudget{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = jobs.WaitConfirm(ctx, job.ID, "check draft"); err != nil {
		t.Fatal(err)
	}
	if rec := request("POST", "/api/desktop-control/jobs/"+job.ID+"/confirm", "", cookie); rec.Code != 200 {
		t.Fatalf("confirm: %s", rec.Body.String())
	}
	got, _ := jobs.Get(job.ID)
	if got.Status != desktopctl.JobRunning || !got.NeedsReobserve {
		t.Fatalf("job=%+v", got)
	}
	if rec := request("POST", "/api/desktop-control/jobs/"+job.ID+"/cancel", "", cookie); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	got, _ = jobs.Get(job.ID)
	if got.Status != desktopctl.JobCancelled {
		t.Fatal(got.Status)
	}
	// 只认两种权限；执行器没起来时说清楚先做什么，不去开系统设置。
	opened := 0
	restore := openDesktopSettingsPane
	defer func() { openDesktopSettingsPane = restore }()
	openDesktopSettingsPane = func(string) error { opened++; return nil }
	if rec := request("POST", "/api/desktop-control/permissions/request", `{"kind":"files"}`, cookie); rec.Code != 400 {
		t.Fatalf("bad kind: %d", rec.Code)
	}
	if rec := request("POST", "/api/desktop-control/permissions/request", `{"kind":"accessibility"}`, cookie); rec.Code != 409 || !strings.Contains(rec.Body.String(), "执行器") || opened != 0 {
		t.Fatalf("no executor: %d %s opened=%d", rec.Code, rec.Body.String(), opened)
	}
	if rec := request("POST", "/api/desktop-control/reconnect", "", cookie); rec.Code != 200 {
		t.Fatalf("reconnect: %s", rec.Body.String())
	}
}

// 一键安装跑在 Diana.app 里，系统设置列表显示 Diana；手动部署显示程序文件名，要给路径。
func TestDesktopAuthorizeTarget(t *testing.T) {
	if got := authorizeTargetFor("/opt/diana/Diana.app/Contents/MacOS/diana-webui"); got.Name != "Diana" || !got.Bundle || got.Path != "/opt/diana/Diana.app" {
		t.Fatalf("bundle: %+v", got)
	}
	if got := authorizeTargetFor("/srv/diana/runtime/diana-webui"); got.Name != "diana-webui" || got.Bundle || !strings.HasSuffix(got.Path, "runtime/diana-webui") {
		t.Fatalf("bare: %+v", got)
	}
}
