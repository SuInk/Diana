// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ChannelBinding associates one transport with the persisted bot profile that
// owns it. Isolation only affects conversation keys; routing always uses the
// source profile so replies cannot cross platforms.
type ChannelBinding struct {
	ProfileID string
	Platform  string
	Name      string
	Channel   Channel
}

type MultiChannel struct {
	bindings []ChannelBinding
	isolate  bool
}

func NewMultiChannel(bindings []ChannelBinding, isolate ...bool) *MultiChannel {
	clean := make([]ChannelBinding, 0, len(bindings))
	for _, binding := range bindings {
		if binding.Channel == nil {
			continue
		}
		binding.ProfileID = strings.TrimSpace(binding.ProfileID)
		binding.Platform = NormalizePlatformID(binding.Platform)
		binding.Name = strings.TrimSpace(binding.Name)
		clean = append(clean, binding)
	}
	isolateContexts := true
	if len(isolate) > 0 {
		isolateContexts = isolate[0]
	}
	return &MultiChannel{bindings: clean, isolate: isolateContexts}
}

func (c *MultiChannel) Connect(ctx context.Context, handler EventHandler) error {
	if c == nil || len(c.bindings) == 0 {
		return fmt.Errorf("assistant: no enabled channels")
	}
	var wg sync.WaitGroup
	for _, binding := range c.bindings {
		binding := binding
		wg.Add(1)
		go func() {
			defer wg.Done()
			wrapped := func(eventCtx context.Context, event MessageEvent) error {
				event.Platform = binding.Platform
				event.ProfileID = binding.ProfileID
				if c.isolate && event.ContextNamespace == "" {
					event.ContextNamespace = binding.ProfileID
				}
				return handler(eventCtx, event)
			}
			_ = binding.Channel.Connect(ctx, wrapped)
		}()
	}
	<-ctx.Done()
	_ = c.Close()
	wg.Wait()
	return ctx.Err()
}

func (c *MultiChannel) Send(ctx context.Context, msg OutgoingMessage) error {
	_, err := c.SendWithResult(ctx, msg)
	return err
}

func (c *MultiChannel) SendWithResult(ctx context.Context, msg OutgoingMessage) (map[string]any, error) {
	binding, err := c.bindingFor(msg.ProfileID, msg.Platform)
	if err != nil {
		return nil, err
	}
	if channel, ok := binding.Channel.(ResultChannel); ok {
		return channel.SendWithResult(ctx, msg)
	}
	return nil, binding.Channel.Send(ctx, msg)
}

func (c *MultiChannel) CallAPI(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	for _, binding := range c.bindings {
		if IsOneBotPlatform(binding.Platform) {
			return binding.Channel.CallAPI(ctx, action, params)
		}
	}
	if len(c.bindings) == 1 {
		return c.bindings[0].Channel.CallAPI(ctx, action, params)
	}
	return nil, fmt.Errorf("assistant: API call %q has no routable OneBot channel", action)
}

func (c *MultiChannel) Status() ChannelStatus {
	statuses := c.ChannelStatuses()
	combined := ChannelStatus{Endpoint: fmt.Sprintf("%d enabled channels", len(statuses))}
	var errors []string
	for _, status := range statuses {
		if status.Connected {
			combined.Connected = true
			if combined.SelfID == "" {
				combined.SelfID = status.SelfID
			}
		}
		if status.UpdatedAt.After(combined.UpdatedAt) {
			combined.UpdatedAt = status.UpdatedAt
		}
		if status.LastError != "" {
			errors = append(errors, firstNonEmpty(status.Name, status.Platform, status.ProfileID)+": "+status.LastError)
		}
	}
	if combined.UpdatedAt.IsZero() {
		combined.UpdatedAt = time.Now()
	}
	combined.LastError = strings.Join(errors, "; ")
	return combined
}

func (c *MultiChannel) ChannelStatuses() []ChannelStatus {
	if c == nil {
		return nil
	}
	statuses := make([]ChannelStatus, 0, len(c.bindings))
	for _, binding := range c.bindings {
		status := binding.Channel.Status()
		status.ProfileID = binding.ProfileID
		status.Platform = binding.Platform
		status.Name = binding.Name
		statuses = append(statuses, status)
	}
	return statuses
}

func (c *MultiChannel) Close() error {
	if c == nil {
		return nil
	}
	var firstErr error
	for _, binding := range c.bindings {
		if err := binding.Channel.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (c *MultiChannel) bindingFor(profileID, platform string) (ChannelBinding, error) {
	profileID = strings.TrimSpace(profileID)
	platform = NormalizePlatformID(platform)
	if profileID != "" {
		for _, binding := range c.bindings {
			if binding.ProfileID == profileID {
				return binding, nil
			}
		}
	}
	if platform != "" {
		var match *ChannelBinding
		for index := range c.bindings {
			if c.bindings[index].Platform != platform {
				continue
			}
			if match != nil {
				return ChannelBinding{}, fmt.Errorf("assistant: platform %q matches multiple channels", platform)
			}
			match = &c.bindings[index]
		}
		if match != nil {
			return *match, nil
		}
	}
	if len(c.bindings) == 1 {
		return c.bindings[0], nil
	}
	return ChannelBinding{}, fmt.Errorf("assistant: no channel for profile %q platform %q", profileID, platform)
}
