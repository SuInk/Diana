// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newAIImageDetectTestTool(t *testing.T, settings map[string]any, event MessageEvent) *dianaAIImageDetectTool {
	t.Helper()
	manager := NewPluginManager(NewAIImageDetectPlugin(nil))
	if settings != nil {
		if _, err := manager.UpdateSettings(aiImageDetectPluginID, settings); err != nil {
			t.Fatal(err)
		}
	}
	pluginValue, values, enabled := manager.PluginWithSettings(aiImageDetectPluginID, nil)
	if !enabled {
		t.Fatal("插件应当默认启用")
	}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, manager, nil, nil, nil, nil)
	return newDianaAIImageDetectTool(runtime, event, pluginValue.(*AIImageDetectPlugin), values)
}

func aiImageDetectTestEvent(imageURL string) MessageEvent {
	return MessageEvent{
		Kind: EventKindGroup, GroupID: "g", UserID: "10001", MessageID: "msg-1", ToMe: true,
		Segments: []MessageSegment{
			{Type: "text", Data: map[string]string{"text": "这是 AI 图吗"}},
			{Type: "image", Data: map[string]string{"url": imageURL}},
		},
	}
}

func runAIImageDetectTool(t *testing.T, tool *dianaAIImageDetectTool) dianaAIImageDetectResult {
	t.Helper()
	payload, err := tool.Run(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var result dianaAIImageDetectResult
	if err := json.Unmarshal([]byte(payload), &result); err != nil {
		t.Fatalf("结果不是 JSON：%v（%s）", err, payload)
	}
	return result
}

func TestAIImageDetectToolDetectsCurrentMessageImage(t *testing.T) {
	data := withPNGChunk(aiImageTestPNG(t), "tEXt", []byte("Software\x00NovelAI"))
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
	result := runAIImageDetectTool(t, newAIImageDetectTestTool(t, nil, aiImageDetectTestEvent(dataURL)))
	if !result.OK || result.Report == nil || result.Report.Verdict != aiImageVerdictMarked || result.MessageID != "msg-1" {
		t.Fatalf("result = %#v", result)
	}
	if !strings.Contains(result.Message, "NovelAI") || !strings.Contains(result.Message, "SynthID") {
		t.Fatalf("结论没说清证据和 SynthID 边界：%s", result.Message)
	}
}

// 大图走给模型看图的加载路径会被缩放重编码、元数据全丢。检测必须读原文件。
func TestAIImageDetectToolReadsOriginalBytesOfLargeImage(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 4000, 300))); err != nil {
		t.Fatal(err)
	}
	data := withPNGChunk(buf.Bytes(), "tEXt", []byte("parameters\x00cat\nSteps: 30, Sampler: DPM++ 2M, CFG scale: 6"))
	path := filepath.Join(t.TempDir(), "large.png")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	result := runAIImageDetectTool(t, newAIImageDetectTestTool(t, nil, aiImageDetectTestEvent(path)))
	if !result.OK || result.Report == nil || result.Report.Verdict != aiImageVerdictMarked {
		t.Fatalf("result = %#v", result)
	}
}

// 查不到标识时不能让模型说成「是真图」。
func TestAIImageDetectToolSummaryForUnmarkedImage(t *testing.T) {
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(aiImageTestPNG(t))
	result := runAIImageDetectTool(t, newAIImageDetectTestTool(t, nil, aiImageDetectTestEvent(dataURL)))
	if !result.OK || result.Report.Verdict != aiImageVerdictNone {
		t.Fatalf("result = %#v", result)
	}
	for _, want := range []string{"不代表一定不是 AI 图", "没有元数据", "没有配置 SynthID 检测服务"} {
		if !strings.Contains(result.Message, want) {
			t.Fatalf("结论缺少 %q：%s", want, result.Message)
		}
	}
}

func TestAIImageDetectToolRespectsPrivateSwitch(t *testing.T) {
	event := aiImageDetectTestEvent("data:image/png;base64," + base64.StdEncoding.EncodeToString(aiImageTestPNG(t)))
	event.Kind = EventKindPrivate
	event.GroupID = ""
	result := runAIImageDetectTool(t, newAIImageDetectTestTool(t, map[string]any{aiImageDetectSettingPrivateEnabled: false}, event))
	if result.OK || result.Report != nil {
		t.Fatalf("私聊关闭时仍然检测了：%#v", result)
	}
}

func TestAIImageDetectToolWithoutImage(t *testing.T) {
	event := aiImageDetectTestEvent("")
	event.Segments = event.Segments[:1]
	result := runAIImageDetectTool(t, newAIImageDetectTestTool(t, nil, event))
	if result.OK || !strings.Contains(result.Message, dianaChatHistoryToolName) {
		t.Fatalf("result = %#v", result)
	}
}

// 插件默认装好启用，群成员也能用；停用后模型看不到工具。
func TestAIImageDetectToolRegistration(t *testing.T) {
	state, ok := NewDefaultPluginManager().Get(aiImageDetectPluginID)
	if !ok || !state.Installed || !state.Enabled {
		t.Fatalf("state = %#v, ok = %v", state, ok)
	}
	if !(RelationshipPolicy{}).allowedAgentToolNames()[dianaAIImageDetectToolName] {
		t.Fatal("群成员应当能调用 AI 图片检测")
	}

	toolPromptFor := func(t *testing.T, enabled bool) string {
		t.Helper()
		provider := &agentSequenceLLMProvider{responses: []string{
			`{"action":"none","prompt":"","tools":[],"context_message_ids":[],"keep_older_summary":false}`,
			`{"action":"final","content":"好"}`,
		}}
		plugins := NewDefaultPluginManager()
		if !enabled {
			if _, err := plugins.SetEnabledForProfile(aiImageDetectPluginID, "qq", false); err != nil {
				t.Fatal(err)
			}
		}
		runtime := NewRuntime(BotConfig{OwnerID: "owner", AgentEnabled: true}, nilChannel{}, plugins, nil, nil, nil, func() (LLMProvider, error) {
			return provider, nil
		})
		if _, err := runtime.replyTo(context.Background(), MessageEvent{
			Kind: EventKindPrivate, UserID: "owner", MessageID: "message-1", ProfileID: "qq",
		}, "这是 AI 图吗"); err != nil {
			t.Fatal(err)
		}
		if len(provider.requests) == 0 {
			t.Fatal("provider was not called")
		}
		return provider.requests[len(provider.requests)-1].Messages[0].Content
	}
	if prompt := toolPromptFor(t, true); !strings.Contains(prompt, dianaAIImageDetectToolName) {
		t.Fatalf("启用时模型看不到检测工具：%s", prompt)
	}
	if prompt := toolPromptFor(t, false); strings.Contains(prompt, dianaAIImageDetectToolName) {
		t.Fatalf("停用后不该挂检测工具：%s", prompt)
	}
}
