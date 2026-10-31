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

// 保存并重新读取后，媒体解析的独立绑定和跟随状态不能改写视觉理解。
func TestMediaParserConfigRoundTripKeepsIndependentRoles(t *testing.T) {
	independent := bindingRole("cheap-vision")
	for _, tc := range []struct {
		name  string
		media *ModelRole
		want  string
	}{
		{"independent", &independent, "cheap-vision"},
		{"follow_chat", &ModelRole{FollowChat: true}, "expensive"},
		{"follow_vision", nil, "strong-vision"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vision := bindingRole("strong-vision")
			cfg := BotConfig{ModelRoles: map[string]ModelRole{"chat": bindingRole("expensive"), "vision": vision}}
			if tc.media != nil {
				cfg.ModelRoles[PurposeMediaParse] = *tc.media
			}
			cfg = cfg.WithDefaults()
			// 覆盖 API payload、保存规范化与再次加载的链路。
			for range 2 {
				cfg = ConfigFromPayload(PayloadFromConfig(cfg), BotConfig{}).WithDefaults()
			}
			roles := cfg.ModelRoles
			if got := roles["vision"]; got.Model != vision.Model || got.ProfileID != vision.ProfileID || got.FollowChat {
				t.Fatalf("vision binding changed: %#v", got)
			}
			media, exists := roles[PurposeMediaParse]
			if exists != (tc.media != nil) {
				t.Fatalf("media binding presence = %v", exists)
			}
			if tc.media != nil && (media.FollowChat != tc.media.FollowChat || media.Model != tc.media.Model) {
				t.Fatalf("media binding changed: %#v", media)
			}
			for _, purpose := range []string{PurposeMediaParse, "image_description_cache", "sticker_description", "image_describe", "image_ocr", imageQuestionPurpose} {
				role, ok := modelRoleFor(roles, purpose, llm.GroupVision)
				if !ok || role.Model != tc.want {
					t.Fatalf("%s routed to %#v, want %s", purpose, role, tc.want)
				}
			}
			if role, _ := modelRoleFor(roles, PurposeReply, llm.GroupChat); role.Model != "expensive" {
				t.Fatal("normal replies changed model")
			}
			if role, _ := modelRoleFor(roles, "", llm.GroupVision); role.Model != "strong-vision" {
				t.Fatal("general vision changed model")
			}
		})
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

func TestMediaParserRegistryBindingDoesNotReplaceVision(t *testing.T) {
	registry, err := llm.RegistryFromDocument(llm.ProviderRegistryDocument{
		Version:   1,
		Providers: []llm.ProviderDefinition{{ID: "p", Name: "Provider", Protocol: llm.ProtocolOpenAIResponses, BaseURL: "https://example.invalid/v1", APIKey: "test", Enabled: true}},
		Models: []llm.ModelDefinition{
			{ID: "p:chat", ProviderID: "p", ModelID: "chat", Name: "chat"},
			{ID: "p:vision", ProviderID: "p", ModelID: "vision", Name: "vision"},
			{ID: "p:media", ProviderID: "p", ModelID: "media", Name: "media"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, follow := range []bool{false, true} {
		roles := map[string]ModelRole{
			"chat":            {ProviderID: "p", ModelID: "chat"},
			"vision":          {ProviderID: "p", ModelID: "vision"},
			PurposeMediaParse: {ProviderID: "p", ModelID: "media"},
		}
		wantMedia := "p:media"
		if follow {
			roles[PurposeMediaParse] = ModelRole{FollowChat: true}
			wantMedia = "p:chat"
		}
		roles = normalizeModelRoles(roles)
		for _, tc := range []struct{ purpose, want string }{{"image_description_cache", wantMedia}, {"", "p:vision"}} {
			selection, ok, err := registrySelectionForGroup(registry, llm.ProfileSet{}, roles, tc.purpose, llm.GroupVision, "")
			if err != nil || !ok || selection.ProviderID != "p" || selection.ModelID != tc.want {
				t.Fatalf("follow=%v purpose=%q: selection=%#v ok=%v err=%v", follow, tc.purpose, selection, ok, err)
			}
		}
	}
}
