// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

// soulSyncInterval 是人设文件的轮询间隔。
//
// 轮询而不是 inotify：目录可能在网络盘、容器挂载卷或 Windows 上，文件事件的可靠性
// 各不相同，而这件事本来就不急——改完人设过半分钟生效完全够用，漏掉一次事件却会
// 让人以为文件同步坏了。每轮读几个小文件、比几个哈希，两边都没变就一个字节都不写。
const soulSyncInterval = 30 * time.Second

// startSoulFileSync 先同步一次，再按间隔盯着目录。返回的 stop 会等 worker 退出。
//
// dir 为空表示没启用（config.yaml 的 storage.souls_dir 留空），此时一个文件都不碰，
// 连目录都不建：人设仍然只在 WebUI 和数据库里。
func startSoulFileSync(parent context.Context, runtime *assistant.Runtime, dir string) func() {
	dir = strings.TrimSpace(dir)
	if dir == "" || runtime == nil {
		return func() {}
	}
	logSoulSync(runtime, dir)
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer recoverGoroutinePanic("soul_files.go:38")
		defer close(done)
		ticker := time.NewTicker(soulSyncInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			logSoulSync(runtime, dir)
		}
	}()
	return func() { cancel(); <-done }
}

// logSoulSync 同步一次并把有动作的结果写进日志。没动作的不打，免得每半分钟刷一屏。
func logSoulSync(runtime *assistant.Runtime, dir string) {
	results, err := runtime.SyncSoulFiles(dir)
	if err != nil {
		log.Printf("soul files: sync %s failed: %v", dir, err)
		return
	}
	for _, result := range results {
		switch result.Action {
		case assistant.SoulSyncUnchanged:
			if strings.TrimSpace(result.Message) != "" {
				log.Printf("soul files: %s (%s): %s", result.ProfileID, result.Path, result.Message)
			}
		case assistant.SoulSyncConflict:
			log.Printf("soul files: %s conflict, kept the database persona; your file was saved to %s", result.ProfileID, result.ConflictPath)
		default:
			log.Printf("soul files: %s %s (%s)", result.ProfileID, result.Action, result.Path)
		}
	}
}
