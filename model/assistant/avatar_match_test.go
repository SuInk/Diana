package assistant

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync/atomic"
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

// patternedRect 是任意宽高比的测试图，用来喂比例闸门。
func patternedRect(width, height int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			value := uint8((x/16+y/16)%2)*180 + 40
			img.SetRGBA(x, y, color.RGBA{R: value, G: value, B: value, A: 255})
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

// avatarDirectoryChannel 是带成员名单和成员头像的假通道，用来把整条匹配链路真跑一遍：
// 取图 → 解码 → 指纹 → 和成员头像比分。
type avatarDirectoryChannel struct {
	*recordingChannel
	members     map[string][]byte
	avatarCalls atomic.Int64
}

func (c *avatarDirectoryChannel) GroupMember(_ context.Context, groupID, userID string) (OneBotGroupMemberInfo, error) {
	if _, ok := c.members[userID]; !ok {
		return OneBotGroupMemberInfo{}, fmt.Errorf("不是群成员")
	}
	return OneBotGroupMemberInfo{GroupID: groupID, UserID: userID, Nickname: "成员" + userID, MembershipVerified: true}, nil
}

func (c *avatarDirectoryChannel) GroupMembers(_ context.Context, groupID string) (GroupMemberDirectory, error) {
	members := make([]OneBotGroupMemberInfo, 0, len(c.members))
	for userID := range c.members {
		members = append(members, OneBotGroupMemberInfo{GroupID: groupID, UserID: userID, Nickname: "成员" + userID, MembershipVerified: true})
	}
	sort.Slice(members, func(left, right int) bool { return members[left].UserID < members[right].UserID })
	return GroupMemberDirectory{Members: members, Complete: true, Total: len(members), TotalKnown: true}, nil
}

func (c *avatarDirectoryChannel) MemberAvatar(_ context.Context, userID string) (GroupAvatar, error) {
	c.avatarCalls.Add(1)
	body, ok := c.members[userID]
	if !ok {
		return GroupAvatar{}, fmt.Errorf("没有头像")
	}
	return GroupAvatar{Data: body, ContentType: "image/png"}, nil
}

// 整条链路跑一遍：用户回复一张图问「这是谁的头像」，当前消息里没有图，图在引用里，
// 而且是从 URL 现取的。要真的认出人来，不是「没报错」就算过。
func TestAvatarMatchResolvesQuotedImageEndToEnd(t *testing.T) {
	var target, other bytes.Buffer
	if err := png.Encode(&target, patternedAvatar(256)); err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(&other, unrelatedAvatar(256)); err != nil {
		t.Fatal(err)
	}
	// 用户发的那张图：同一张头像被重新编码成 JPEG、尺寸也不一样，和真实转发一致。
	var posted bytes.Buffer
	if err := jpeg.Encode(&posted, patternedAvatar(384), &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}

	var requested atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requested.Add(1)
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(posted.Bytes())
	}))
	defer server.Close()

	channel := &avatarDirectoryChannel{
		recordingChannel: &recordingChannel{},
		members:          map[string][]byte{"20002": target.Bytes(), "20003": other.Bytes()},
	}
	runtime := NewRuntime(BotConfig{ID: "qq", OwnerID: "10001"}, channel, NewPluginManager(), nil, &stubReminderStore{}, nil, nil)

	event := MessageEvent{
		Kind: EventKindGroup, GroupID: "20005", UserID: "10001", MessageID: "266007836",
		Segments: []MessageSegment{
			{Type: "reply", Data: map[string]string{"id": "-421668118"}},
			{Type: "text", Data: map[string]string{"text": "这个是哪个群成员头像"}},
		},
		Quoted: &QuotedMessage{
			MessageID: "-421668118",
			Segments:  []MessageSegment{{Type: "image", Data: map[string]string{"url": server.URL + "/quoted.jpg"}}},
		},
	}

	match, err := runtime.matchCurrentGroupMemberAvatar(t.Context(), event)
	if err != nil {
		t.Fatalf("匹配报错了：%v", err)
	}
	if requested.Load() == 0 {
		t.Fatal("引用消息里的图根本没被取过")
	}
	if !match.Matched {
		t.Fatalf("没认出人来：%+v", match)
	}
	if match.UserID != "20002" {
		t.Fatalf("认错人了：%+v", match)
	}
	if match.ImageSource != "quoted_message" {
		t.Fatalf("没说明比的是引用里的图：%+v", match)
	}
	if match.Compared != 2 || !match.CandidatesComplete {
		t.Fatalf("候选统计不对：%+v", match)
	}
}

