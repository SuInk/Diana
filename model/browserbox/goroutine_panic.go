// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package browserbox

import "github.com/SuInk/diana/internal/safego"

// recoverGoroutinePanic 是本包所有后台协程的 panic 边界：浏览器进程、画面推送
// 和 CDP 读循环崩掉只该停掉那一条，不该带走整个 Diana。
func recoverGoroutinePanic(component string) { safego.Recover("browserbox." + component) }
