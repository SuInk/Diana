// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/llm"
)

// 相邻媒体合并原本是纯时间规则：同一发送者、15 秒内、最近一条还没被消费的媒体，
// 直接并进这条文本。它对「发张图，紧接着问这是什么」很准，但对下面这种就不是：
//
//	群主：  这是我们的 API Key ……
//	群友：  [一张表情]          ← 对上一条的反应
//	群友：  这是什么            ← 问的还是 API Key 那条
//
// 两种情形在队列层完全同形——同一发送者、几秒内、媒体在前文本在后。分辨它们要
// 的不是更细的时间窗，而是「这句话到底在指谁」，那是语义问题。
//
// 所以合并前先判一次：只在真正有歧义时问模型，其余情况按规则直接走，不额外付费。

const (
	// mediaReferenceMinimumConfidence 是采纳「指的就是这张图」的最低置信度。
	//
	// 判错的两个方向代价不对称：错并了，模型会拿着一张无关的图笃定作答，而且媒体
	// 那条任务已经被注销、不可逆；没并成，图还在历史里，agent 模式下模型自己能取。
	// 所以拿不准就不并。
	mediaReferenceMinimumConfidence = 0.6
	// mediaReferenceNeighborLimit 是喂给判断器的近邻消息条数。
	mediaReferenceNeighborLimit = 6
	// mediaReferenceCompetingWindow 是往前找「竞争指代对象」的时间范围。超出这个
	// 范围的别人的发言，已经不像是这句话在指的东西了。
	mediaReferenceCompetingWindow = 3 * time.Minute
)

// explicitMediaReferencePattern 匹配明确指向媒体本身的说法。命中就不用问模型了：
// 用户已经说清楚在讲图/视频/表情。
var explicitMediaReferencePattern = regexp.MustCompile(
	`(这|那|上面|刚(才|刚)?(发)?)?\s*(张|个|条|段)?\s*(图片?|照片|截图|表情|动图|视频|录音|语音|文件|附件)` +
		`|图里|图中|画里|画面(里|中)?|视频里|里面(这|那)?(个|张)?`)

// mediaReferenceDecision 是判断器的结论。
type mediaReferenceDecision struct {
	RefersToMedia bool    `json:"refers_to_media"`
	Confidence    float64 `json:"confidence"`
	Reason        string  `json:"reason,omitempty"`
}

// mediaReferenceOutcome 记录这次是怎么定下来的，供日志和排查用。
type mediaReferenceOutcome struct {
	Merge      bool
	Method     string
	Confidence float64
	Reason     string
}

// shouldMergeAdjacentMedia 决定相邻媒体要不要并进这条文本。
//
// 三档，从便宜到贵：
//  1. 文本明说了在讲图/表情 → 直接并，没有歧义可言
//  2. 附近没有别人发的、能被指的消息 → 直接并，这正是这个功能存在的理由
//  3. 两者都不满足 → 有歧义，问模型
func (r *Runtime) shouldMergeAdjacentMedia(ctx context.Context, event MessageEvent, text string, media []MessageEvent) mediaReferenceOutcome {
	if len(media) == 0 {
		return mediaReferenceOutcome{Merge: false, Method: "no_candidate"}
	}
	if explicitMediaReferencePattern.MatchString(strings.TrimSpace(text)) {
		return mediaReferenceOutcome{Merge: true, Method: "explicit_media_reference", Confidence: 1}
	}
	competing := r.competingReferents(event, media)
	if len(competing) == 0 {
		return mediaReferenceOutcome{Merge: true, Method: "no_competing_referent", Confidence: 1}
	}
	decision, err := r.judgeMediaReference(ctx, event, text, media, competing)
	if err != nil {
		// 判不出来就不并：错并是不可逆的，不并只是少贴一张图。
		return mediaReferenceOutcome{Merge: false, Method: "llm_unavailable", Reason: err.Error()}
	}
	if !decision.RefersToMedia || decision.Confidence < mediaReferenceMinimumConfidence {
		return mediaReferenceOutcome{
			Merge:      false,
			Method:     "llm_declined",
			Confidence: decision.Confidence,
			Reason:     decision.Reason,
		}
	}
	return mediaReferenceOutcome{
		Merge:      true,
		Method:     "llm_confirmed",
		Confidence: decision.Confidence,
		Reason:     decision.Reason,
	}
}

