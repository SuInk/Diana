package assistant

import (
	"strings"
	"testing"
	"time"
)

func TestChunkSendIntervalSimulatesTyping(t *testing.T) {
	off := BotConfig{SendChunkIntervalMS: 500}
	if got := chunkSendInterval(off, strings.Repeat("字", 40)); got != 500*time.Millisecond {
		t.Fatalf("typing delay off: got %v, want fixed interval", got)
	}
	on := BotConfig{SendChunkIntervalMS: 500, TypingDelayEnabled: boolPointer(true), TypingDelayPerCharMS: 100}
	for _, tc := range []struct {
		next string
		want time.Duration
	}{
		{"好", 500 * time.Millisecond},                            // 短消息不低于分段间隔
		{strings.Repeat("字", 20), 2 * time.Second},               // 按字数
		{"  " + strings.Repeat("字", 20) + "\n", 2 * time.Second}, // 首尾空白不算
		{strings.Repeat("字", 500), maxTypingDelay},               // 长消息封顶
	} {
		if got := chunkSendInterval(on, tc.next); got != tc.want {
			t.Fatalf("next=%d runes: got %v, want %v", len([]rune(tc.next)), got, tc.want)
		}
	}
	defaultSpeed := BotConfig{SendChunkIntervalMS: 1, TypingDelayEnabled: boolPointer(true)}
	if got := chunkSendInterval(defaultSpeed, strings.Repeat("字", 10)); got != 10*defaultTypingDelayPerCharMS*time.Millisecond {
		t.Fatalf("default speed: got %v", got)
	}
}

func TestTypingDelayConfigRoundTripAndGroupOverride(t *testing.T) {
	cfg := BotConfig{TypingDelayEnabled: boolPointer(true), TypingDelayPerCharMS: 5000}.WithDefaults()
	if cfg.TypingDelayPerCharMS != maxTypingDelayPerCharMS {
		t.Fatalf("per-char speed not clamped: %d", cfg.TypingDelayPerCharMS)
	}
	restored := ConfigFromPayload(PayloadFromConfig(cfg), BotConfig{})
	if !boolValue(restored.TypingDelayEnabled, false) || restored.TypingDelayPerCharMS != cfg.TypingDelayPerCharMS {
		t.Fatalf("payload round trip lost typing delay: %+v %d", restored.TypingDelayEnabled, restored.TypingDelayPerCharMS)
	}
	r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{
		"100": {GroupID: "100", Enabled: true, TypingDelayEnabled: boolPointer(false)},
	}})
	if boolValue(r.effectiveConfigForEvent(MessageEvent{Kind: EventKindGroup, GroupID: "100"}).TypingDelayEnabled, false) {
		t.Fatal("group override did not disable typing delay")
	}
}