// 用户直接发一张群成员的头像，不问任何问题。Diana 要当场知道这是谁的头像，
// 而不是等模型想起来调 match_avatar——多数时候它想不起来，直接就着画面说话了。
func TestAvatarMatchAnnotatesInboundGroupImage(t *testing.T) {
	var target, other bytes.Buffer
	if err := png.Encode(&target, patternedAvatar(256)); err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(&other, unrelatedAvatar(256)); err != nil {
		t.Fatal(err)
	}
	var posted bytes.Buffer
	if err := jpeg.Encode(&posted, patternedAvatar(320), &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(posted.Bytes())
	}))
	defer server.Close()

	channel := &avatarDirectoryChannel{
		recordingChannel: &recordingChannel{},
		members:          map[string][]byte{"20002": target.Bytes(), "20003": other.Bytes()},
	}
	runtime := NewRuntime(BotConfig{ID: "qq", OwnerID: "10001"}, channel, NewPluginManager(), nil, &stubReminderStore{}, nil, nil)
	postedSegment := MessageSegment{Type: "image", Data: map[string]string{"url": server.URL + "/posted.jpg"}}
	event := MessageEvent{
		Kind: EventKindGroup, GroupID: "20005", UserID: "10001", MessageID: "m1",
		Segments: []MessageSegment{postedSegment},
	}

	annotation := runtime.avatarMatchAnnotation(t.Context(), event)
	if annotation == "" {
		t.Fatal("发头像进来没有自动比对")
	}
	if !strings.Contains(annotation, "成员20002") || !strings.Contains(annotation, "user_id=20002") {
		t.Fatalf("没说清是谁：%q", annotation)
	}

	// 私聊没有群成员可比，不该浪费这一趟。
	private := event
	private.Kind = EventKindPrivate
	if got := runtime.avatarMatchAnnotation(t.Context(), private); got != "" {
		t.Fatalf("私聊也比了：%q", got)
	}

	// 引用里的旧图不自动比：那通常是在聊图本身，要问模型再调工具。
	quotedOnly := MessageEvent{
		Kind: EventKindGroup, GroupID: "20005", UserID: "10001", MessageID: "m2",
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "哈哈"}}},
		Quoted:   &QuotedMessage{MessageID: "m1", Segments: []MessageSegment{postedSegment}},
	}
	if got := runtime.avatarMatchAnnotation(t.Context(), quotedOnly); got != "" {
		t.Fatalf("引用里的旧图被自动比了：%q", got)
	}

	// 没比中就什么都不附——群里表情包一条接一条，每条加一句「未匹配」纯是 token 开销。
	var meme bytes.Buffer
	if err := jpeg.Encode(&meme, unrelatedAvatar(320), &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
	memeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(meme.Bytes())
	}))
	defer memeServer.Close()
	onlyStrangers := &avatarDirectoryChannel{recordingChannel: &recordingChannel{}, members: map[string][]byte{"20002": target.Bytes()}}
	strangerRuntime := NewRuntime(BotConfig{ID: "qq", OwnerID: "10001"}, onlyStrangers, NewPluginManager(), nil, &stubReminderStore{}, nil, nil)
	memeEvent := MessageEvent{
		Kind: EventKindGroup, GroupID: "20005", UserID: "10001", MessageID: "m3",
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{"url": memeServer.URL + "/meme.jpg"}}},
	}
	if got := strangerRuntime.avatarMatchAnnotation(t.Context(), memeEvent); got != "" {
		t.Fatalf("没比中却附了一句：%q", got)
	}
}

