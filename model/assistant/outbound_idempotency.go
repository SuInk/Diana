// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// OutboundStepLedger 记录一次入站事件在处理过程中已经成功送达的出站步骤。
// 入站队列失败后会整条重跑：重跑时已经发出去的文字分片、图片和视频不应该再发
// 一次，否则用户会收到重复消息（典型表现是同一张图片被反复发送）。
type OutboundStepLedger interface {
	// OutboundStepDelivered 查询某个步骤是否已经送达，并返回当时的消息 ID。
	OutboundStepDelivered(ctx context.Context, turnID, stepKey string) (messageID string, delivered bool, err error)
	// RecordOutboundStep 在步骤真正送达后登记，供后续重跑跳过。
	RecordOutboundStep(ctx context.Context, turnID, stepKey, messageID string) error
	// ClearOutboundSteps 在这条入站事件不再需要重跑时清理账本。
	ClearOutboundSteps(ctx context.Context, turnID string) error
}

type outboundTurnKey struct{}

// outboundTurn 把「同一条入站事件的这一轮处理」串起来。seq 让步骤键带上序号，
// 于是同一轮里两条内容完全相同的消息不会互相误判为重复。
type outboundTurn struct {
	id  string
	seq atomic.Int64
	// 下面几个计数器记录这一轮实际发出去的东西。事件详情里的「回复结果」只有一个
	// 文本字段，装不下「发了一张转发卡片加九张图」；发媒体不发文字时它甚至是空的，
	// 前端于是显示「未保存回复正文」——可东西明明发出去了。
	//
	// 逐条投递路径去补是补不完的（转发卡片、resolver 散装兜底、插件直发各走各的），
	// 所以在两个真正的收口处记账：普通消息在 sendOutgoingWithResult，合并转发在
	// sendForwardNodesWithResult。
	sentMessages atomic.Int64
	sentImages   atomic.Int64
	sentVideos   atomic.Int64
	sentAudios   atomic.Int64
	forwardCards atomic.Int64
	forwardNodes atomic.Int64
	// media 记下发出去的每张图是哪张。只有数量的话，事件里只能看到「发了 1 张
	// 图片」，说不出发的是哪个表情包。
	mediaMu sync.Mutex
	media   []OutboundMedia
}

// maxOutboundMediaPerTurn 限制一轮记多少张图的来源；超出的只计数，不记来源。
const maxOutboundMediaPerTurn = 16

// OutboundMedia 是一轮里发出去的一张图。Source 是发送时用的本地路径或 URL，
// 只落库、供控制台按序号取图，不下发给前端；内联的 data URI 不存，Inline 标出来。
type OutboundMedia struct {
	Kind   string `json:"kind"`
	Label  string `json:"label,omitempty"`
	Source string `json:"source,omitempty"`
	Inline bool   `json:"inline,omitempty"`
}

// OutboundDelivery 是一轮回复实际发出去的内容概览。
type OutboundDelivery struct {
	Messages     int             `json:"messages,omitempty"`
	Images       int             `json:"images,omitempty"`
	Videos       int             `json:"videos,omitempty"`
	Audios       int             `json:"audios,omitempty"`
	ForwardCards int             `json:"forward_cards,omitempty"`
	ForwardNodes int             `json:"forward_nodes,omitempty"`
	Media        []OutboundMedia `json:"media,omitempty"`
}

// Empty 报告这一轮有没有发出任何东西。
func (d OutboundDelivery) Empty() bool {
	return d.Messages == 0 && d.Images == 0 && d.Videos == 0 &&
		d.Audios == 0 && d.ForwardCards == 0 && d.ForwardNodes == 0
}

func (t *outboundTurn) recordSentMessage(msg OutgoingMessage) {
	if t == nil {
		return
	}
	t.sentMessages.Add(1)
	images, videos, audios := outgoingMediaCounts(msg)
	if images > 0 {
		t.sentImages.Add(int64(images))
	}
	if videos > 0 {
		t.sentVideos.Add(int64(videos))
	}
	if audios > 0 {
		t.sentAudios.Add(int64(audios))
	}
	t.recordSentImages(msg)
}

func (t *outboundTurn) recordSentImages(msg OutgoingMessage) {
	var items []OutboundMedia
	for index, source := range msg.ImageURLs {
		label := ""
		if index < len(msg.ImageLabels) {
			label = strings.TrimSpace(msg.ImageLabels[index])
		}
		items = append(items, outboundImage(source, label))
	}
	for _, segment := range msg.Segments {
		if !strings.EqualFold(strings.TrimSpace(segment.Type), "image") {
			continue
		}
		source := firstNonEmpty(segment.Data["file"], segment.Data["url"], segment.Data["path"])
		items = append(items, outboundImage(source, segment.Data["summary"]))
	}
	if len(items) == 0 {
		return
	}
	t.mediaMu.Lock()
	defer t.mediaMu.Unlock()
	for _, item := range items {
		if len(t.media) >= maxOutboundMediaPerTurn {
			return
		}
		t.media = append(t.media, item)
	}
}

func outboundImage(source, label string) OutboundMedia {
	source = strings.TrimSpace(source)
	item := OutboundMedia{Kind: "image", Label: strings.TrimSpace(label)}
	// base64 动辄几百 KB，不往事件表里塞。
	if inlineImageSource(source) {
		item.Inline = true
		return item
	}
	item.Source = source
	return item
}

func (t *outboundTurn) recordSentForward(nodes int) {
	if t == nil {
		return
	}
	t.forwardCards.Add(1)
	if nodes > 0 {
		t.forwardNodes.Add(int64(nodes))
	}
}