// competingReferents 找出「这句话可能在指的别的东西」：媒体之前、由别人发出、
// 有实际内容的消息。没有这种消息时就没有歧义——用户只可能在说自己刚发的那张。
func (r *Runtime) competingReferents(event MessageEvent, media []MessageEvent) []MessageEvent {
	history := r.contextHistory(event)
	if len(history) == 0 {
		return nil
	}
	sender := strings.TrimSpace(event.UserID)
	earliestMedia := int64(0)
	for _, item := range media {
		if earliestMedia == 0 || (item.Time > 0 && item.Time < earliestMedia) {
			earliestMedia = item.Time
		}
	}
	if earliestMedia <= 0 {
		earliestMedia = event.Time
	}
	mediaIDs := make(map[string]bool, len(media))
	for _, item := range media {
		if id := strings.TrimSpace(item.MessageID); id != "" {
			mediaIDs[id] = true
		}
	}
	cutoff := earliestMedia - int64(mediaReferenceCompetingWindow/time.Second)
	competing := make([]MessageEvent, 0, mediaReferenceNeighborLimit)
	for index := len(history) - 1; index >= 0 && len(competing) < mediaReferenceNeighborLimit; index-- {
		item := history[index]
		id := strings.TrimSpace(item.MessageID)
		if id == "" || id == strings.TrimSpace(event.MessageID) || mediaIDs[id] {
			continue
		}
		// 只看媒体之前的：之后来的消息不可能是这句话的指代对象。
		if item.Time > earliestMedia || item.Time < cutoff {
			continue
		}
		// 自己发的不算竞争对象——「这」指向自己刚说过的话时，并不并图都不影响
		// 模型读到那句话，它就在上下文里。
		if strings.TrimSpace(item.UserID) == sender {
			continue
		}
		if strings.TrimSpace(historyPlainText(item)) == "" && !segmentsHaveReferenceContent(item.Segments) {
			continue
		}
		competing = append(competing, item)
	}
	return competing
}

const mediaReferenceSystemPrompt = `你在判断一句话指的是不是紧挨着它的那条媒体消息。

场景：同一个人先发了媒体（图片／表情／视频／语音／文件），几秒后又发了一句话。
这句话有两种可能：
A. 在问／评论他自己刚发的那条媒体
B. 那条媒体只是他对更早消息的一个反应（比如甩个表情），这句话问的是更早的那条消息

判断依据：
1. media 是候选媒体，is_sticker 表示它看起来是表情/贴纸——表情更常是反应，不是话题本身。
2. competing_messages 是媒体之前、由别人发出的消息，按由近到远排列。它们是 B 的候选对象。
3. 结合这句话的措辞：泛指的短句（“这是什么”“什么意思”“看不懂”）两种都可能，要靠上下文定；
   如果更早那条消息本身就令人费解、值得追问（例如一串密钥、一段报错、一个陌生链接），
   而媒体只是一张表情，那多半是 B。
4. 用户在问自己刚发的照片、截图、文件时是 A；没人会问自己刚甩的表情是什么。
5. 拿不准就给低置信度，不要硬选。

只输出 JSON：{"refers_to_media": true/false, "confidence": 0~1, "reason": "简短判据"}`

func (r *Runtime) judgeMediaReference(ctx context.Context, event MessageEvent, text string, media, competing []MessageEvent) (mediaReferenceDecision, error) {
	ctx = withLLMUsagePurpose(ctx, "inbound_media_reference")
	payload, err := json.Marshal(map[string]any{
		"sender":             event.SenderNameOrID(),
		"text":               strings.TrimSpace(text),
		"media":              mediaReferenceCandidates(media, event.Time),
		"competing_messages": mediaReferenceNeighbors(competing, event.Time),
	})
	if err != nil {
		return mediaReferenceDecision{}, err
	}
	callCtx, cancel := context.WithTimeout(ctx, semanticRouteTimeout)
	defer cancel()
	raw, err := r.runLLMRouterProvider(callCtx, func(client LLMProvider) (string, error) {
		resp, err := client.Generate(callCtx, llm.GenerateRequest{Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: mediaReferenceSystemPrompt},
			{Role: llm.RoleUser, Content: string(payload)},
		}})
		if err != nil {
			return "", err
		}
		return resp.Text, nil
	})
	if err != nil {
		return mediaReferenceDecision{}, err
	}
	decision, ok := parseMediaReferenceDecision(raw)
	if !ok {
		return mediaReferenceDecision{}, errMediaReferenceUnparsable
	}
	return decision, nil
}

var errMediaReferenceUnparsable = errors.New("diana: media reference decision is not valid JSON")

