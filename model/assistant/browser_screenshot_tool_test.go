// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

type screenshotBrowserStub struct{ data []byte }

func (s screenshotBrowserStub) Render(_ context.Context, rawURL string) (agent.RenderedPage, error) {
	return agent.RenderedPage{URL: rawURL, Sandboxed: true}, nil
}
func (s screenshotBrowserStub) Screenshot(_ context.Context, rawURL string) (agent.PageScreenshot, error) {
	if rawURL != "https://example.com" {
		return agent.PageScreenshot{}, fmt.Errorf("test capture failure")
	}
	return agent.PageScreenshot{URL: rawURL, Title: "Example", Stable: true, PNG: s.data}, nil
}

func assistantScreenshotPNG(t *testing.T) []byte {
	t.Helper()
	var output bytes.Buffer
	if err := png.Encode(&output, image.NewRGBA(image.Rect(0, 0, 20, 20))); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestScreenshotViewAndTelegramDeliveryStayInCurrentReply(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	data := assistantScreenshotPNG(t)
	var uploaded []byte
	var chatID, threadID string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			http.Error(w, "invalid", 400)
			return
		}
		defer r.MultipartForm.RemoveAll()
		chatID, threadID = r.FormValue("chat_id"), r.FormValue("message_thread_id")
		file, _, err := r.FormFile("photo")
		if err != nil {
			t.Error(err)
			http.Error(w, "no photo", 400)
			return
		}
		defer file.Close()
		uploaded, _ = io.ReadAll(file)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":42}}`))
	}))
	defer api.Close()
	r := NewRuntime(BotConfig{Platform: PlatformTelegram}, NewTelegramChannel(TelegramConfig{BotToken: "test", APIBaseURL: api.URL}), NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Platform: PlatformTelegram, Kind: EventKindGroup, GroupID: "-100123", MessageThreadID: "77", UserID: "123"}
	newTool := func(event MessageEvent) agent.ToolResultPartsTool {
		registry := agent.NewToolRegistry(agent.NewWebpageScreenshotTool(screenshotBrowserStub{data: data}))
		r.wrapScreenshotTools(registry, event)
		tool, _ := registry.Get("webpage_screenshot")
		return tool.(agent.ToolResultPartsTool)
	}
	tool := newTool(event)
	ctx := context.Background()
	if _, err := tool.Run(ctx, map[string]any{"action": "send", "image_id": "photo.png"}); err == nil {
		t.Fatal("arbitrary local file accepted")
	}
	output, err := tool.Run(ctx, map[string]any{"url": "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		ImageID string `json:"image_id"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil || result.ImageID == "" {
		t.Fatalf("no image ID: %s %v", output, err)
	}
	if len(tool.ToolResultParts(output)) != 1 {
		t.Fatal("missing visual evidence")
	}
	other := newTool(MessageEvent{Platform: PlatformTelegram, Kind: EventKindPrivate, UserID: "other"})
	if _, err := other.Run(ctx, map[string]any{"action": "send", "image_id": result.ImageID}); err == nil {
		t.Fatal("another conversation reused screenshot ID")
	}
	// Even a fresh reply in the same conversation cannot reuse the ID.
	if _, err := newTool(event).Run(ctx, map[string]any{"action": "send", "image_id": result.ImageID}); err == nil {
		t.Fatal("another reply reused screenshot ID")
	}
	if _, err := tool.Run(ctx, map[string]any{"action": "send", "image_id": result.ImageID, "user_id": "other", "group_id": "other"}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(uploaded, data) || chatID != event.GroupID || threadID != event.MessageThreadID {
		t.Fatalf("wrong image or destination: %s %s", chatID, threadID)
	}
	if len(tool.ToolResultParts("")) != 0 {
		t.Fatal("stale picture after send")
	}
	if _, err := tool.Run(ctx, map[string]any{"action": "send", "image_id": result.ImageID}); err == nil {
		t.Fatal("duplicate send accepted")
	}
	if _, err := tool.Run(ctx, map[string]any{"url": "https://example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Run(ctx, map[string]any{"url": "https://other.example"}); err == nil {
		t.Fatal("expected capture failure")
	}
	if len(tool.ToolResultParts("")) != 0 {
		t.Fatal("stale screenshot attached after failed capture")
	}
}

func TestPublicScreenshotFollowsBrowserPluginAndDoesNotGrantLoggedBrowser(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	plugin := newSandboxedBrowserRenderPlugin(screenshotBrowserStub{data: assistantScreenshotPNG(t)})
	manager := NewPluginManager(plugin)
	for _, enabled := range []bool{true, false} {
		tools, err := manager.AgentToolsWithOverrides(map[string]bool{sandboxedBrowserPluginID: enabled})
		if err != nil {
			t.Fatal(err)
		}
		cfg := BotConfig{AgentBrowserScreenshotAccess: BrowserScreenshotAccess{Mode: BrowserScreenshotDisabled}}.WithDefaults()
		registry, err := (&Runtime{}).newAgentRegistry(context.Background(), cfg, MessageEvent{UserID: "member"}, RelationshipPolicy{}, tools...)
		if err != nil {
			t.Fatal(err)
		}
		defer registry.Close()
		if _, ok := registry.Get("webpage_screenshot"); ok != enabled {
			t.Fatalf("plugin enabled=%v screenshot visible=%v", enabled, ok)
		}
		for _, name := range []string{"browser_screenshot", "browser_open", "browser_click", "view_image", "send_attachment"} {
			if _, ok := registry.Get(name); ok {
				t.Fatalf("public screenshot granted %s", name)
			}
		}
	}
}

// The live provider config comes from the deployed service; all deliveries are
// recorded locally and the screenshot uses a fresh public Chrome profile.
func TestLiveBrowserScreenshotCaptureAndSend(t *testing.T) {
	raw := os.Getenv("DIANA_TEST_BROWSER_LLM_CONFIG")
	if raw == "" {
		t.Skip("set DIANA_TEST_BROWSER_LLM_CONFIG to replay the deployed model")
	}
	var config llm.ProviderConfig
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		t.Fatal("invalid live provider configuration")
	}
	config.Timeout = 90 * time.Second
	client, err := llm.NewClient(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	channel := &recordingChannel{}
	r := NewRuntime(BotConfig{Platform: PlatformTelegram}, channel, NewPluginManager(), nil, nil, nil, nil)
	registry := agent.NewToolRegistry(agent.NewWebpageScreenshotTool(nil))
	event := MessageEvent{Platform: PlatformTelegram, Kind: EventKindPrivate, UserID: "screenshot-replay"}
	r.wrapScreenshotTools(registry, event)
	runner, err := agent.NewRunner(client, agent.Config{WorkDir: t.TempDir(), MaxSteps: 6}, registry)
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	response, err := runner.Run(ctx, agent.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "请把 https://example.com 的当前页面截图发给我，并告诉我页面标题。"}}})
	if err != nil {
		t.Fatal(err)
	}
	captured, sent := false, false
	for _, step := range response.Steps {
		if step.Tool != "webpage_screenshot" {
			continue
		}
		if step.Error != "" {
			t.Fatalf("screenshot replay failed: %s", step.Error)
		}
		sent = sent || step.Input["action"] == "send"
		captured = captured || step.Input["action"] != "send"
	}
	if !captured || !sent || len(channel.sent) != 1 || channel.sent[0].UserID != event.UserID || len(channel.sent[0].ImageURLs) != 1 || !strings.Contains(response.Text, "Example") {
		t.Fatalf("model did not capture, deliver to the current user and identify the page: capture=%v send=%v deliveries=%d", captured, sent, len(channel.sent))
	}
	t.Logf("live screenshot replay passed (%s): capture, view and current-user delivery", config.Model)
}