// 自动匹配前先看比例：不是 1:1 的图直接放过，一张成员头像都不下载。群里的图绝大
// 多数走这条路，这道闸门就是这个功能的成本上限。
func TestAvatarAnnotationSkipsNonAvatarShapedImages(t *testing.T) {
	encode := func(width, height int) []byte {
		var buffer bytes.Buffer
		if err := jpeg.Encode(&buffer, patternedRect(width, height), &jpeg.Options{Quality: 85}); err != nil {
			t.Fatal(err)
		}
		return buffer.Bytes()
	}
	for _, item := range []struct {
		name          string
		width, height int
		want          bool
	}{
		{"正方形头像", 640, 640, true},
		{"差一点点的方图", 600, 597, true},
		{"横图", 1280, 720, false},
		{"竖图截屏", 720, 1280, false},
		{"5:4 的图（工具路径放行，自动路径不放）", 500, 400, false},
		{"太小的图标", 16, 16, false},
		{"几千像素的方形照片", 2048, 2048, false},
	} {
		if got := looksLikeAvatarImage(encode(item.width, item.height)); got != item.want {
			t.Fatalf("%s(%dx%d): looksLikeAvatarImage = %v, want %v", item.name, item.width, item.height, got, item.want)
		}
	}
	if looksLikeAvatarImage([]byte("not an image")) {
		t.Fatal("解不开的数据不该进入比对")
	}
}

// 同一张图在群里会被反复发，结论按内容缓存，不重复比全群头像。
func TestAvatarAnnotationCachesPerImageAndGroup(t *testing.T) {
	avatarAnnotationCache.Clear()
	t.Cleanup(func() { avatarAnnotationCache.Clear() })

	segment := MessageSegment{Type: "image", Data: map[string]string{imageContentSHA256Key: "sha-of-the-meme"}}
	key := avatarMatchAnnotationTestKey(segment)
	if _, ok := loadAvatarAnnotation(key, "group-a"); ok {
		t.Fatal("没存过就读到了")
	}

	storeAvatarAnnotation(key, "group-a", "【头像匹配】…")
	if got, ok := loadAvatarAnnotation(key, "group-a"); !ok || got == "" {
		t.Fatalf("命中的结论没被缓存：%q ok=%v", got, ok)
	}
	// 「没比中」也要记住，否则每发一次表情包都要再比一遍全群头像。
	storeAvatarAnnotation(key, "group-b", "")
	if got, ok := loadAvatarAnnotation(key, "group-b"); !ok || got != "" {
		t.Fatalf("未命中的结论没被缓存：%q ok=%v", got, ok)
	}
	// 同一张头像在另一个群里对应的可能是别人，不能跨群复用。
	if _, ok := loadAvatarAnnotation(key, "group-c"); ok {
		t.Fatal("结论被跨群复用了")
	}
}

func avatarMatchAnnotationTestKey(segment MessageSegment) string {
	return avatarAnnotationCacheKey(segment, nil)
}

// 比一次头像要遍历整群。走 MemberAvatarChannel 的平台（Telegram）每个成员的头像
// 都是两次 Bot API 调用，不缓存就是「每发一张方图就把全群头像重下一遍」。
func TestMemberAvatarsAreFetchedOncePerDay(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())

	var target, other bytes.Buffer
	if err := png.Encode(&target, patternedAvatar(256)); err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(&other, unrelatedAvatar(256)); err != nil {
		t.Fatal(err)
	}
	var posted bytes.Buffer
	if err := jpeg.Encode(&posted, patternedAvatar(320), &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(posted.Bytes())
	}))
	defer server.Close()

	channel := &avatarDirectoryChannel{
		recordingChannel: &recordingChannel{},
		members:          map[string][]byte{"20002": target.Bytes(), "20003": other.Bytes()},
	}
	runtime := NewRuntime(BotConfig{ID: "tg", Platform: PlatformTelegram, OwnerID: "10001"}, channel, NewPluginManager(), nil, &stubReminderStore{}, nil, nil)
	event := MessageEvent{
		Kind: EventKindGroup, GroupID: "20005", UserID: "10001", MessageID: "m1", Platform: PlatformTelegram,
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{"url": server.URL + "/posted.jpg"}}},
	}

	first, err := runtime.matchCurrentGroupMemberAvatar(t.Context(), event)
	if err != nil || !first.Matched {
		t.Fatalf("第一次就没比出来：%+v err=%v", first, err)
	}
	afterFirst := channel.avatarCalls.Load()
	if afterFirst != 2 {
		t.Fatalf("第一次应当每个成员各取一次头像，实际 %d 次", afterFirst)
	}

	second, err := runtime.matchCurrentGroupMemberAvatar(t.Context(), event)
	if err != nil || !second.Matched {
		t.Fatalf("第二次没比出来：%+v err=%v", second, err)
	}
	if extra := channel.avatarCalls.Load() - afterFirst; extra != 0 {
		t.Fatalf("同一天又去取了 %d 次头像，缓存没生效", extra)
	}
}