func (t *outboundTurn) delivery() OutboundDelivery {
	if t == nil {
		return OutboundDelivery{}
	}
	return OutboundDelivery{
		Messages:     int(t.sentMessages.Load()),
		Images:       int(t.sentImages.Load()),
		Videos:       int(t.sentVideos.Load()),
		Audios:       int(t.sentAudios.Load()),
		ForwardCards: int(t.forwardCards.Load()),
		ForwardNodes: int(t.forwardNodes.Load()),
		Media:        t.mediaSnapshot(),
	}
}

func (t *outboundTurn) mediaSnapshot() []OutboundMedia {
	t.mediaMu.Lock()
	defer t.mediaMu.Unlock()
	return append([]OutboundMedia(nil), t.media...)
}

// outboundMediaCounts 数一条出站消息里的图片、视频和语音。URL 字段和 segment
// 两种写法都要认：插件用前者，Agent 工具和 CQ 码走后者。
func outgoingMediaCounts(msg OutgoingMessage) (images, videos, audios int) {
	images = len(msg.ImageURLs)
	videos = len(msg.VideoURLs)
	audios = len(msg.AudioURLs)
	for _, segment := range msg.Segments {
		switch strings.ToLower(strings.TrimSpace(segment.Type)) {
		case "image":
			images++
		case "video":
			videos++
		case "record":
			audios++
		}
	}
	return images, videos, audios
}

func withOutboundTurn(ctx context.Context, turnID string) context.Context {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return ctx
	}
	return context.WithValue(ctx, outboundTurnKey{}, &outboundTurn{id: turnID})
}

func outboundTurnFromContext(ctx context.Context) *outboundTurn {
	if ctx == nil {
		return nil
	}
	turn, _ := ctx.Value(outboundTurnKey{}).(*outboundTurn)
	return turn
}

// nextStepKey 返回本轮下一个步骤的键：序号 + 载荷指纹。两者都相同才算同一步，
// 因此重跑时内容发生变化（例如模型重新生成了不同的文字）不会被误跳过。
func (t *outboundTurn) nextStepKey(fingerprint string) string {
	if t == nil {
		return ""
	}
	return strconv.FormatInt(t.seq.Add(1), 10) + ":" + fingerprint
}

func outgoingMessageFingerprint(msg OutgoingMessage) string {
	parts := []string{
		"platform=" + msg.Platform,
		"profile=" + msg.ProfileID,
		"group=" + msg.GroupID,
		"user=" + msg.UserID,
		"text=" + msg.Text,
		"reply=" + msg.ReplyMessageID,
		"mention=" + msg.MentionUserID,
		"images=" + strings.Join(msg.ImageURLs, "|"),
		"image_album=" + strconv.FormatBool(msg.ImageAlbum),
		"videos=" + strings.Join(msg.VideoURLs, "|"),
		"audios=" + strings.Join(msg.AudioURLs, "|"),
	}
	for _, segment := range msg.Segments {
		parts = append(parts, "segment="+segment.Type+":"+segmentFingerprint(segment))
	}
	return fingerprintOf(parts...)
}

func segmentFingerprint(segment MessageSegment) string {
	keys := make([]string, 0, len(segment.Data))
	for key := range segment.Data {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+segment.Data[key])
	}
	return strings.Join(parts, ";")
}

func fingerprintOf(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:16])
}

// outboundStepLedger 返回当前配置的账本；入站存储没有实现时返回 nil，调用方
// 退回「每次都真的发送」的旧行为。
func (r *Runtime) outboundStepLedger() OutboundStepLedger {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ledger, _ := r.inboundStore.(OutboundStepLedger)
	return ledger
}

// claimOutboundStep 在真正调用发送接口前判断这一步是否已经送达过。
// 返回的 stepKey 为空表示本次不参与幂等（没有 turn 或没有账本）。
func (r *Runtime) claimOutboundStep(ctx context.Context, fingerprint string) (stepKey, deliveredMessageID string, delivered bool) {
	turn := outboundTurnFromContext(ctx)
	if turn == nil {
		return "", "", false
	}
	ledger := r.outboundStepLedger()
	if ledger == nil {
		return "", "", false
	}
	stepKey = turn.nextStepKey(fingerprint)
	lookupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	messageID, ok, err := ledger.OutboundStepDelivered(lookupCtx, turn.id, stepKey)
	if err != nil {
		log.Printf("diana outbound step lookup failed: %v", err)
		return stepKey, "", false
	}
	return stepKey, messageID, ok
}

func (r *Runtime) recordOutboundStep(ctx context.Context, stepKey, messageID string) {
	if strings.TrimSpace(stepKey) == "" {
		return
	}
	turn := outboundTurnFromContext(ctx)
	if turn == nil {
		return
	}
	ledger := r.outboundStepLedger()
	if ledger == nil {
		return
	}
	recordCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := ledger.RecordOutboundStep(recordCtx, turn.id, stepKey, messageID); err != nil {
		log.Printf("diana outbound step record failed: %v", err)
	}
}

// clearOutboundSteps 在入站事件走到终态后清理账本。
func (r *Runtime) clearOutboundSteps(turnID string) {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return
	}
	ledger := r.outboundStepLedger()
	if ledger == nil {
		return
	}
	clearCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := ledger.ClearOutboundSteps(clearCtx, turnID); err != nil {
		log.Printf("diana outbound step cleanup failed: %v", err)
	}
}

// replayedOutboundResult 构造一个「这步之前已经发过」的返回值，让调用方拿到
// 与首次发送一致的消息 ID。
func replayedOutboundResult(messageID string) map[string]any {
	if strings.TrimSpace(messageID) == "" {
		return map[string]any{}
	}
	return map[string]any{"message_id": messageID}
}
