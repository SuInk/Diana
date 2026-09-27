// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func newAccessLogTestRouter(out *bytes.Buffer) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(webuiAccessLogger(out))
	router.GET("/api/logs", func(c *gin.Context) {
		if c.Query("fail") != "" {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "boom"})
			return
		}
		c.JSON(http.StatusOK, gin.H{})
	})
	router.GET("/api/browser-box/status", func(c *gin.Context) { c.Status(http.StatusNotModified) })
	router.POST("/api/system/update", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{}) })
	router.GET("/api/assistant/config", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{}) })
	return router
}

func serveAccessLogTest(router *gin.Engine, method, target string) {
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, target, nil))
}

func TestWebUIAccessLogSkipsSuccessfulPolling(t *testing.T) {
	var out bytes.Buffer
	router := newAccessLogTestRouter(&out)
	serveAccessLogTest(router, http.MethodGet, "/api/logs?kind=all&limit=100")
	serveAccessLogTest(router, http.MethodGet, "/api/browser-box/status?bot=b1")
	if out.Len() != 0 {
		t.Fatalf("successful polling logged:\n%s", out.String())
	}

	// 失败的轮询、写操作和普通请求照常记。
	serveAccessLogTest(router, http.MethodGet, "/api/logs?fail=1")
	serveAccessLogTest(router, http.MethodPost, "/api/system/update")
	serveAccessLogTest(router, http.MethodGet, "/api/assistant/config")
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("logged %d lines, want 3:\n%s", len(lines), out.String())
	}
	for index, want := range []string{`| 500 |`, `| POST     "/api/system/update"`, `| GET      "/api/assistant/config"`} {
		if !strings.HasPrefix(lines[index], "[GIN] ") || !strings.Contains(lines[index], want) {
			t.Fatalf("line %d = %q, want it to contain %q", index, lines[index], want)
		}
	}
}

func TestWebUIAccessLogKeepsSlowPolling(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/logs?kind=all", nil)
	param := gin.LogFormatterParams{Request: request, Method: http.MethodGet, StatusCode: http.StatusOK, Path: "/api/logs?kind=all", TimeStamp: time.Now()}
	param.Latency = 200 * time.Millisecond
	if got := webuiAccessLogFormatter(param); got != "" {
		t.Fatalf("fast polling logged: %q", got)
	}
	param.Latency = 2 * time.Second
	if got := webuiAccessLogFormatter(param); !strings.Contains(got, `"/api/logs?kind=all"`) {
		t.Fatalf("slow polling not logged: %q", got)
	}
}

func TestWebUIAccessLogTruncatesLongQuery(t *testing.T) {
	var out bytes.Buffer
	router := newAccessLogTestRouter(&out)
	query := "q=" + strings.Repeat("a", 400)
	serveAccessLogTest(router, http.MethodGet, "/api/assistant/config?"+query)
	line := out.String()
	kept := query[:accessLogMaxQueryBytes]
	if !strings.Contains(line, `"/api/assistant/config?`+kept+`…(+242 bytes)"`) || strings.Contains(line, query) {
		t.Fatalf("query not truncated: %q", line)
	}
	if got := truncateAccessLogQuery("/api/assistant/config?q=short"); got != "/api/assistant/config?q=short" {
		t.Fatalf("short query changed: %q", got)
	}
	// 截断落在多字节字符中间时退回字符边界。
	multibyte := "/x?" + strings.Repeat("a", accessLogMaxQueryBytes-1) + "中文"
	if got := truncateAccessLogQuery(multibyte); !strings.HasSuffix(got, strings.Repeat("a", accessLogMaxQueryBytes-1)+"…(+6 bytes)") {
		t.Fatalf("multibyte truncation = %q", got)
	}
}
