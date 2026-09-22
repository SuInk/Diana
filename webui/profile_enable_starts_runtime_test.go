// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

// blockingChannel 的 Connect 会一直挂到 ctx 结束，和真实的反向 WebSocket 监听器
// 一样。fakeChannel 的 Connect 立刻返回 nil，运行时会把它当成「连接断了」，
// 启动后随即把自己标回未运行——那是测试替身的行为，不是启停逻辑的问题。
type blockingChannel struct{ fakeChannel }

func (blockingChannel) Connect(ctx context.Context, _ assistant.EventHandler) error {
	<-ctx.Done()
	return ctx.Err()
}

// 启用一台机器人时，停着的运行时要被拉起来。以前这里返回 200、界面把开关点亮，
// 运行时却一直是停的：ApplyProfiles 只重启本来就在跑的，停着的它不碰。接入端反连
// 过来会被一路 503 挡掉，而控制台只显示「等待连接」，看不出是没启动。
func TestEnablingProfileStartsStoppedRuntime(t *testing.T) {
	config := assistant.DefaultBotConfig()
	config.Enabled = false
	// 反连要求配了 Access Token 才算配置有效，否则 Start 会因为校验失败而返回。
	config.OneBotAccessToken = "enable-start-token"
	runtime := assistant.NewRuntime(config, blockingChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := NewBotHandlerWithFactory(ctx, runtime, func(assistant.BotConfig) assistant.Channel { return blockingChannel{} })
	router := botTestRouter(h)
	profile := h.profiles.Profiles().Profiles[0]

	if runtime.Status().Running {
		t.Fatal("runtime should start out stopped")
	}

	post := func(path string, payload any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(payload)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw)))
		return response
	}

	response := post("/api/assistant/config/profile-enabled", map[string]any{"profile_id": profile.ID, "enabled": true})
	if response.Code != http.StatusOK {
		t.Fatalf("enable: %d %s", response.Code, response.Body.String())
	}
	if !runtime.Status().Running {
		t.Fatal("enabling a bot left the runtime stopped")
	}

	// 停用回去仍然只改配置，不该顺带把运行时拉起来。
	if response = post("/api/assistant/config/profile-enabled", map[string]any{"profile_id": profile.ID, "enabled": false}); response.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", response.Code, response.Body.String())
	}
	if runtime.Status().Running {
		t.Fatal("disabling the only bot left the runtime running")
	}

	// 批量启用走的是另一个处理函数，同样要把运行时带起来。
	if response = post("/api/assistant/config/profiles-enabled", map[string]any{"enabled": true}); response.Code != http.StatusOK {
		t.Fatalf("enable all: %d %s", response.Code, response.Body.String())
	}
	if !runtime.Status().Running {
		t.Fatal("enabling every bot left the runtime stopped")
	}
}

// 机器人是启用着的、配置却起不来时，原因要留在状态里。以前这条 return 走在清空
// lastError 之前，接口看到的是「没在跑，也没有错误」，前端只能显示成「等待连接」。
func TestStartWithInvalidProfileRecordsReason(t *testing.T) {
	config := assistant.DefaultBotConfig()
	config.Enabled = true
	// 反连没配 Access Token：配置校验过不了，运行时起不来。
	config.OneBotAccessToken = ""
	runtime := assistant.NewRuntime(config, blockingChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)

	if err := runtime.Start(context.Background()); err == nil {
		t.Fatal("Start() succeeded with an invalid profile")
	}
	status := runtime.Status()
	if status.Running {
		t.Fatal("runtime reported running after a failed start")
	}
	if status.LastError == "" {
		t.Fatal("failed start left last_error empty")
	}
}

// 一台都没启用是用户自己的选择，不该在界面上留一条红色错误——每张卡都已经写着
// 「未启用」了。
func TestStartWithoutEnabledProfilesStaysQuiet(t *testing.T) {
	config := assistant.DefaultBotConfig()
	config.Enabled = false
	runtime := assistant.NewRuntime(config, blockingChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)

	if err := runtime.Start(context.Background()); err != assistant.ErrBotDisabled {
		t.Fatalf("Start() error = %v, want %v", err, assistant.ErrBotDisabled)
	}
	if status := runtime.Status(); status.Running || status.LastError != "" {
		t.Fatalf("status = %+v, want stopped without an error", status)
	}
}
