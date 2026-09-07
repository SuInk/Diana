// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package safego

import (
	"log"
	"runtime/debug"
)

// Recover is deferred at every non-request goroutine boundary. A panic still
// stops the faulty task, but it cannot terminate the whole Diana process.
func Recover(component string) {
	if recovered := recover(); recovered != nil {
		log.Printf("diana goroutine panic recovered: component=%s panic=%v\n%s", component, recovered, debug.Stack())
	}
}
