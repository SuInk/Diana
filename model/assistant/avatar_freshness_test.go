// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"strings"
	"testing"
	"time"
)

// 线上那一次：北京时间 00:54 换的头像，qlogo 回的是「00:54:02 GMT」。
func TestAvatarLastModifiedReadsQLogoGMTAsBeijingTime(t *testing.T) {
	now := time.Date(2026, 9, 26, 1, 5, 30, 0, avatarBeijingZone)
	raw := "Sat, 26 Sep 2026 00:54:02 GMT"
	want := time.Date(2026, 9, 26, 0, 54, 2, 0, avatarBeijingZone)

	if got := avatarLastModified(OneBotMemberAvatarURL("10001"), raw, now); !got.Equal(want) {
		t.Fatalf("qlogo 时间 = %v，want %v", got, want)
	}
	if got := avatarLastModified(OneBotGroupAvatarURL("20002"), raw, now); !got.Equal(want) {
		t.Fatalf("qlogo 群头像时间 = %v，want %v", got, want)
	}
	// 别的来源按规范当 GMT；字面值算出来在未来时，退回按北京时间解释。
	if got := avatarLastModified("https://cdn.example.com/a.png", raw, now); !got.Equal(want) {
		t.Fatalf("未来时间没有退回北京时间：%v", got)
	}
	honest := "Fri, 25 Sep 2026 16:54:02 GMT"
	if got := avatarLastModified("https://cdn.example.com/a.png", honest, now); !got.Equal(want) {
		t.Fatalf("真实 GMT 被改动：%v", got)
	}
	// 按北京时间解释仍在未来、或者根本解析不了，宁可不给。
	for _, bad := range []string{"Sat, 26 Sep 2026 09:30:00 GMT", "not a date", ""} {
		if got := avatarLastModified(OneBotMemberAvatarURL("10001"), bad, now); !got.IsZero() {
			t.Fatalf("%q 应当丢弃，得到 %v", bad, got)
		}
	}
	if isQLogoURL("https://qlogo.cn.evil.com/a.png") {
		t.Fatal("伪造的 qlogo 域名被认成 qlogo")
	}
}

func TestFormatAvatarUpdateHint(t *testing.T) {
	now := time.Date(2026, 9, 26, 1, 5, 0, 0, avatarBeijingZone)
	for age, want := range map[time.Duration]string{
		20 * time.Second:     "头像刚刚更新过",
		11 * time.Minute:     "头像约 11 分钟前更新过",
		5 * time.Hour:        "头像约 5 小时前更新过",
		3 * 24 * time.Hour:   "头像约 3 天前更新过",
		100 * 24 * time.Hour: "头像约 3 个月前更新过",
	} {
		if got := formatAvatarUpdateHint(now.Add(-age), now); got != want {
			t.Fatalf("age %v = %q，want %q", age, got, want)
		}
	}
}

type stubAvatarResponse struct {
	body         []byte
	lastModified string
}

// stubAvatarFetch 让 view_avatar 走本地数据，同时记下它请求的地址。
func stubAvatarFetch(t *testing.T, next func() stubAvatarResponse) *[]string {
	t.Helper()
	var requested []string
	original := fetchAvatarImage
	fetchAvatarImage = func(_ context.Context, imageURL string, _ int64) ([]byte, string, http.Header, error) {
		requested = append(requested, imageURL)
		resp := next()
		header := http.Header{}
		if resp.lastModified != "" {
			header.Set("Last-Modified", resp.lastModified)
		}
		return resp.body, "image/png", header, nil
	}
	t.Cleanup(func() { fetchAvatarImage = original })
	return &requested
}

