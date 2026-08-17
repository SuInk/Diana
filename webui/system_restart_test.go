package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func newRestartTestRouter(trigger func()) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	NewRestartHandler(trigger).Register(router)
	return router
}

// TestRestartRequiresConfirmation 验证缺少确认串时不触发重启。
func TestRestartRequiresConfirmation(t *testing.T) {
	triggered := make(chan struct{}, 1)
	router := newRestartTestRouter(func() { triggered <- struct{}{} })

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/system/restart", strings.NewReader(`{}`))
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	select {
	case <-triggered:
		t.Fatal("restart must not trigger without confirmation")
	case <-time.After(700 * time.Millisecond):
	}
}

// TestRestartTriggersAfterResponse 验证确认后触发重启回调。
func TestRestartTriggersAfterResponse(t *testing.T) {
	triggered := make(chan struct{}, 1)
	router := newRestartTestRouter(func() { triggered <- struct{}{} })

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/system/restart", strings.NewReader(`{"confirmation":"restart-service"}`))
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	select {
	case <-triggered:
	case <-time.After(3 * time.Second):
		t.Fatal("restart trigger was not called")
	}
}
