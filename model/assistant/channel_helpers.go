// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"crypto/aes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// 通道适配器共用的小工具：回调应答、AES 填充、事件去重。

// writeJSON 输出 JSON 响应。
func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

// stripPKCS7 去掉 PKCS#7 填充。
func stripPKCS7(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("assistant: 空的明文")
	}
	padding := int(data[len(data)-1])
	if padding <= 0 || padding > aes.BlockSize || padding > len(data) {
		return nil, fmt.Errorf("assistant: 填充长度非法")
	}
	// 逐字节核对填充，防止把正常数据当填充截掉。
	for _, b := range data[len(data)-padding:] {
		if int(b) != padding {
			return nil, fmt.Errorf("assistant: 填充内容非法")
		}
	}
	return data[:len(data)-padding], nil
}

// —— 事件去重 ——

// eventDeduper 记住最近处理过的事件 ID。
type eventDeduper struct {
	mu   sync.Mutex
	ttl  time.Duration
	seen map[string]time.Time
}

func newEventDeduper(ttl time.Duration) *eventDeduper {
	return &eventDeduper{ttl: ttl, seen: map[string]time.Time{}}
}

// Accept 返回这个事件是否是第一次见到。
func (d *eventDeduper) Accept(id string) bool {
	if d == nil || strings.TrimSpace(id) == "" {
		return true
	}
	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	if seenAt, ok := d.seen[id]; ok && now.Sub(seenAt) < d.ttl {
		return false
	}
	// 顺手清掉过期条目，避免长期运行后无限增长。
	for key, seenAt := range d.seen {
		if now.Sub(seenAt) >= d.ttl {
			delete(d.seen, key)
		}
	}
	d.seen[id] = now
	return true
}