// parseMediaReferenceDecision 和语义指代那边同一个口径：容忍代码围栏和前后废话，
// 只取最外层的那对花括号。
func parseMediaReferenceDecision(raw string) (mediaReferenceDecision, bool) {
	raw = strings.TrimSpace(stripJSONCodeFence(raw))
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end < start {
		return mediaReferenceDecision{}, false
	}
	var decision mediaReferenceDecision
	if err := json.Unmarshal([]byte(raw[start:end+1]), &decision); err != nil {
		return mediaReferenceDecision{}, false
	}
	return decision, true
}

type mediaReferenceCandidate struct {
	Sender     string   `json:"sender,omitempty"`
	Kinds      []string `json:"kinds,omitempty"`
	IsSticker  bool     `json:"is_sticker,omitempty"`
	AgeSeconds int64    `json:"age_seconds"`
}

func mediaReferenceCandidates(media []MessageEvent, anchor int64) []mediaReferenceCandidate {
	out := make([]mediaReferenceCandidate, 0, len(media))
	for _, item := range media {
		candidate := mediaReferenceCandidate{
			Sender:    strings.TrimSpace(item.SenderNameOrID()),
			IsSticker: eventLooksLikeSticker(item),
		}
		if anchor > 0 && item.Time > 0 {
			candidate.AgeSeconds = anchor - item.Time
		}
		seen := map[string]bool{}
		for _, segment := range item.Segments {
			switch segment.Type {
			case "image", "video", "record", "file":
				if !seen[segment.Type] {
					seen[segment.Type] = true
					candidate.Kinds = append(candidate.Kinds, segment.Type)
				}
			}
		}
		out = append(out, candidate)
	}
	return out
}

type mediaReferenceNeighbor struct {
	Sender     string `json:"sender,omitempty"`
	Text       string `json:"text,omitempty"`
	HasMedia   bool   `json:"has_media,omitempty"`
	AgeSeconds int64  `json:"age_seconds"`
}

func mediaReferenceNeighbors(items []MessageEvent, anchor int64) []mediaReferenceNeighbor {
	out := make([]mediaReferenceNeighbor, 0, len(items))
	for _, item := range items {
		neighbor := mediaReferenceNeighbor{
			Sender:   strings.TrimSpace(item.SenderNameOrID()),
			Text:     truncateRunesFromStart(historyPlainText(item), 280),
			HasMedia: segmentsHaveReferenceContent(item.Segments),
		}
		if anchor > 0 && item.Time > 0 {
			neighbor.AgeSeconds = anchor - item.Time
		}
		out = append(out, neighbor)
	}
	return out
}

// eventLooksLikeSticker 判断这条媒体像不像表情/贴纸。
//
// OneBot 各实现口径不一：商城表情走 mface，收藏和小表情多是 image 带 sub_type。
// 认不出来就当普通图片——这只是给判断器的一个线索，不是硬规则。
func eventLooksLikeSticker(event MessageEvent) bool {
	for _, segment := range event.Segments {
		switch segment.Type {
		case "mface", "face":
			return true
		case "image":
			if subType := strings.TrimSpace(segment.Data["sub_type"]); subType != "" && subType != "0" {
				return true
			}
		}
	}
	return false
}

// recordInboundMediaReference 把「为什么并/为什么没并」写进日志。
// 这个判断本身有歧义，出问题时必须能倒查是哪一档、置信度多少。
func (r *Runtime) recordInboundMediaReference(ctx context.Context, turnID string, event MessageEvent, media []MessageEvent, outcome mediaReferenceOutcome) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	mediaIDs := make([]string, 0, len(media))
	for _, item := range media {
		mediaIDs = appendUniqueStrings(mediaIDs, strings.TrimSpace(item.MessageID))
	}
	message := "相邻媒体未并入本轮，按独立消息处理"
	if outcome.Merge {
		message = "相邻媒体已并入本轮"
	}
	_ = writer.AppendLog(ctx, applog.Entry{
		Kind:    applog.KindOperation,
		Level:   applog.LevelInfo,
		Action:  "diana.inbound.media_reference",
		Message: message,
		Actor:   oneBotEventActor(event),
		Target:  strings.TrimSpace(event.MessageID),
		Detail:  outcome.Reason,
		Metadata: map[string]any{
			"turn_id":            turnID,
			"trigger_message_id": event.MessageID,
			"media_message_ids":  mediaIDs,
			"merged":             outcome.Merge,
			"association_method": outcome.Method,
			"confidence":         outcome.Confidence,
		},
	})
}
