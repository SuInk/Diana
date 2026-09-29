// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package netguard

import (
	"errors"
	"fmt"
	"io"
	"net/http"
)

// ErrResponseTooLarge 表示响应超过了调用方给的读取上限。
var ErrResponseTooLarge = errors.New("响应超过读取上限")

// ReadLimited 最多读取 limit 字节。超出时返回 ErrResponseTooLarge，而不是把截断的
// 半截数据交给调用方：截断的 JSON 解析时只会报 unexpected EOF，看不出是响应太大。
func ReadLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return data, err
	}
	if int64(len(data)) > limit {
		return data[:limit], fmt.Errorf("%w（%s）", ErrResponseTooLarge, formatByteLimit(limit))
	}
	return data, nil
}

// ReadResponseBody 按 ReadLimited 读取响应体，但只有 2xx 响应超限才报错：非 2xx 的
// 正文只拿来拼错误信息，截断无妨，不能让「响应过大」盖掉真正的 HTTP 状态错误。
func ReadResponseBody(resp *http.Response, limit int64) ([]byte, error) {
	data, err := ReadLimited(resp.Body, limit)
	if errors.Is(err, ErrResponseTooLarge) && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
		return data, nil
	}
	return data, err
}

func formatByteLimit(limit int64) string {
	switch {
	case limit >= 1<<20 && limit%(1<<20) == 0:
		return fmt.Sprintf("%d MiB", limit>>20)
	case limit >= 1<<10 && limit%(1<<10) == 0:
		return fmt.Sprintf("%d KiB", limit>>10)
	default:
		return fmt.Sprintf("%d 字节", limit)
	}
}
