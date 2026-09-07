package assistant

import (
	"strings"
	"testing"
)

func TestForwardThresholdsDefaultToUnlimitedAndCanBeCleared(t *testing.T) {
	cfg := BotConfig{}.WithDefaults()
	if cfg.ForwardReplyThreshold != 0 || cfg.ForwardReplyChunkThreshold != 0 {
		t.Fatalf("unexpected defaults: %d, %d", cfg.ForwardReplyThreshold, cfg.ForwardReplyChunkThreshold)
	}
	previous := BotConfig{ForwardReplyThreshold: 300, ForwardReplyChunkThreshold: 4}.WithDefaults()
	payload := PayloadFromConfig(previous)
	payload.ForwardReplyThreshold = 0
	payload.ForwardReplyChunkThreshold = 0
	cleared := ConfigFromPayload(payload, previous).WithDefaults()
	group := (GroupConfig{}).WithDefaults("123", previous)
	if cleared.ForwardReplyThreshold != 0 || cleared.ForwardReplyChunkThreshold != 0 ||
		group.ForwardReplyThreshold != 0 || group.ForwardReplyChunkThreshold != 0 {
		t.Fatal("cleared thresholds must not restore defaults or inherit bot thresholds")
	}
}

func TestUnlimitedChatIgnoresLegacyLengthAndBubbleSettings(t *testing.T) {
	cfg := BotConfig{ReplyMaxBubbles: 2, DirectReplyChunkSize: 10}.WithDefaults()
	long := strings.Repeat("字", 5000)
	reply := strings.Repeat("一句回应"+notificationSplitMarker, 10) + long
	limits := chatSplitLimitsFrom(cfg)
	for name, split := range map[string]func(string, chatSplitLimits) []string{
		"chat": splitChatReply, "forward": splitForwardReply,
	} {
		t.Run(name, func(t *testing.T) {
			chunks := split(reply, limits)
			if len(chunks) != 11 || chunks[10] != long {
				t.Fatalf("legacy limits altered reply: chunks=%d", len(chunks))
			}
		})
	}
	if shouldUseForwardReply(reply, splitChatReply(reply, limits), 0, 0) {
		t.Fatal("unset thresholds triggered forwarding")
	}
	code := "```text" + notificationLineMarker + long + notificationLineMarker + "```"
	if got := splitChatReply(code, limits); len(got) != 1 || got[0] != "```text\n"+long+"\n```" {
		t.Fatal("unlimited fenced code block was split or altered")
	}
}

func TestForwardThresholdBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name           string
		length, chunks int
		want           bool
	}{
		{"unlimited", 0, 0, false},
		{"negative", -1, -1, false},
		{"exact length", 6, 0, false},
		{"over length", 5, 0, true},
		{"exact chunks", 0, 3, false},
		{"over chunks", 0, 2, true},
		{"length independently triggers", 5, 9, true},
		{"chunks independently trigger", 99, 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldUseForwardReply("一二三四五六", []string{"一二", "三四", "五六"}, tc.length, tc.chunks); got != tc.want {
				t.Fatalf("forward=%v, want %v", got, tc.want)
			}
		})
	}
}
