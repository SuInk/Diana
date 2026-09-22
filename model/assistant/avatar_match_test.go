package assistant

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"strings"
	"testing"
)

func TestAvatarSimilarityMatchesResizedCompressedCopy(t *testing.T) {
	source := patternedAvatar(384)
	candidate := patternedAvatar(640)
	unrelated := unrelatedAvatar(640)

	var sourcePNG, candidateJPEG, unrelatedJPEG bytes.Buffer
	if err := png.Encode(&sourcePNG, source); err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(&candidateJPEG, candidate, &jpeg.Options{Quality: 82}); err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(&unrelatedJPEG, unrelated, &jpeg.Options{Quality: 82}); err != nil {
		t.Fatal(err)
	}
	sourceHash, err := avatarFingerprint(sourcePNG.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	candidateHash, _ := avatarFingerprint(candidateJPEG.Bytes())
	unrelatedHash, _ := avatarFingerprint(unrelatedJPEG.Bytes())
	matchScore := avatarSimilarity(sourceHash, candidateHash)
	unrelatedScore := avatarSimilarity(sourceHash, unrelatedHash)
	if matchScore < avatarMatchMinimumScore || matchScore-unrelatedScore < avatarMatchMinimumLead {
		t.Fatalf("match=%.4f unrelated=%.4f", matchScore, unrelatedScore)
	}
}

func TestNewImageEvidencePromotesLowValueDuplicate(t *testing.T) {
	event := MessageEvent{
		Kind: EventKindGroup, GroupID: "group", UserID: "user", MessageID: "current", Time: 200,
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{imageContentSHA256Key: "new-image"}}},
	}
	history := []MessageEvent{{
		Kind: EventKindGroup, GroupID: "group", UserID: "bot", MessageID: "answer", Time: 190, Outbound: true,
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{imageContentSHA256Key: "old-image"}}},
	}}
	if !imageEvidenceNewSinceLastBot(event, history, "bot") {
		t.Fatal("different current image was not treated as new evidence")
	}
	decision := proactiveReplyDecision{
		ShouldReply: false, Confidence: 0.98, Category: "none", Answerable: true,
		RequestsResponse: true, Blocker: proactiveBlockerLowValue, Reason: "duplicate",
	}
	if !promoteNewImageEvidence(&decision, event, true, 0.5, chatInSettings{}) {
		t.Fatal("new image evidence did not override the low-value duplicate decision")
	}
	if !decision.ShouldReply || decision.Category != "needs_response" || decision.TargetMessageID != event.MessageID {
		t.Fatalf("decision=%#v", decision)
	}

	history[0].Segments[0].Data[imageContentSHA256Key] = "new-image"
	if imageEvidenceNewSinceLastBot(event, history, "bot") {
		t.Fatal("the same previously answered image was treated as new evidence")
	}
}

func TestLiveAvatarSimilarity(t *testing.T) {
	sourcePath := os.Getenv("DIANA_LIVE_AVATAR_SOURCE")
	candidatePath := os.Getenv("DIANA_LIVE_AVATAR_CANDIDATE")
	if sourcePath == "" || candidatePath == "" {
		t.Skip("DIANA_LIVE_AVATAR_SOURCE and DIANA_LIVE_AVATAR_CANDIDATE are required")
	}
	sourceBody, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	candidateBody, err := os.ReadFile(candidatePath)
	if err != nil {
		t.Fatal(err)
	}
	source, err := avatarFingerprint(sourceBody)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := avatarFingerprint(candidateBody)
	if err != nil {
		t.Fatal(err)
	}
	score := avatarSimilarity(source, candidate)
	t.Logf("avatar similarity %.6f", score)
	if score < avatarMatchMinimumScore {
		t.Fatalf("score %.6f below threshold %.2f", score, avatarMatchMinimumScore)
	}
}

