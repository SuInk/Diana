// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/SuInk/diana/model/applog"
)

// 消息互通：把两个会话的消息互相搬过去。
//
// 一条链路只连两端。多端全互通看着更灵活，但配了三四个端点之后，「谁说的话会
// 出现在哪」就说不清了，出问题也没法只停掉其中一段；两两成对能叠出同样的拓扑，
// 还能单独开关、单独排错。
const (
	MessageRelayKindGroup   = "group"
	MessageRelayKindPrivate = "private"
	// messageRelayTimeout 是一次转发的时间预算。转发是旁路，不该把目标平台的
	// 网络延迟叠到本轮回复上。
	messageRelayTimeout = 15 * time.Second
)

// MessageRelayEndpoint 是互通链路的一端：某台机器人上的某个群聊或某个人。
type MessageRelayEndpoint struct {
	ProfileID string `json:"profile_id"`
	Platform  string `json:"platform,omitempty"`
	Kind      string `json:"kind"`
	TargetID  string `json:"target_id"`
}

// MessageRelayPair 是一对互相转发的会话。
type MessageRelayPair struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Enabled bool   `json:"enabled"`
	// Endpoints 恒为两端。少于两端的链路没有意义，多于两端的会在规范化时截断。
	Endpoints []MessageRelayEndpoint `json:"endpoints"`
}

func (e MessageRelayEndpoint) normalized() MessageRelayEndpoint {
	e.ProfileID = strings.TrimSpace(e.ProfileID)
	e.Platform = NormalizePlatformID(e.Platform)
	e.TargetID = strings.TrimSpace(e.TargetID)
	if e.Kind = strings.TrimSpace(e.Kind); e.Kind != MessageRelayKindPrivate {
		e.Kind = MessageRelayKindGroup
	}
	return e
}

func (e MessageRelayEndpoint) valid() bool {
	return e.ProfileID != "" && e.Platform != "" && e.TargetID != ""
}

// key 用来判断两端是不是同一个会话，也用来给转发去重。
func (e MessageRelayEndpoint) key() string {
	return e.ProfileID + "|" + e.Platform + "|" + e.Kind + "|" + e.TargetID
}

// matches 判断这条消息是不是从这一端进来的。
func (e MessageRelayEndpoint) matches(event MessageEvent) bool {
	if strings.TrimSpace(event.ProfileID) != e.ProfileID || NormalizePlatformID(event.Platform) != e.Platform {
		return false
	}
	if string(event.Kind) != e.Kind {
		return false
	}
	target := strings.TrimSpace(event.UserID)
	if event.Kind == EventKindGroup {
		target = strings.TrimSpace(event.GroupID)
	}
	return target == e.TargetID
}

// NormalizeMessageRelays 规范化互通配置：补 ID、丢掉配不全的链路。
//
// 两端相同的链路会被丢掉——那是把消息发回它自己来的地方，只会在群里刷出一份
// 自己的复读。
func NormalizeMessageRelays(pairs []MessageRelayPair) []MessageRelayPair {
	out := make([]MessageRelayPair, 0, len(pairs))
	seen := make(map[string]struct{}, len(pairs))
	for _, pair := range pairs {
		endpoints := make([]MessageRelayEndpoint, 0, 2)
		for _, endpoint := range pair.Endpoints {
			if endpoint = endpoint.normalized(); endpoint.valid() {
				endpoints = append(endpoints, endpoint)
			}
			if len(endpoints) == 2 {
				break
			}
		}
		if len(endpoints) != 2 || endpoints[0].key() == endpoints[1].key() {
			continue
		}
		pair.Endpoints = endpoints
		pair.Name = strings.TrimSpace(pair.Name)
		if pair.ID = strings.TrimSpace(pair.ID); pair.ID == "" {
			pair.ID = uuid.NewString()
		}
		if _, duplicate := seen[pair.ID]; duplicate {
			pair.ID = uuid.NewString()
		}
		seen[pair.ID] = struct{}{}
		out = append(out, pair)
	}
	return out
}

// messageRelaysWithoutProfile 去掉牵扯到某台机器人的所有链路。
func messageRelaysWithoutProfile(pairs []MessageRelayPair, profileID string) []MessageRelayPair {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" || len(pairs) == 0 {
		return pairs
	}
	out := make([]MessageRelayPair, 0, len(pairs))
	for _, pair := range pairs {
		involved := false
		for _, endpoint := range pair.Endpoints {
			if strings.TrimSpace(endpoint.ProfileID) == profileID {
				involved = true
				break
			}
		}
		if !involved {
			out = append(out, pair)
		}
	}
	return out
}

