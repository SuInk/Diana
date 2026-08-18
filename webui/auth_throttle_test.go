// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestAuthThrottleBacksOffAfterFreeAttempts(t *testing.T) {
	throttle := newAuthThrottle()
	now := time.Now()

	for i := 0; i < authThrottleFreeAttempts; i++ {
		if lock := throttle.Fail("1.2.3.4", now); lock != 0 {
			t.Fatalf("attempt %d locked too early: %v", i+1, lock)
		}
		if wait := throttle.RetryAfter("1.2.3.4", now); wait != 0 {
			t.Fatalf("attempt %d should not block: %v", i+1, wait)
		}
	}

	first := throttle.Fail("1.2.3.4", now)
	if first != authThrottleBaseDelay {
		t.Fatalf("first lock = %v, want %v", first, authThrottleBaseDelay)
	}
	if wait := throttle.RetryAfter("1.2.3.4", now); wait <= 0 {
		t.Fatal("locked source was not blocked")
	}
	// 别的来源不受连坐。
	if wait := throttle.RetryAfter("5.6.7.8", now); wait != 0 {
		t.Fatalf("unrelated source blocked: %v", wait)
	}
	// 锁定期过了就放行。
	if wait := throttle.RetryAfter("1.2.3.4", now.Add(first+time.Second)); wait != 0 {
		t.Fatalf("still blocked after the lock expired: %v", wait)
	}

	second := throttle.Fail("1.2.3.4", now.Add(first+time.Second))
	if second != first*2 {
		t.Fatalf("second lock = %v, want %v", second, first*2)
	}
}

func TestAuthThrottleLockIsCapped(t *testing.T) {
	throttle := newAuthThrottle()
	now := time.Now()
	var last time.Duration
	for i := 0; i < 40; i++ {
		last = throttle.Fail("1.2.3.4", now)
		now = now.Add(last + time.Second)
	}
	if last != authThrottleMaxDelay {
		t.Fatalf("lock = %v, want cap %v", last, authThrottleMaxDelay)
	}
}

func TestAuthThrottleResetClearsCount(t *testing.T) {
	throttle := newAuthThrottle()
	now := time.Now()
	for i := 0; i <= authThrottleFreeAttempts; i++ {
		throttle.Fail("1.2.3.4", now)
	}
	throttle.Reset("1.2.3.4")
	if wait := throttle.RetryAfter("1.2.3.4", now); wait != 0 {
		t.Fatalf("reset did not clear the lock: %v", wait)
	}
	if lock := throttle.Fail("1.2.3.4", now); lock != 0 {
		t.Fatalf("counter survived reset: %v", lock)
	}
}

// 换 IP 也躲不过全局兜底。
func TestAuthThrottleGlobalCeiling(t *testing.T) {
	throttle := newAuthThrottle()
	now := time.Now()
	for i := 0; i < authThrottleGlobalLimit; i++ {
		throttle.Fail(string(rune('a'+i%26))+string(rune('a'+i/26)), now)
	}
	if wait := throttle.RetryAfter("brand-new-source", now); wait <= 0 {
		t.Fatal("global ceiling did not engage")
	}
	if wait := throttle.RetryAfter("brand-new-source", now.Add(authThrottleGlobalCooldown+time.Second)); wait != 0 {
		t.Fatalf("global cooldown never lifted: %v", wait)
	}
}

func TestAuthThrottleDoesNotGrowUnbounded(t *testing.T) {
	throttle := newAuthThrottle()
	now := time.Now()
	for i := 0; i < authThrottleMaxTracked+200; i++ {
		throttle.Fail(string(rune(i)), now)
	}
	throttle.mu.Lock()
	tracked := len(throttle.entries)
	throttle.mu.Unlock()
	if tracked > authThrottleMaxTracked {
		t.Fatalf("tracked %d sources, want at most %d", tracked, authThrottleMaxTracked)
	}
}

// 登录接口连续失败后应当返回 429 且带 Retry-After。
func TestLoginThrottledAfterRepeatedFailures(t *testing.T) {
	router, manager, _ := newAuthTestRouter(t)
	if err := manager.SetPassword("", "console-pass-1"); err != nil {
		t.Fatalf("SetPassword() error = %v", err)
	}

	var last *httptest.ResponseRecorder
	for i := 0; i <= authThrottleFreeAttempts; i++ {
		last = postLogin(router, "wrong-password")
		if last.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d: %s", i+1, last.Code, last.Body.String())
		}
	}

	last = postLogin(router, "wrong-password")
	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("throttled attempt = %d: %s", last.Code, last.Body.String())
	}
	if last.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After header")
	}
	// 退避期内即使密码正确也一律回绝，否则限流可以被正确密码绕开。
	if rec := postLogin(router, "console-pass-1"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("correct password bypassed the lock = %d: %s", rec.Code, rec.Body.String())
	}
}

func postLogin(router *gin.Engine, password string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login",
		strings.NewReader(`{"username":"","password":"`+password+`"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	return rec
}
