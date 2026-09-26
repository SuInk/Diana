// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import "github.com/SuInk/diana/internal/safego"

// recover() 必须由这个被 defer 的函数直接调用，挪进 safego 里就接不住 panic。
func recoverGoroutinePanic(component string) { safego.Report("agent."+component, recover()) }