func patternedAvatar(size int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			nx, ny := float64(x)/float64(size), float64(y)/float64(size)
			value := uint8(225)
			switch {
			case (nx-0.32)*(nx-0.32)+(ny-0.42)*(ny-0.42) < 0.035:
				value = 55
			case (nx-0.68)*(nx-0.68)+(ny-0.42)*(ny-0.42) < 0.035:
				value = 105
			case ny > 0.66 && nx > 0.25 && nx < 0.75:
				value = 145
			}
			img.SetRGBA(x, y, color.RGBA{R: value, G: uint8(min(255, int(value)+8)), B: uint8(max(0, int(value)-5)), A: 255})
		}
	}
	return img
}

func unrelatedAvatar(size int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			value := uint8((x/24+y/24)%2) * 210
			img.SetRGBA(x, y, color.RGBA{R: value, G: 40, B: 220 - value/2, A: 255})
		}
	}
	return img
}

// 线上第一次有人用这个工具就撞上了：回复一张图问「这是哪个群成员头像」，当前消息里
// 只有一个 reply 段，一张图都没有，工具直接报「没有可匹配的图片」。图在引用消息里。
func TestAvatarMatchUsesQuotedImageWhenCurrentMessageHasNone(t *testing.T) {
	image := MessageSegment{Type: "image", Data: map[string]string{"url": "https://example.invalid/avatar.png"}}

	current := MessageEvent{Segments: []MessageSegment{image}}
	if segment, source, ok := avatarMatchImageSegment(current); !ok || source != "current_message" || segment.Data["url"] != image.Data["url"] {
		t.Fatalf("当前消息里的图没被选中：ok=%v source=%q", ok, source)
	}

	quoted := MessageEvent{
		Segments: []MessageSegment{{Type: "reply", Data: map[string]string{"id": "-421668118"}}},
		Quoted:   &QuotedMessage{MessageID: "-421668118", Segments: []MessageSegment{image}},
	}
	segment, source, ok := avatarMatchImageSegment(quoted)
	if !ok {
		t.Fatal("引用消息里的图没被用上，这条路就是线上报错的那条")
	}
	if source != "quoted_message" {
		t.Fatalf("没有说明比的是引用里的图：source=%q", source)
	}
	if segment.Data["url"] != image.Data["url"] {
		t.Fatalf("取错了图：%#v", segment)
	}
}

// 语义引用是 Diana 自己推断「这个」指哪条，不是用户点的。比错了图还会一本正经报出
// 某个成员，不如让模型看见没有图。
func TestAvatarMatchIgnoresSemanticQuote(t *testing.T) {
	event := MessageEvent{
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "这个是谁的头像"}}},
		Quoted: &QuotedMessage{
			MessageID: "guessed",
			Semantic:  true,
			Segments:  []MessageSegment{{Type: "image", Data: map[string]string{"url": "https://example.invalid/guessed.png"}}},
		},
	}
	if _, _, ok := avatarMatchImageSegment(event); ok {
		t.Fatal("语义引用里的图被当成了用户指定的图")
	}
}

// 视频抽帧不是用户发的图，不能拿来比对。
func TestAvatarMatchSkipsVideoFrames(t *testing.T) {
	event := MessageEvent{Segments: []MessageSegment{
		{Type: "image", Data: map[string]string{"url": "https://example.invalid/frame.png", "source_type": "video_frame"}},
	}}
	if _, _, ok := avatarMatchImageSegment(event); ok {
		t.Fatal("视频抽帧被当成了可匹配的图片")
	}
}

// 两边都没有图时，错误信息要说清楚两边都找过了，否则用户以为补发一张就行。
func TestAvatarMatchErrorMentionsBothPlaces(t *testing.T) {
	runtime := NewRuntime(BotConfig{ID: "qq", OwnerID: "10001"}, &recordingChannel{}, NewPluginManager(), nil, &stubReminderStore{}, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20005", UserID: "10001"}
	_, err := runtime.matchCurrentGroupMemberAvatar(t.Context(), event)
	if err == nil || !strings.Contains(err.Error(), "被引用") {
		t.Fatalf("错误信息没提到引用消息：%v", err)
	}
}
