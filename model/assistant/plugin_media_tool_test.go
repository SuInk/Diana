// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

type mediaToolResolverFixture struct{ requests []PluginRequest }

func (p *mediaToolResolverFixture) Manifest() PluginManifest {
	return PluginManifest{ID: resolverPluginID, Name: "resolver", BuiltIn: true}
}
func (p *mediaToolResolverFixture) Handle(_ context.Context, req PluginRequest) (*PluginResponse, error) {
	p.requests = append(p.requests, req)
	return &PluginResponse{Handled: true, Context: "视频说明", Reply: "视频说明", VideoURLs: []string{"https://example.com/video.mp4"}}, nil
}

func mediaToolFixture(t *testing.T) (*dianaPluginMediaTool, *mediaToolResolverFixture, *recordingChannel) {
	t.Helper()
	p := &mediaToolResolverFixture{}
	channel := &recordingChannel{}
	r := NewRuntime(BotConfig{ID: "bot", Enabled: true, Platform: PlatformOneBotV11}, channel, NewPluginManager(p, NewFileParserPlugin(nil)), nil, nil, nil, nil)
	return &dianaPluginMediaTool{runtime: r, event: MessageEvent{ProfileID: "bot", Platform: PlatformOneBotV11, Kind: EventKindPrivate, UserID: "member", MessageID: "message"}}, p, channel
}

func TestPluginMediaResolveAndSend(t *testing.T) {
	tool, plugin, channel := mediaToolFixture(t)
	tool.event.RawMessage = "https://example.com/unrelated"
	result, err := tool.Run(context.Background(), map[string]any{"action": "resolve", "url": "https://example.com/requested"})
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		ID string `json:"media_id"`
	}
	if err := json.Unmarshal([]byte(result), &value); err != nil || value.ID == "" {
		t.Fatalf("result=%s err=%v", result, err)
	}
	if len(channel.sent) != 0 || len(plugin.requests) != 1 {
		t.Fatal("resolve must not send")
	}
	req := plugin.requests[0]
	if req.Event.UserID != "member" || req.Event.ProfileID != "bot" || req.Event.RawMessage != "" || req.Text != "https://example.com/requested" {
		t.Fatalf("request lost identity or included unrelated URL: %+v", req)
	}
	for range 2 {
		if _, err := tool.Run(context.Background(), map[string]any{"action": "send", "media_id": value.ID}); err != nil {
			t.Fatal(err)
		}
	}
	if len(channel.sent) != 1 || len(channel.sent[0].VideoURLs) != 1 {
		t.Fatalf("sent=%+v", channel.sent)
	}
}

func TestPluginMediaPermissionsAndInputs(t *testing.T) {
	tool, _, _ := mediaToolFixture(t)
	ctx := context.Background()
	for _, input := range []map[string]any{
		{"action": "resolve", "url": "/tmp/video.mp4"},
		{"action": "view_video", "url": "file:///tmp/video.mp4"},
		{"action": "view_video", "url": "https://user:secret@example.com/a.mp4"},
		{"action": "send", "media_id": "other-turn"},
		{"action": "view_video", "media_id": "other-turn"},
		{"action": "invalid"},
	} {
		tool.parts = []llm.ContentPart{{Type: llm.ContentPartText, Text: "stale"}}
		if _, err := tool.Run(ctx, input); err == nil {
			t.Fatalf("accepted invalid input: %v", input)
		}
		if len(tool.ToolResultParts("")) != 0 {
			t.Fatal("stale parts leaked")
		}
	}
	if _, err := tool.runtime.plugins.SetEnabledForProfile(resolverPluginID, "bot", false); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Run(ctx, map[string]any{"action": "resolve", "url": "https://example.com/video"}); err == nil {
		t.Fatal("disabled resolver was callable")
	}
	if _, err := tool.runtime.plugins.SetEnabledForProfile(fileParserPluginID, "bot", false); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Run(ctx, map[string]any{"action": "view_video", "url": "https://example.com/video.mp4"}); err == nil {
		t.Fatal("disabled parser was callable")
	}
	if !((RelationshipPolicy{}).allowedAgentToolNames()[dianaPluginMediaToolName]) {
		t.Fatal("members cannot discover media tool")
	}
}

func TestPluginMediaConcurrentSendDoesNotDuplicate(t *testing.T) {
	tool, _, channel := mediaToolFixture(t)
	tool.results = map[string]PluginResponse{"media_1": {Reply: "已解析的媒体"}}
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := tool.Run(context.Background(), map[string]any{"action": "send", "media_id": "media_1"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(channel.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(channel.sent))
	}
}

func TestPluginMediaVideoFramesReachModel(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	path := filepath.Join(t.TempDir(), "video.mp4")
	if output, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=64x64:rate=2:duration=2", "-pix_fmt", "yuv420p", path).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, output)
	}
	tool, _, _ := mediaToolFixture(t)
	tool.results = map[string]PluginResponse{"media_1": {VideoURLs: []string{path}}}
	result, err := tool.Run(context.Background(), map[string]any{"action": "view_video", "media_id": "media_1"})
	if err != nil {
		t.Fatal(err)
	}
	parts := tool.ToolResultParts(result)
	if len(parts) == 0 {
		t.Fatal("no video frames attached")
	}
	for _, part := range parts {
		if part.Type != llm.ContentPartImageURL || !strings.HasPrefix(part.ImageURL, "data:image/") {
			t.Fatalf("frame was not embedded: %+v", part)
		}
	}
}

func TestPluginMediaRegisteredForMembersAndPluginSwitches(t *testing.T) {
	tool, _, _ := mediaToolFixture(t)
	cfg := tool.runtime.effectiveConfigForEvent(tool.event).WithDefaults()
	cfg.AgentEnabled = true
	cfg.AgentMode = AgentModeSafe
	cfg.AgentSkillRoots = []string{t.TempDir()}
	cfg.AgentMCPConfigPath = filepath.Join(t.TempDir(), "missing.json")
	for _, enabled := range []bool{true, false} {
		for _, id := range []string{resolverPluginID, fileParserPluginID} {
			if _, err := tool.runtime.plugins.SetEnabledForProfile(id, "bot", enabled); err != nil {
				t.Fatal(err)
			}
		}
		registry, err := tool.runtime.newReplyAgentRegistry(context.Background(), cfg, tool.event, RelationshipPolicy{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, found := registry.Get(dianaPluginMediaToolName)
		_ = registry.Close()
		if found != enabled {
			t.Fatalf("enabled=%v registered=%v", enabled, found)
		}
	}
}
