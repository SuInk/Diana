package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SuInk/diana/model/updater"

	"github.com/gin-gonic/gin"
)

type fakeSystemUpdater struct {
	status updater.Status
	result updater.Result
	err    error
}

// Status 返回当前状态快照。
func (f fakeSystemUpdater) Status(context.Context) (updater.Status, error) {
	if f.err != nil {
		return updater.Status{}, f.err
	}
	return f.status, nil
}

// Update 封装当前模块的 Update 逻辑。
func (f fakeSystemUpdater) Update(context.Context) (updater.Result, error) {
	if f.err != nil {
		return updater.Result{}, f.err
	}
	return f.result, nil
}

// Check 返回 fetch 后的状态快照。
func (f fakeSystemUpdater) Check(context.Context) (updater.Status, error) {
	if f.err != nil {
		return updater.Status{}, f.err
	}
	return f.status, nil
}

// Rollback 返回回退后的结果快照。
func (f fakeSystemUpdater) Rollback(_ context.Context, ref string) (updater.Result, error) {
	if f.err != nil {
		return updater.Result{}, f.err
	}
	result := f.result
	result.Updated = true
	if result.Status.HeadCommit == "" {
		result.Status.HeadCommit = ref
	}
	return result, nil
}

// TestSystemUpdateHandlerStatus 验证对应功能场景。
func TestSystemUpdateHandlerStatus(t *testing.T) {
	handler := NewSystemUpdateHandler(fakeSystemUpdater{
		status: updater.Status{
			Root:       "/tmp/repo",
			Branch:     "main",
			RemoteName: "origin",
			RemoteURL:  "https://github.com/example/repo.git",
			HeadCommit: "abc1234",
			Dirty:      true,
		},
	})
	router := systemUpdateTestRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/api/system/update", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload updater.Status
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if payload.Root != "/tmp/repo" || payload.Branch != "main" || !payload.Dirty {
		t.Fatalf("payload = %#v", payload)
	}
}

// TestSystemUpdateHandlerRemoteMissing 验证对应功能场景。
func TestSystemUpdateHandlerRemoteMissing(t *testing.T) {
	handler := NewSystemUpdateHandler(fakeSystemUpdater{err: updater.ErrRemoteNotConfigured})
	router := systemUpdateTestRouter(handler)

	req := httptest.NewRequest(http.MethodPost, "/api/system/update", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// TestSystemUpdateHandlerUpdate 验证对应功能场景。
func TestSystemUpdateHandlerUpdate(t *testing.T) {
	now := time.Now()
	handler := NewSystemUpdateHandler(fakeSystemUpdater{
		result: updater.Result{
			Status: updater.Status{
				Root:       "/tmp/repo",
				Branch:     "main",
				RemoteName: "origin",
				RemoteURL:  "https://github.com/example/repo.git",
			},
			Fetched: true,
			Updated: true,
			Output:  "Updating abc..def",
			At:      now,
		},
	})
	router := systemUpdateTestRouter(handler)

	req := httptest.NewRequest(http.MethodPost, "/api/system/update", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload updater.Result
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if !payload.Fetched || !payload.Updated || payload.Output == "" {
		t.Fatalf("payload = %#v", payload)
	}
}

// systemUpdateTestRouter 封装当前模块的 systemUpdateTestRouter 逻辑。
func systemUpdateTestRouter(handler *SystemUpdateHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler.Register(router)
	return router
}
