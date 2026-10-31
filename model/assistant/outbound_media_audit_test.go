// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// testPNGDataURL 造一张能真正解码的小图；颜色不同，内容哈希就不同，缓存键也不同。
func testPNGDataURL(t *testing.T, shade uint8) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for x := 0; x < 4; x++ {
		for y := 0; y < 4; y++ {
			img.Set(x, y, color.RGBA{R: shade, G: 10, B: 10, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

func imageAuditRuntime(t *testing.T, enabled bool, reply string) (*Runtime, *qualityTestProvider, *recordingChannel) {
	t.Helper()
	outboundImageAuditCache.reset()
	t.Cleanup(outboundImageAuditCache.reset)
	provider := &qualityTestProvider{reply: reply}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{BotAccount: "42", ReplyImageAuditEnabled: boolPointer(enabled)}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	return runtime, provider, channel
}

var imageAuditGroupEvent = MessageEvent{Kind: EventKindGroup, Platform: PlatformOneBotV11, GroupID: "20001", UserID: "30001", MessageID: "40001"}

func imageAuditRequestImageCount(req llm.GenerateRequest) int {
	count := 0
	for _, message := range req.Messages {
		for _, part := range message.Parts {
			if part.Type == llm.ContentPartImageURL {
				count++
			}
		}
	}
	return count
}

// 没过审的图拿掉，原位置写明发不了和原因，同条文字照发；走的是真的发送入口。
func TestSendOutgoingDropsRejectedImageAndKeepsText(t *testing.T) {
	runtime, provider, channel := imageAuditRuntime(t, true, `{"images":[{"index":1,"account_safe":0.99},{"index":2,"account_safe":0.02,"account_risk":"explicit","reason":"裸露"}]}`)
	safe, unsafe := testPNGDataURL(t, 1), testPNGDataURL(t, 2)
	msg := OutgoingMessage{
		Text:        "给你看两张图",
		ImageURLs:   []string{safe, unsafe},
		ImageLabels: []string{"猫", "坏图"},
	}
	if _, err := runtime.sendOutgoingWithResult(t.Context(), imageAuditGroupEvent, msg); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("一条消息的图只该审一次，实际 %d 次", len(provider.requests))
	}
	if got := imageAuditRequestImageCount(provider.requests[0]); got != 2 {
		t.Fatalf("两张图都要交给审核模型，实际 %d 张", got)
	}
	sent := channel.sentSnapshot()
	if len(sent) != 1 {
		t.Fatalf("文字和过审的图仍要发出，实际 %d 条", len(sent))
	}
	got := sent[0]
	if len(got.ImageURLs) != 1 || got.ImageURLs[0] != safe {
		t.Fatalf("只该留下过审的那张：%v", got.ImageURLs)
	}
	if len(got.ImageLabels) != 1 || got.ImageLabels[0] != "猫" {
		t.Fatalf("标签要跟着图一起拿掉：%v", got.ImageLabels)
	}
	if !strings.HasPrefix(got.Text, "给你看两张图") || !strings.Contains(got.Text, "这张图片没有发出：未通过账号安全审核") {
		t.Fatalf("文字照发，并写明图片没发出和原因：%q", got.Text)
	}
	if strings.Contains(got.Text, "裸露") || strings.Contains(got.Text, "露骨内容") {
		t.Fatalf("默认智能档不点风险类别，也不复述审核器的具体理由：%q", got.Text)
	}
}

// 只有图没有字的消息，图被拿掉后发说明，不能整条无声消失。
func TestSendOutgoingImageOnlyBecomesNotice(t *testing.T) {
	runtime, _, channel := imageAuditRuntime(t, true, `{"images":[{"index":1,"account_safe":0.01,"account_risk":"politics"}]}`)
	if _, err := runtime.sendOutgoingWithResult(t.Context(), imageAuditGroupEvent, OutgoingMessage{ImageURLs: []string{testPNGDataURL(t, 3)}}); err != nil {
		t.Fatal(err)
	}
	sent := channel.sentSnapshot()
	if len(sent) != 1 || len(sent[0].ImageURLs) != 0 || !strings.Contains(sent[0].Text, "没有发出") {
		t.Fatalf("只有图的消息应换成说明：%#v", sent)
	}
}

// 默认关闭：不审、不多花一次调用，图片原样发出。
func TestSendOutgoingImageAuditDisabledByDefault(t *testing.T) {
	runtime, provider, channel := imageAuditRuntime(t, false, `{"images":[{"index":1,"account_safe":0.01}]}`)
	image := testPNGDataURL(t, 4)
	if _, err := runtime.sendOutgoingWithResult(t.Context(), imageAuditGroupEvent, OutgoingMessage{ImageURLs: []string{image}}); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 0 {
		t.Fatalf("关着时不该调模型，实际 %d 次", len(provider.requests))
	}
	if sent := channel.sentSnapshot(); len(sent) != 1 || len(sent[0].ImageURLs) != 1 {
		t.Fatalf("关着时图片原样发出：%#v", sent)
	}
}

// 审核挂掉放行，和文字审核的取舍一致。
func TestSendOutgoingImageAuditFailsOpen(t *testing.T) {
	runtime, provider, channel := imageAuditRuntime(t, true, "not json")
	if _, err := runtime.sendOutgoingWithResult(t.Context(), imageAuditGroupEvent, OutgoingMessage{ImageURLs: []string{testPNGDataURL(t, 5)}}); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("应该审过一次，实际 %d 次", len(provider.requests))
	}
	if sent := channel.sentSnapshot(); len(sent) != 1 || len(sent[0].ImageURLs) != 1 || strings.Contains(sent[0].Text, "没有发出") {
		t.Fatalf("审核读不出结论时放行：%#v", sent)
	}
}

// 同一张表情包反复发不重审。
func TestSendOutgoingImageAuditCachesVerdict(t *testing.T) {
	runtime, provider, channel := imageAuditRuntime(t, true, `{"images":[{"index":1,"account_safe":0.98}]}`)
	image := testPNGDataURL(t, 6)
	for range 2 {
		if _, err := runtime.sendOutgoingWithResult(t.Context(), imageAuditGroupEvent, OutgoingMessage{ImageURLs: []string{image}}); err != nil {
			t.Fatal(err)
		}
	}
	if len(provider.requests) != 1 {
		t.Fatalf("同一张图第二次应命中缓存，实际调了 %d 次", len(provider.requests))
	}
	if sent := channel.sentSnapshot(); len(sent) != 2 {
		t.Fatalf("两次都要发出，实际 %d 条", len(sent))
	}
}

// 边界和文字的账号安全审核一致：主人触发的不审，私聊跟随私聊开关。
func TestOutboundImageAuditEnabledFollowsAccountSafetyScope(t *testing.T) {
	cfg := BotConfig{OwnerID: "30009", ReplyImageAuditEnabled: boolPointer(true)}
	if !outboundImageAuditEnabled(imageAuditGroupEvent, cfg) {
		t.Fatal("群聊里非主人触发的应当审")
	}
	owner := imageAuditGroupEvent
	owner.UserID = "30009"
	if outboundImageAuditEnabled(owner, cfg) {
		t.Fatal("主人触发的不审")
	}
	private := MessageEvent{Kind: EventKindPrivate, UserID: "30001"}
	if outboundImageAuditEnabled(private, cfg) {
		t.Fatal("私聊默认不审")
	}
	cfg.ReplySafetyPrivateEnabled = boolPointer(true)
	if !outboundImageAuditEnabled(private, cfg) {
		t.Fatal("开了私聊审核后私聊也审")
	}
}

// 图片消息段（表情包形式发的）原位换成说明文字段。
func TestDropRejectedOutgoingImagesReplacesSegment(t *testing.T) {
	msg := OutgoingMessage{Segments: []MessageSegment{
		{Type: "text", Data: map[string]string{"text": "看"}},
		{Type: "image", Data: map[string]string{"file": "a.png", "sub_type": "1"}},
	}}
	got := dropRejectedOutgoingImages(msg, []int{1}, []*outboundImageVerdict{{Safe: false, Risk: "illegal"}}, RefusalStrategyExplain)
	if got.Segments[0].Data["text"] != "看" {
		t.Fatalf("文字段不动：%#v", got.Segments)
	}
	if got.Segments[1].Type != "text" || !strings.Contains(got.Segments[1].Data["text"], "违法或高风险内容") {
		t.Fatalf("图片段应换成说明：%#v", got.Segments[1])
	}
	if msg.Segments[1].Type != "image" {
		t.Fatal("不能改到调用方的切片")
	}
}

// 漏掉的图、读不出的分数按放行；整段不是 JSON 才算失败。
func TestParseOutboundImageAuditDecision(t *testing.T) {
	verdicts, ok := parseOutboundImageAuditDecision("```json\n{\"images\":[{\"index\":2,\"account_safe\":0.05,\"account_risk\":\"explicit\"},{\"index\":9,\"account_safe\":0}]}\n```", 3)
	if !ok {
		t.Fatal("应能解析")
	}
	if !verdicts[0].Safe || verdicts[1].Safe || !verdicts[2].Safe {
		t.Fatalf("只有第 2 张不安全，越界序号忽略：%#v", verdicts)
	}
	if _, ok := parseOutboundImageAuditDecision("没有结论", 1); ok {
		t.Fatal("读不出 JSON 应报失败")
	}
}

// 模型调 TTS 工具时回复只剩语音段，文字审核要拿到念的那段文字。
func TestAuditableReplyTextRestoresSpokenText(t *testing.T) {
	file := "http://127.0.0.1:8080/media/voice-test.mp3?t=1"
	voiceSpokenTexts.remember(file, "要念出来的话", time.Minute)
	reply := "[CQ:record,file=" + escapeCQParameter(file) + "]"
	if got := auditableReplyText(reply); got != "[语音] 要念出来的话" {
		t.Fatalf("语音段应换回文字，实际 %q", got)
	}
	other := "[CQ:record,file=http://example.com/song.mp3]"
	if got := auditableReplyText(other); got != other {
		t.Fatalf("认不出来源的语音段原样保留，实际 %q", got)
	}
}

// 转发卡片里的图同样要审，没过的换成说明文字段，卡片照发。
func TestAuditForwardNodeImagesReplacesRejected(t *testing.T) {
	runtime, provider, _ := imageAuditRuntime(t, true, `{"images":[{"index":1,"account_safe":0.03,"account_risk":"explicit"}]}`)
	nodes := []map[string]any{{"type": "node", "data": map[string]any{
		"name": "路人",
		"content": []any{
			map[string]any{"type": "text", "data": map[string]any{"text": "站外正文"}},
			map[string]any{"type": "image", "data": map[string]any{"file": testPNGDataURL(t, 7)}},
		},
	}}}
	got := runtime.auditForwardNodeImages(t.Context(), imageAuditGroupEvent, nodes)
	if len(provider.requests) != 1 {
		t.Fatalf("应审一次，实际 %d 次", len(provider.requests))
	}
	content := got[0]["data"].(map[string]any)["content"].([]map[string]any)
	if content[1]["type"] != "text" || !strings.Contains(segmentDataString(content[1]["data"], "text"), "没有发出") {
		t.Fatalf("图片段应换成说明：%#v", content[1])
	}
	if original := nodes[0]["data"].(map[string]any)["content"].([]any)[1].(map[string]any); original["type"] != "image" {
		t.Fatal("不能改到调用方的节点")
	}
}

// 说明的说法跟着拒答话术：只有说明原因写风险类别，模糊拒答连审核都不提。
func TestOutboundImageRejectedNoticeFollowsRefusalStrategy(t *testing.T) {
	rejected := []outboundImageVerdict{{Risk: "explicit", Reason: "裸露"}, {Risk: "politics"}}
	cases := map[RefusalStrategy]string{
		RefusalStrategyExplain: "（有 2 张图片没有发出：未通过账号安全审核，露骨内容、涉政内容）",
		RefusalStrategySmart:   "（有 2 张图片没有发出：未通过账号安全审核）",
		RefusalStrategyRewrite: "（有 2 张图片没有发出：未通过账号安全审核）",
		RefusalStrategyVague:   "（有 2 张图片发不了）",
		"":                     "（有 2 张图片没有发出：未通过账号安全审核）",
	}
	for strategy, want := range cases {
		if got := outboundImageRejectedNotice(strategy, rejected); got != want {
			t.Errorf("%q 档：got %q, want %q", strategy, got, want)
		}
	}
	if got := outboundImageRejectedNotice(RefusalStrategyVague, rejected[:1]); got != "（这张图片发不了）" {
		t.Errorf("单张图模糊拒答：%q", got)
	}
}
