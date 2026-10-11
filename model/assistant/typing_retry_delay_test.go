package assistant

import (
	"testing"
	"time"
)

func TestTypingRetryDelay(t *testing.T) {
	for _, tc := range []struct {
		name        string
		interval    time.Duration
		maxFailures int
		result      typingRefreshResult
		want        time.Duration
	}{
		{"qq_success", 500 * time.Millisecond, 3, typingRefreshOK, 500 * time.Millisecond},
		{"qq_failure", 500 * time.Millisecond, 3, typingRefreshFailed, 2500 * time.Millisecond},
		{"telegram_failure", 4 * time.Second, 0, typingRefreshFailed, 4 * time.Second},
		{"longer_interval", 4 * time.Second, 3, typingRefreshFailed, 4 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			indicator := &typingIndicator{interval: tc.interval, maxFailures: tc.maxFailures, retryInterval: 2500 * time.Millisecond}
			if got := indicator.retryDelay(tc.result); got != tc.want {
				t.Fatalf("retryDelay() = %v, want %v", got, tc.want)
			}
		})
	}
}