// messageRelayTargets 找出这条消息该被搬到哪些会话去。
//
// 同一个会话可能出现在好几条链路里，去重一次，免得同一条消息在那边刷出两遍。
func messageRelayTargets(pairs []MessageRelayPair, event MessageEvent) []MessageRelayEndpoint {
	var targets []MessageRelayEndpoint
	sent := make(map[string]struct{})
	for _, pair := range pairs {
		if !pair.Enabled || len(pair.Endpoints) != 2 {
			continue
		}
		for index, endpoint := range pair.Endpoints {
			if !endpoint.matches(event) {
				continue
			}
			other := pair.Endpoints[1-index]
			if _, done := sent[other.key()]; done {
				continue
			}
			sent[other.key()] = struct{}{}
			targets = append(targets, other)
		}
	}
	return targets
}

// messageRelayText 拼转发正文。带上来源平台和发言人，否则那边看到的是一句
// 没头没尾的话。
func messageRelayText(event MessageEvent) (text string, images, videos []string) {
	text = strings.TrimSpace(PlainText(event.Segments))
	if text == "" {
		text = strings.TrimSpace(event.RawMessage)
	}
	images, videos = relayMedia(event.Segments)
	if text == "" && len(images) == 0 && len(videos) == 0 {
		return "", nil, nil
	}
	prefix := "【" + relayPlatformLabel(event.Platform) + " · " + event.SenderNameOrID() + "】"
	if text == "" {
		return prefix, images, videos
	}
	return prefix + "\n" + text, images, videos
}

func relayMedia(segments []MessageSegment) (images, videos []string) {
	for _, segment := range segments {
		source := firstNonEmpty(segment.Data["cached_file"], segment.Data["url"], segment.Data["file"])
		if strings.TrimSpace(source) == "" {
			continue
		}
		switch segment.Type {
		case "image":
			images = append(images, source)
		case "video":
			videos = append(videos, source)
		}
	}
	return
}

func relayPlatformLabel(platform string) string {
	switch NormalizePlatformID(platform) {
	case PlatformTelegram:
		return "Telegram"
	case PlatformOneBotV11:
		return "QQ"
	case PlatformQQOfficial:
		return "QQ 官方"
	case PlatformFeishu:
		return "飞书"
	case PlatformDingTalk:
		return "钉钉"
	case PlatformWeCom:
		return "企业微信"
	}
	return strings.TrimSpace(platform)
}

// messageRelays 返回当前生效的互通配置。
func (r *Runtime) messageRelays() []MessageRelayPair {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.relayPairs
}

// relayInboundEvent 把消息搬到互通的另一端。
//
// 它跑在回复判断之前：就算 planner 最后决定不回复，原消息也该被搬过去。
func (r *Runtime) relayInboundEvent(ctx context.Context, event MessageEvent) {
	if event.Kind != EventKindGroup && event.Kind != EventKindPrivate {
		return
	}
	// 多数平台不会把机器人自己发的消息回推，但 OneBot 会。转发自己的回声会让
	// 两端互相放大，永远停不下来。
	if selfID := strings.TrimSpace(event.SelfID); selfID != "" && strings.TrimSpace(event.UserID) == selfID {
		return
	}
	targets := messageRelayTargets(r.messageRelays(), event)
	if len(targets) == 0 {
		return
	}
	text, images, videos := messageRelayText(event)
	if text == "" {
		return
	}
	r.mu.RLock()
	channel := r.channel
	r.mu.RUnlock()
	if channel == nil {
		return
	}
	for _, target := range targets {
		msg := OutgoingMessage{Platform: target.Platform, ProfileID: target.ProfileID, Text: text, ImageURLs: images, VideoURLs: videos}
		if target.Kind == MessageRelayKindGroup {
			msg.GroupID = target.TargetID
		} else {
			msg.UserID = target.TargetID
		}
		if err := channel.Send(ctx, msg); err != nil {
			r.recordMessageRelayFailure(event, target, err)
		}
	}
}

// recordMessageRelayFailure 把转发失败记进审计日志。转发是旁路，失败不该打断
// 本轮回复，但也不能悄悄咽掉——不然那边的人只会觉得消息莫名其妙丢了。
func (r *Runtime) recordMessageRelayFailure(event MessageEvent, target MessageRelayEndpoint, cause error) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	logCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = writer.AppendLog(logCtx, applog.Entry{
		Kind:    applog.KindError,
		Level:   applog.LevelError,
		Action:  "diana.message_relay_failed",
		Message: "消息互通转发失败",
		Detail:  cause.Error(),
		Actor:   oneBotEventActor(event),
		Target:  target.TargetID,
		Metadata: map[string]any{
			"source_platform": NormalizePlatformID(event.Platform),
			"source_kind":     string(event.Kind),
			"target_profile":  target.ProfileID,
			"target_platform": target.Platform,
			"target_kind":     target.Kind,
			"target_id":       target.TargetID,
		},
	})
}
