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
	if err := auth.SetPassword("", "desktop-test-secret"); err != nil {
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
	for _, tc := range []struct{ method, path string }{{"GET", "status"}, {"GET", "jobs"}, {"PUT", "policy"}, {"POST", "takeover"}, {"POST", "jobs/test/confirm"}} {
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
}
