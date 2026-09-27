// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// videoQuotaServer 按 behavior 模拟视频接口：ok 正常出片，fail 受理后渲染失败，
// reject 提交时就拒绝。gate 非空时提交请求要等它关上才返回。
func videoQuotaServer(t *testing.T, behavior string, gate chan struct{}) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/media":
			_, _ = w.Write([]byte("mp4"))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/videos":
			if gate != nil {
				<-gate
			}
			if behavior == "reject" {
				http.Error(w, `{"error":"model not found"}`, http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"id":"vid","status":"queued"}`))
		case r.URL.Path == "/v1/videos/vid":
			if behavior == "fail" {
				_, _ = w.Write([]byte(`{"id":"vid","status":"failed","error":{"message":"render crashed"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"vid","status":"completed","progress":100}`))
		case r.URL.Path == "/v1/videos/vid/content":
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("mp4"))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func videoQuotaRuntime(t *testing.T, serverURL string, bot BotConfig) *Runtime {
	t.Helper()
	bot.ID = "qq"
	bot.OwnerID = "10001"
	bot.ModelRoles = map[string]ModelRole{"video": {ProfileID: "p1", Model: "sora-2", Params: map[string]string{"poll_interval_seconds": "0.001"}}}
	store := &stubLLMProfileStore{set: llm.ProfileSet{Profiles: []llm.Profile{mediaSlotProfile("p1", serverURL)}}}
	runtime := NewRuntime(bot, nilChannel{}, NewDefaultPluginManager(), store, nil, nil, nil)
	runtime.SetLocalMediaSharer(&recordingLocalMediaSharer{url: serverURL + "/media"})
	return runtime
}

func runVideoTool(t *testing.T, runtime *Runtime, userID, messageID string) dianaVideoToolResult {
	t.Helper()
	event := MessageEvent{Platform: "onebot", Kind: EventKindGroup, ProfileID: "qq", GroupID: "20001", UserID: userID, MessageID: messageID}
	raw, err := newDianaVideoTool(runtime, event, RelationshipPolicyFor(UserMemoryProfile{}, "10001", userID)).Run(context.Background(), map[string]any{"prompt": "海边日落"})
	if err != nil {
		t.Fatal(err)
	}
	var result dianaVideoToolResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("result = %q: %v", raw, err)
	}
	return result
}

func waitVideoTasks(t *testing.T, runtime *Runtime) {
	t.Helper()
	waitForCondition(t, 3*time.Second, func() bool { return runtime.activeSubagentTaskCount() == 0 })
}

// 每群上限用满后，工具如实返回超限和能照念的说明，而不是报一个会被重试的错误。
func TestVideoToolBlocksWhenGroupQuotaExhausted(t *testing.T) {
	server := videoQuotaServer(t, "ok", nil)
	runtime := videoQuotaRuntime(t, server.URL, BotConfig{VideoGenerationDailyGroupLimit: 1})
	if result := runVideoTool(t, runtime, "20002", "v-1"); !result.OK || result.QuotaExceeded {
		t.Fatalf("first = %#v", result)
	}
	waitVideoTasks(t, runtime)
	result := runVideoTool(t, runtime, "20003", "v-2")
	if result.OK || !result.QuotaExceeded {
		t.Fatalf("用满之后应当超限：%#v", result)
	}
	for _, want := range []string{"今天本群的视频生成次数已用完（1/1），明天再来", "不要再调用 video", "变相交付视频"} {
		if !strings.Contains(result.Notice, want) {
			t.Fatalf("notice 缺少 %q：%q", want, result.Notice)
		}
	}
}

// 主人不受限。
func TestVideoToolExemptsOwner(t *testing.T) {
	server := videoQuotaServer(t, "ok", nil)
	runtime := videoQuotaRuntime(t, server.URL, BotConfig{VideoGenerationDailyGroupLimit: 1, VideoGenerationDailyUserLimit: 1})
	for index := range 3 {
		if result := runVideoTool(t, runtime, "10001", "owner-"+string(rune('a'+index))); !result.OK || result.QuotaExceeded {
			t.Fatalf("主人第 %d 次被拦：%#v", index+1, result)
		}
		waitVideoTasks(t, runtime)
	}
}

// 任务受理后渲染失败照样花了钱，按一次记账；提交时就被拒的没建任务，不记。
func TestVideoToolCountsAcceptedFailuresButNotRejectedSubmits(t *testing.T) {
	failed := videoQuotaRuntime(t, videoQuotaServer(t, "fail", nil).URL, BotConfig{VideoGenerationDailyGroupLimit: 1})
	if result := runVideoTool(t, failed, "20002", "fail-1"); !result.OK {
		t.Fatalf("first = %#v", result)
	}
	waitVideoTasks(t, failed)
	if result := runVideoTool(t, failed, "20003", "fail-2"); !result.QuotaExceeded {
		t.Fatalf("受理后失败也应当算一次：%#v", result)
	}

	rejected := videoQuotaRuntime(t, videoQuotaServer(t, "reject", nil).URL, BotConfig{VideoGenerationDailyGroupLimit: 1})
	for index := range 2 {
		if result := runVideoTool(t, rejected, "20002", "reject-"+string(rune('a'+index))); !result.OK || result.QuotaExceeded {
			t.Fatalf("提交被拒不该占次数，第 %d 次：%#v", index+1, result)
		}
		waitVideoTasks(t, rejected)
	}
}

// 同样的任务还在跑时重发请求直接复用，不占名额。
func TestVideoToolReusesRunningTaskWithoutQuota(t *testing.T) {
	gate := make(chan struct{})
	server := videoQuotaServer(t, "ok", gate)
	runtime := videoQuotaRuntime(t, server.URL, BotConfig{VideoGenerationDailyGroupLimit: 1})
	first := runVideoTool(t, runtime, "20002", "same")
	if !first.OK || first.Reused {
		t.Fatalf("first = %#v", first)
	}
	second := runVideoTool(t, runtime, "20002", "same")
	close(gate)
	if second.QuotaExceeded || !second.Reused || second.TaskID != first.TaskID {
		t.Fatalf("重发同样的请求应当复用在跑的任务：%#v", second)
	}
	waitVideoTasks(t, runtime)
}

// 群覆盖三态：nil 跟随机器人，0 本群不限，正数本群上限；视频和生图各算各的。
func TestVideoQuotaGroupOverrideStates(t *testing.T) {
	bot := BotConfig{VideoGenerationDailyGroupLimit: 2, ImageGenerationDailyGroupLimit: 9}
	for _, tc := range []struct {
		override *int64
		want     int64
	}{
		{nil, 2},
		{limitPtr(0), 0},
		{limitPtr(1), 1},
	} {
		groupLimit, _ := EffectiveMediaGenerationLimits(bot, GroupConfig{VideoGenerationDailyGroupLimit: tc.override}, MediaGenerationVideo)
		if groupLimit != tc.want {
			t.Fatalf("override %v => %d, want %d", tc.override, groupLimit, tc.want)
		}
	}
	runtime, event := mediaQuotaRuntime(t, BotConfig{VideoGenerationDailyGroupLimit: 5, ImageGenerationDailyGroupLimit: 1}, GroupConfig{VideoGenerationDailyGroupLimit: limitPtr(1)})
	if _, err := runtime.reserveMediaGeneration(context.Background(), event, MediaGenerationImage, 1); err != nil {
		t.Fatal(err)
	}
	reservation, err := runtime.reserveMediaGeneration(context.Background(), event, MediaGenerationVideo, 1)
	if err != nil {
		t.Fatalf("在途的生图不该占视频的名额：%v", err)
	}
	reservation.commit(context.Background(), 1)
	_, err = runtime.reserveMediaGeneration(context.Background(), event, MediaGenerationVideo, 1)
	if notice := quotaErrorOf(t, err).Notice(); notice != "今天本群的视频生成次数已用完（1/1），明天再来" {
		t.Fatalf("应当按群里的 1 次拦：%q", notice)
	}
	unlimited, unlimitedEvent := mediaQuotaRuntime(t, BotConfig{VideoGenerationDailyGroupLimit: 1}, GroupConfig{VideoGenerationDailyGroupLimit: limitPtr(0)})
	for range 3 {
		if err := reserveVideoAndCommit(unlimited, unlimitedEvent); err != nil {
			t.Fatalf("本群不限时不该拦：%v", err)
		}
	}
}

// 每人上限跨群合计。
func TestVideoQuotaBlocksUserAcrossGroups(t *testing.T) {
	runtime, event := mediaQuotaRuntime(t, BotConfig{VideoGenerationDailyUserLimit: 1}, GroupConfig{})
	if err := reserveVideoAndCommit(runtime, event); err != nil {
		t.Fatal(err)
	}
	event.GroupID = "20009"
	if notice := quotaErrorOf(t, reserveVideoAndCommit(runtime, event)).Notice(); notice != "今天你的视频生成次数已用完（1/1），明天再来" {
		t.Fatalf("notice = %q", notice)
	}
}

// 群配置里视频上限的「不限」（0）和「跟随」存一遍再读回来要分得开。
func TestVideoQuotaGroupOverrideSurvivesRoundTrip(t *testing.T) {
	bot := BotConfig{ID: "qq", VideoGenerationDailyGroupLimit: 4, VideoGenerationDailyUserLimit: 2}
	data, err := json.Marshal(bot.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	var restored BotConfig
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.VideoGenerationDailyGroupLimit != 4 || restored.VideoGenerationDailyUserLimit != 2 {
		t.Fatalf("机器人配置回读 = %#v", restored)
	}
	for _, tc := range []struct {
		raw  string
		want int64
	}{
		{`{"group_id":"20001","video_generation_daily_group_limit":0}`, 0},
		{`{"group_id":"20001","video_generation_daily_group_limit":3}`, 3},
		{`{"group_id":"20001"}`, 4},
	} {
		var saved GroupConfig
		if err := json.Unmarshal([]byte(tc.raw), &saved); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(saved.WithDefaults("20001", restored))
		if err != nil {
			t.Fatal(err)
		}
		var reloaded GroupConfig
		if err := json.Unmarshal(data, &reloaded); err != nil {
			t.Fatal(err)
		}
		if groupLimit, _ := EffectiveMediaGenerationLimits(restored, reloaded, MediaGenerationVideo); groupLimit != tc.want {
			t.Fatalf("%s 回读后每群上限 = %d，want %d", tc.raw, groupLimit, tc.want)
		}
	}
}

func reserveVideoAndCommit(runtime *Runtime, event MessageEvent) error {
	reservation, err := runtime.reserveMediaGeneration(context.Background(), event, MediaGenerationVideo, 1)
	if err != nil {
		return err
	}
	reservation.commit(context.Background(), 1)
	return nil
}
