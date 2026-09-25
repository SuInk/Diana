// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package xvfb

import "github.com/SuInk/diana/internal/safego"

// recoverGoroutinePanic 是本包后台协程的 panic 边界：等 Xvfb 退出、读显示号崩掉只该
// 停掉那一条，不该带走整个 Diana。
func recoverGoroutinePanic(component string) { safego.Recover("xvfb." + component) }
