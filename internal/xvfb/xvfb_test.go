// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package xvfb

import (
	"testing"
	"time"
)

func TestSharedDisplayFailureIsCachedUntilNextLaunchAfterInterval(t *testing.T) {
	shared := &Shared{Width: 320, Height: 240, RetryInterval: time.Hour}
	shared.lastFailure = time.Now()
	if env, ok := shared.Env(); ok || env != nil {
		t.Fatalf("fresh display failure was retried immediately: env=%v ok=%v", env, ok)
	}

	shared.mu.Lock()
	shared.lastFailure = time.Now().Add(-2 * time.Hour)
	shared.mu.Unlock()
	if SystemDisplayAvailable() || Available() {
		if _, ok := shared.Env(); !ok {
			t.Fatal("expired display failure was not retried by the next launch")
		}
	}
	if shared.display != nil {
		shared.display.Stop()
	}
}
