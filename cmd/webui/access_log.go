// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// quietAccessLogPaths 是控制台定时轮询的只读接口。日志页、内置浏览器页开着就每
// 5 秒一次，线上 3 天光这几条就各记了三千多行、占了进程日志一半的字节，把真正
// 有用的请求和运行日志淹掉。它们成功时不记访问日志；失败、慢请求照常记。
var quietAccessLogPaths = map[string]bool{
	"/api/logs":                            true,
	"/api/health":                          true,
	"/api/stats":                           true,
	"/api/system/version":                  true,
	"/api/system/update":                   true,
	"/api/assistant/status":                true,
	"/api/assistant/plugins/vrchat/status": true,
	"/api/browser-source":                  true,
	"/api/browser-box/status":              true,
	"/api/browser-box/tabs":                true,
}

// quietAccessLogSlowThreshold 以上的轮询请求照样记下：轮询变慢本身就是要看的信号。
const quietAccessLogSlowThreshold = time.Second

// accessLogMaxQueryBytes 是访问日志里 query 保留的长度。日志页的请求带着整串筛选
// 条件，一行能有好几百字节；留个开头认得出是哪种请求就够了。
const accessLogMaxQueryBytes = 160

// webuiAccessLogger 是 WebUI 的访问日志中间件：格式和 gin 默认的一致（不带颜色码），
// 只是跳过成功的轮询请求、截断过长的 query。
func webuiAccessLogger(out io.Writer) gin.HandlerFunc {
	return gin.LoggerWithConfig(gin.LoggerConfig{
		Output:    out,
		Formatter: webuiAccessLogFormatter,
	})
}

// webuiAccessLogFormatter 返回空串表示这条不记（gin 会原样写出空串，等于什么都不写）。
func webuiAccessLogFormatter(param gin.LogFormatterParams) string {
	if quietAccessLogEntry(param) {
		return ""
	}
	path := truncateAccessLogQuery(param.Path)
	latency := param.Latency
	switch {
	case latency > time.Minute:
		latency = latency.Truncate(10 * time.Second)
	case latency > time.Second:
		latency = latency.Truncate(10 * time.Millisecond)
	case latency > time.Millisecond:
		latency = latency.Truncate(10 * time.Microsecond)
	}
	// 和 gin 默认格式去掉颜色码后逐字节一致。
	return fmt.Sprintf("[GIN] %v | %3d | %8v | %15s | %-7s  %#v\n%s",
		param.TimeStamp.Format("2006/01/02 - 15:04:05"),
		param.StatusCode,
		latency,
		param.ClientIP,
		param.Method,
		path,
		param.ErrorMessage,
	)
}

// quietAccessLogEntry 判断这条是不是可以不记的成功轮询。
func quietAccessLogEntry(param gin.LogFormatterParams) bool {
	if param.Method != http.MethodGet && param.Method != http.MethodHead {
		return false
	}
	if param.StatusCode != http.StatusNotModified && (param.StatusCode < 200 || param.StatusCode >= 300) {
		return false
	}
	if param.Latency >= quietAccessLogSlowThreshold || param.ErrorMessage != "" {
		return false
	}
	path := param.Path
	if param.Request != nil && param.Request.URL != nil {
		path = param.Request.URL.Path
	} else if index := strings.IndexByte(path, '?'); index >= 0 {
		path = path[:index]
	}
	return quietAccessLogPaths[path]
}

// truncateAccessLogQuery 把 path?query 里过长的 query 截短，并注明截掉了多少字节。
func truncateAccessLogQuery(path string) string {
	index := strings.IndexByte(path, '?')
	if index < 0 {
		return path
	}
	query := path[index+1:]
	if len(query) <= accessLogMaxQueryBytes {
		return path
	}
	cut := accessLogMaxQueryBytes
	// 按 UTF-8 字符边界截，别把一个汉字劈成两半。query 通常是百分号编码的纯 ASCII，
	// 这里只是防着客户端直接发了原始字节。
	for cut > 0 && query[cut]&0xC0 == 0x80 {
		cut--
	}
	return fmt.Sprintf("%s?%s…(+%d bytes)", path[:index], query[:cut], len(query)-cut)
}
