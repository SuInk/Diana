package assistant

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func TestMediaPolicyConfigRoundTrip(t *testing.T) {
	disabled := false
	cfg := BotConfig{AutoImageDescription: &disabled, AutoVideoPreprocess: &disabled}
	restored := ConfigFromPayload(PayloadFromConfig(cfg), BotConfig{}).WithDefaults()
	if boolValue(restored.AutoImageDescription, true) || boolValue(restored.AutoVideoPreprocess, true) {
		t.Fatal("disabled media settings were lost")
	}
	unchanged := ConfigFromPayload(ConfigPayload{}, cfg)
	if boolValue(unchanged.AutoImageDescription, true) || boolValue(unchanged.AutoVideoPreprocess, true) {
		t.Fatal("older API client reset media policy")
	}
	if !boolValue(BotConfig{}.AutoImageDescription, true) || !boolValue(BotConfig{}.AutoVideoPreprocess, true) {
		t.Fatal("legacy behavior changed")
	}
}

func TestMediaDescriptionDisabledStillAllowsExplicitRead(t *testing.T) {
	disabled := false
	imagePath, hash := writeRecallImageFixture(t)
	store := newRecallImageTestStore()
	provider := &recallImageVisionProvider{}
	runtime := NewRuntime(BotConfig{AutoImageDescription: &disabled}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	runtime.SetMessageHistoryStore(store)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "group", UserID: "user", MessageID: "image", Time: time.Now().Unix(), Segments: []MessageSegment{{Type: "image", Data: map[string]string{"cached_file": imagePath, imageContentSHA256Key: hash}}}}
	for range 20 {
		runtime.enqueueHistoryImageDescriptions(event)
	}
	runtime.historyImageDescMu.Lock()
	pending := len(runtime.historyImageDescJobs)
	runtime.historyImageDescMu.Unlock()
	if pending != 0 || provider.callCount() != 0 {
		t.Fatal("automatic description ran while disabled")
	}
	runtime.enqueueHistoryImageDescriptionsNow(event)
	waitForCondition(t, 2*time.Second, func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return store.descriptions[hash].Description != ""
	})
	if provider.callCount() != 1 {
		t.Fatalf("explicit calls = %d", provider.callCount())
	}
}

func TestAutomaticVideoDisabledPreservesMediaIndex(t *testing.T) {
	disabled := false
	runtime := NewRuntime(BotConfig{AutoVideoPreprocess: &disabled}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Segments: []MessageSegment{{Type: "video", Data: map[string]string{"url": "https://example.com/video.mp4", "file_id": "original"}}}, Quoted: &QuotedMessage{Segments: []MessageSegment{{Type: "video", Data: map[string]string{"url": "https://example.com/quoted.mp4"}}}}}
	ctx := runtime.withAutomaticMediaPolicy(context.Background(), event)
	if got := cacheMessageEventVideos(ctx, event); !reflect.DeepEqual(got, event) {
		t.Fatal("passive ingestion changed the media index")
	}
	if skipAutomaticVideo(context.Background()) {
		t.Fatal("explicit read unexpectedly disabled")
	}
}

func TestMediaParserOverridesVisionFollowChat(t *testing.T) {
	roles := normalizeModelRoles(map[string]ModelRole{"chat": bindingRole("expensive"), "vision": {FollowChat: true}, PurposeMediaParse: bindingRole("cheap-vision")})
	for _, purpose := range []string{"image_description_cache", "sticker_description", "image_describe", "image_ocr"} {
		role, ok := modelRoleFor(roles, purpose, llm.GroupVision)
		if !ok || role.Model != "cheap-vision" {
			t.Fatalf("%s routed to %#v", purpose, role)
		}
	}
	role, _ := modelRoleFor(roles, PurposeReply, llm.GroupVision)
	if role.Model != "expensive" {
		t.Fatal("normal replies changed model")
	}
	delete(roles, PurposeMediaParse)
	role, _ = modelRoleFor(roles, "image_description_cache", llm.GroupVision)
	if role.Model != "expensive" {
		t.Fatal("unconfigured parser lost legacy fallback")
	}
}

func TestMediaVideoHistoryToolParsesOnDemandAndReusesFrames(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	path := filepath.Join(t.TempDir(), "video.mp4")
	if output, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=5:duration=1", "-pix_fmt", "yuv420p", path).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, output)
	}
	disabled := false
	runtime := NewRuntime(BotConfig{AutoVideoPreprocess: &disabled, AutoImageDescription: &disabled}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "group", UserID: "sender", MessageID: "video", Segments: []MessageSegment{{Type: "video", Data: map[string]string{"url": path}}}}
	passive := cacheMessageEventVideos(runtime.withAutomaticMediaPolicy(context.Background(), event), event)
	if len(passive.Segments) != 1 {
		t.Fatal("passive ingestion extracted frames")
	}
	runtime.remember(passive)
	tool := newDianaHistoryImagesTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "group", UserID: "reader"})
	output, err := tool.Run(context.Background(), map[string]any{"message_ids": []any{"video"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(tool.ToolResultParts(output)) == 0 {
		t.Fatal("explicit read returned no frames")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	output, err = tool.Run(context.Background(), map[string]any{"message_ids": []any{"video"}})
	if err != nil || len(tool.ToolResultParts(output)) == 0 {
		t.Fatalf("cached frames were not reusable: %v", err)
	}
}

func TestMediaParserFailureDoesNotUseChatProvider(t *testing.T) {
	store := &stubLLMProfileStore{set: llm.ProfileSet{Profiles: []llm.Profile{
		{ID: "chat", Group: llm.GroupChat, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: "expensive"}},
		{ID: "media", Group: llm.GroupVision, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: "cheap"}},
	}}}
	runtime := NewRuntime(BotConfig{ModelRoles: map[string]ModelRole{"chat": {ProfileID: "chat", Model: "expensive"}, "vision": {FollowChat: true}, PurposeMediaParse: {ProfileID: "media", Model: "cheap"}}}, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
	var used []string
	runtime.SetLLMProviderConfigFactory(func(cfg llm.ProviderConfig) (LLMProvider, error) {
		used = append(used, cfg.Model)
		return restoredModelProvider{err: fmt.Errorf("401 unauthorized")}, nil
	})
	path, _ := writeRecallImageFixture(t)
	_, err := runtime.describeRecallImage(context.Background(), MessageEvent{}, path)
	if err == nil || len(used) == 0 {
		t.Fatalf("missing failed media attempt: %v %v", err, used)
	}
	for _, model := range used {
		if model != "cheap" {
			t.Fatalf("unexpected costly fallback: %v", used)
		}
	}
}
