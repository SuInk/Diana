// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"fmt"
	"log"
	"runtime/debug"
)

// recoverChatStreamPanic keeps malformed provider stream data or an SDK bug
// inside the current request. It must be deferred by every goroutine that
// consumes a provider stream before that goroutine starts reading events.
func recoverChatStreamPanic(ctx context.Context, out chan<- ChatEvent, provider string) {
	recovered := recover()
	if recovered == nil {
		return
	}
	err := fmt.Errorf("llm: %s stream panicked: %v", provider, recovered)
	log.Printf("%v\n%s", err, debug.Stack())
	select {
	case out <- ChatEvent{Type: ChatEventError, Error: err.Error()}:
	case <-ctx.Done():
	}
}

func recoverBackgroundLLMPanic(done chan<- error, operation string) {
	recovered := recover()
	if recovered == nil {
		return
	}
	err := fmt.Errorf("llm: %s panicked: %v", operation, recovered)
	log.Printf("%v\n%s", err, debug.Stack())
	done <- err
}
