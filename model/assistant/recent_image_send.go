// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"time"
)

// recentImageSendWindow 内同一会话再发同一张图就不发了。
//
// 群里同时可以跑几轮回复（InboundGroupConcurrency）。两个人前后脚要同一张图时，
// 两轮各自查到、各自发出：第二轮决定发图时第一轮的图还没到群里，历史里看不见，
// 只能在发送这一步拦（2026-11-11 两人先后要同一个 GitHub 头像，4 秒内发了两遍）。
const recentImageSendWindow = 3 * time.Minute

// recentImageSend 是一张图在某会话里最近一次发出（或另一轮正在发）的时间。发送中的也算：
// 另一轮正在发同一张图时这边同样不发，发失败会把占位撤掉。
type recentImageSend struct {
	at time.Time
}

// recentImageFingerprint 按图片内容算指纹：data URI 解码后算，同一张图换了 MIME
// 写法也认得出；其他地址按原文算。
func recentImageFingerprint(image string) string {
	image = strings.TrimSpace(image)
	if image == "" {
		return ""
	}
	payload := []byte(image)
	if header, encoded, ok := strings.Cut(image, ","); ok && strings.HasPrefix(strings.ToLower(header), "data:") && strings.HasSuffix(strings.ToLower(header), ";base64") {
		if data, err := base64.StdEncoding.DecodeString(encoded); err == nil && len(data) > 0 {
			payload = data
		}
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// reserveImageSend 在发图前占住这组图。这组图在本会话的窗口内都已经发过（或另一轮正在发）
// 时返回 since>0、ok=false，调用方不再发；否则占住并返回 finish，发送结束后传入是否成功。
func (r *Runtime) reserveImageSend(event MessageEvent, images []string) (finish func(sent bool), since time.Duration, ok bool) {
	session := sessionSendLogKey(event)
	keys := make([]string, 0, len(images))
	for _, image := range images {
		if fingerprint := recentImageFingerprint(image); fingerprint != "" {
			keys = append(keys, session+"\x00"+fingerprint)
		}
	}
	if len(keys) == 0 {
		return func(bool) {}, 0, true
	}
	now := r.clock()
	r.recentImageSendMu.Lock()
	defer r.recentImageSendMu.Unlock()
	if r.recentImageSends == nil {
		r.recentImageSends = map[string]recentImageSend{}
	}
	for key, record := range r.recentImageSends {
		if now.Sub(record.at) > recentImageSendWindow {
			delete(r.recentImageSends, key)
		}
	}
	allSent := true
	var latest time.Time
	for _, key := range keys {
		record, found := r.recentImageSends[key]
		if !found {
			allSent = false
			break
		}
		if record.at.After(latest) {
			latest = record.at
		}
	}
	if allSent {
		return nil, max(now.Sub(latest), time.Second), false
	}
	previous := make(map[string]recentImageSend, len(keys))
	for _, key := range keys {
		if record, found := r.recentImageSends[key]; found {
			previous[key] = record
		}
		r.recentImageSends[key] = recentImageSend{at: now}
	}
	return func(sent bool) {
		r.recentImageSendMu.Lock()
		defer r.recentImageSendMu.Unlock()
		for _, key := range keys {
			if sent {
				r.recentImageSends[key] = recentImageSend{at: r.clock()}
				continue
			}
			if record, found := previous[key]; found {
				r.recentImageSends[key] = record
			} else {
				delete(r.recentImageSends, key)
			}
		}
	}, 0, true
}