// avatarURLDirectoryChannel 只提供名单，不提供头像字节——头像按 URL 取，
// QQ 走的就是这条路。
type avatarURLDirectoryChannel struct {
	*recordingChannel
	avatarURLs map[string]string
}

func (c *avatarURLDirectoryChannel) GroupMember(_ context.Context, groupID, userID string) (OneBotGroupMemberInfo, error) {
	url, ok := c.avatarURLs[userID]
	if !ok {
		return OneBotGroupMemberInfo{}, fmt.Errorf("不是群成员")
	}
	return OneBotGroupMemberInfo{GroupID: groupID, UserID: userID, Nickname: "成员" + userID, AvatarURL: url, MembershipVerified: true}, nil
}

func (c *avatarURLDirectoryChannel) GroupMembers(_ context.Context, groupID string) (GroupMemberDirectory, error) {
	members := make([]OneBotGroupMemberInfo, 0, len(c.avatarURLs))
	for userID, url := range c.avatarURLs {
		members = append(members, OneBotGroupMemberInfo{GroupID: groupID, UserID: userID, Nickname: "成员" + userID, AvatarURL: url, MembershipVerified: true})
	}
	sort.Slice(members, func(left, right int) bool { return members[left].UserID < members[right].UserID })
	return GroupMemberDirectory{Members: members, Complete: true, Total: len(members), TotalKnown: true}, nil
}

// 按 URL 取头像的平台（QQ）同样一天只下一次：URL 上挂了 diana_avatar_day，
// 落进磁盘媒体缓存。
func TestMemberAvatarURLsAreDownloadedOncePerDay(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())

	var downloads atomic.Int64
	avatars := map[string]image.Image{"20002": patternedAvatar(256), "20003": unrelatedAvatar(256)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who := strings.TrimPrefix(r.URL.Path, "/avatar/")
		img, ok := avatars[who]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		downloads.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_ = png.Encode(w, img)
	}))
	defer server.Close()

	var posted bytes.Buffer
	if err := jpeg.Encode(&posted, patternedAvatar(320), &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
	postedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(posted.Bytes())
	}))
	defer postedServer.Close()

	channel := &avatarURLDirectoryChannel{recordingChannel: &recordingChannel{}, avatarURLs: map[string]string{
		"20002": server.URL + "/avatar/20002",
		"20003": server.URL + "/avatar/20003",
	}}
	runtime := NewRuntime(BotConfig{ID: "qq", OwnerID: "10001"}, channel, NewPluginManager(), nil, &stubReminderStore{}, nil, nil)
	event := MessageEvent{
		Kind: EventKindGroup, GroupID: "20005", UserID: "10001", MessageID: "m1",
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{"url": postedServer.URL + "/posted.jpg"}}},
	}

	match, err := runtime.matchCurrentGroupMemberAvatar(t.Context(), event)
	if err != nil || match.UserID != "20002" {
		t.Fatalf("第一次没认出来：%+v err=%v", match, err)
	}
	if got := downloads.Load(); got != 2 {
		t.Fatalf("第一次应当每个成员各下一次，实际 %d 次", got)
	}

	if _, err := runtime.matchCurrentGroupMemberAvatar(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	if got := downloads.Load(); got != 2 {
		t.Fatalf("同一天重复下载了：总计 %d 次", got)
	}
}
