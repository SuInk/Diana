// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package safego

import (
	"runtime/debug"

	"github.com/SuInk/diana/internal/dlog"
)

// Recover is deferred at every non-request goroutine boundary. A panic still
// stops the faulty task, but it cannot terminate the whole Diana process.
func Recover(component string) {
	if recovered := recover(); recovered != nil {
		dlog.Error("goroutine panic recovered",
			"component", component,
			"panic", recovered,
			"stack", string(debug.Stack()))
	}
}
