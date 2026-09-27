// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"log"
	"time"
)

// botRuntimeStopper 是关机时需要的那一个方法，方便测试替换。
type botRuntimeStopper interface {
	Stop() error
}

// stopBotRuntime 停掉机器人运行时，并等入站队列、记忆任务的 worker 释放租约、
// 存好恢复检查点再返回（Stop 对两组 worker 各等最多 5 秒）。必须在关闭数据库之前
// 调用：worker 收尾时写的就是这个库。
func stopBotRuntime(runtime botRuntimeStopper) {
	if runtime == nil {
		return
	}
	started := time.Now()
	if err := runtime.Stop(); err != nil {
		log.Printf("assistant runtime stop: %v", err)
	}
	log.Printf("assistant runtime stopped in %s", time.Since(started).Round(time.Millisecond))
}
