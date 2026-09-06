package assistant

import (
	"fmt"
	"strings"
)

// Resolve capabilities from the actual target binding/transport, not just the
// Runtime's default profile (which may belong to another platform).
func (r *Runtime) outboundPlatformForEvent(event MessageEvent) (string, error) {
	_, platform, err := r.outboundChannelForEvent(event)
	return platform, err
}

func (r *Runtime) outboundChannelForEvent(event MessageEvent) (Channel, string, error) {
	if r == nil {
		return nil, "", fmt.Errorf("diana: runtime is not configured")
	}
	r.mu.RLock()
	channel := r.channel
	r.mu.RUnlock()
	if channel == nil {
		return nil, "", fmt.Errorf("diana: channel is not configured")
	}
	platform := ""
	target := channel
	if multi, ok := channel.(*MultiChannel); ok {
		binding, err := multi.bindingFor(event.ProfileID, event.Platform)
		if err != nil {
			return nil, "", err
		}
		platform = binding.Platform
		target = binding.Channel
	}
	actual := target.Status().Platform
	if _, ok := target.(*TelegramChannel); ok {
		actual = PlatformTelegram
	}
	if actual != "" {
		if platform != "" && NormalizePlatformID(platform) != NormalizePlatformID(actual) {
			return nil, "", fmt.Errorf("diana: channel binding does not match outbound transport")
		}
		platform = actual
	}
	if platform != "" && strings.TrimSpace(event.Platform) != "" && NormalizePlatformID(platform) != NormalizePlatformID(event.Platform) {
		return nil, "", fmt.Errorf("diana: event platform does not match outbound transport")
	}
	if platform == "" {
		platform = firstNonEmpty(event.Platform, r.effectiveConfigForEvent(event).Platform)
	}
	return target, NormalizePlatformID(platform), nil
}
