// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package safego

import (
	"runtime/debug"

	"github.com/SuInk/diana/internal/dlog"
)

// Recover is deferred at every non-request goroutine boundary. A panic still
// stops the faulty task, but it cannot terminate the whole Diana process.
//
// recover() only stops a panic when the deferred function calls it directly,
// so Recover works only as `defer safego.Recover("x")`. A package wrapper that
// calls Recover is one frame too deep and recovers nothing; wrappers must call
// recover() themselves and hand the value to Report.
func Recover(component string) {
	Report(component, recover())
}

// Report logs a value already obtained from recover() by the deferred
// function. A nil value means there was no panic and is ignored.
func Report(component string, recovered any) {
	if recovered == nil {
		return
	}
	dlog.Error("goroutine panic recovered",
		"component", component,
		"panic", recovered,
		"stack", string(debug.Stack()))
}