func solidAvatarPNG(t *testing.T, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for x := 0; x < 32; x++ {
		for y := 0; y < 32; y++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func runViewAvatar(t *testing.T, r *Runtime, event MessageEvent, source string) map[string]any {
	t.Helper()
	tool := newDianaRemoteImageTool(r, event)
	output, err := tool.Run(context.Background(), map[string]any{"action": "view_avatar", "avatar_source": source})
	if err != nil {
		t.Fatal(err)
	}
	if parts := tool.ToolResultParts(output); len(parts) != 1 || !strings.HasPrefix(parts[0].ImageURL, "data:image/") {
		t.Fatalf("头像没有作为图片附上：%+v", parts)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

// 以前 view_avatar 只交回一张图，模型分不出「一直是这样」和「刚换成这样」。
func TestRemoteImageViewAvatarReportsQLogoUpdateTime(t *testing.T) {
	now := time.Date(2026, 9, 26, 1, 5, 30, 0, avatarBeijingZone)
	avatar := solidAvatarPNG(t, color.RGBA{R: 200, A: 255})
	requested := stubAvatarFetch(t, func() stubAvatarResponse {
		return stubAvatarResponse{body: avatar, lastModified: "Sat, 26 Sep 2026 00:54:02 GMT"}
	})
	r := NewRuntime(BotConfig{Platform: PlatformOneBotV11, BotAccount: "10001"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.now = func() time.Time { return now }
	event := MessageEvent{Platform: PlatformOneBotV11, Kind: EventKindGroup, GroupID: "1049765710", UserID: "22222", SelfID: "10001"}

	result := runViewAvatar(t, r, event, avatarSourceBot)
	if len(*requested) != 1 || !strings.Contains((*requested)[0], "qlogo.cn") || !strings.Contains((*requested)[0], "nk=10001") {
		t.Fatalf("请求的头像地址不对：%v", *requested)
	}
	if result["updated_at"] != "2026-09-26 00:54" {
		t.Fatalf("updated_at = %v", result["updated_at"])
	}
	if result["updated_hint"] != "头像约 11 分钟前更新过" {
		t.Fatalf("updated_hint = %v", result["updated_hint"])
	}
	if result["image_id"] != "remote_image_1" || result["avatar_source"] != avatarSourceBot {
		t.Fatalf("原有字段丢了：%v", result)
	}
	// 第一次看没有可比的，不能编出「和上次一样」。
	if _, ok := result["change_note"]; ok {
		t.Fatalf("第一次看就给了比对结论：%v", result)
	}
}

func TestRemoteImageViewAvatarNoticesBotAvatarChangedSinceLastView(t *testing.T) {
	now := time.Date(2026, 9, 26, 0, 30, 0, 0, avatarBeijingZone)
	current := solidAvatarPNG(t, color.RGBA{B: 200, A: 255})
	stubAvatarFetch(t, func() stubAvatarResponse { return stubAvatarResponse{body: current} })
	r := NewRuntime(BotConfig{Platform: PlatformOneBotV11, BotAccount: "10001"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.now = func() time.Time { return now }
	event := MessageEvent{Platform: PlatformOneBotV11, Kind: EventKindGroup, GroupID: "1049765710", UserID: "22222", SelfID: "10001"}

	first := runViewAvatar(t, r, event, avatarSourceBot)
	if _, ok := first["same_as_last_view"]; ok {
		t.Fatalf("第一次看不该有比对：%v", first)
	}
	if _, ok := first["updated_at"]; ok {
		t.Fatalf("没有 Last-Modified 时不该编更新时间：%v", first)
	}

	now = now.Add(10 * time.Minute)
	same := runViewAvatar(t, r, event, avatarSourceBot)
	if same["same_as_last_view"] != true || same["last_viewed_at"] != "2026-09-26 00:30" {
		t.Fatalf("同一张图没认出来：%v", same)
	}

	now = now.Add(30 * time.Minute)
	current = solidAvatarPNG(t, color.RGBA{G: 200, A: 255})
	changed := runViewAvatar(t, r, event, avatarSourceBot)
	if changed["same_as_last_view"] != false || !strings.Contains(stringFromAny(changed["change_note"]), "不一样") {
		t.Fatalf("换过的头像没报出来：%v", changed)
	}
	if changed["last_viewed_at"] != "2026-09-26 00:40" {
		t.Fatalf("last_viewed_at = %v", changed["last_viewed_at"])
	}

	// 只跟踪机器人自己；别人的头像不记，也不给比对。
	sender := runViewAvatar(t, r, event, avatarSourceSender)
	if _, ok := sender["same_as_last_view"]; ok {
		t.Fatalf("发送者头像也做了比对：%v", sender)
	}
	// 另一台机器人的记录互不串。
	other := NewRuntime(BotConfig{Platform: PlatformOneBotV11, BotAccount: "10002"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	other.now = r.now
	if result := runViewAvatar(t, other, event, avatarSourceBot); result["same_as_last_view"] != nil {
		t.Fatalf("别的机器人拿到了这台的记录：%v", result)
	}
}

// 线上机器人没查就说「头像根本没变 你喝多眼花了吧」。这条规则对谁都一样，进稳定提示词，
// 并且登记在提示词表里，能在 WebUI 里改。
func TestSystemPromptTellsBotNotToDenyObservedSelfChanges(t *testing.T) {
	runtime := NewRuntime(BotConfig{ID: "qq"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	prompt := runtime.systemPrompt(MessageEvent{Kind: EventKindGroup, GroupID: "1", UserID: "2"}, nil)
	if !strings.Contains(prompt, promptSelfObservation) {
		t.Fatal("系统提示词里没有「别人说你变了」这条规则")
	}
	for _, want := range []string{"能用工具查就先查", "不要断然否认对方的观察", "照结果改口"} {
		if !strings.Contains(promptSelfObservation, want) {
			t.Fatalf("规则缺少关键要求：%s", want)
		}
	}
	registered := false
	for _, spec := range PromptSpecs() {
		if spec.Key == "reply.self_observation" {
			registered = spec.Default == promptSelfObservation
		}
	}
	if !registered {
		t.Fatal("reply.self_observation 没有登记")
	}
	override := NewRuntime(BotConfig{ID: "qq", PromptOverrides: map[string]string{"reply.self_observation": "自定义的自我核查规则"}}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	if got := override.systemPrompt(MessageEvent{Kind: EventKindGroup, GroupID: "1", UserID: "2"}, nil); !strings.Contains(got, "自定义的自我核查规则") || strings.Contains(got, promptSelfObservation) {
		t.Fatal("改写没有生效")
	}
}
