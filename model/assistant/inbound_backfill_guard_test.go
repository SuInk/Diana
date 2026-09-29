package assistant

import (
	"testing"
	"time"
)

func TestHistoryRetryBackoffDoublesThenGivesUp(t *testing.T) {
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute}
	for i, expected := range want {
		delay, retry := historyRetryBackoff(i + 1)
		if !retry || delay != expected {
			t.Fatalf("failure %d: delay=%s retry=%v, want %s", i+1, delay, retry, expected)
		}
	}
	if _, retry := historyRetryBackoff(historyRetryMaxAttempts); retry {
		t.Fatalf("still retrying after %d failures", historyRetryMaxAttempts)
	}
	if delay, _ := historyRetryBackoff(1); delay > historyRetryDelayMax {
		t.Fatalf("delay %s exceeds cap", delay)
	}
}

func TestHasOneBotProfileIgnoresOtherPlatforms(t *testing.T) {
	oneBot := NewRuntime(BotConfig{Platform: PlatformOneBotV11}, newQueueTestChannel(), NewPluginManager(), nil, nil, nil, nil)
	if !oneBot.hasOneBotProfile() {
		t.Fatal("OneBot runtime reported no OneBot profile")
	}
	qq := NewRuntime(BotConfig{Platform: PlatformQQOfficial}, newQueueTestChannel(), NewPluginManager(), nil, nil, nil, nil)
	if qq.hasOneBotProfile() {
		t.Fatal("QQ official-only runtime reported a OneBot profile")
	}
}
