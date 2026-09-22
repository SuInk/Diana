// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// 机器人说错话之后只能再发一条「当我没说」，错的那条还挂在群里——看到的人多半只看
// 到第一条。这里给出真正的撤回，但只撤回机器人自己发出的那一条：
//
//   - 目标必须是本会话历史里 Outbound=true 的事件，且带平台返回的真实消息 ID。
//     按文本猜、按序号猜都不行：猜错就是删掉别人的话。
//   - 本地兜底 ID（local-out-…）撤不了。那是平台没回消息 ID 时写进历史占位用的，
//     拿它去调接口只会删错或报一个看不懂的错。
//   - 不支持撤回的平台、以及平台自己的时限和权限限制，都原样返回失败原因，不吞。
const platformOpRecall = "recall"

// localOutboundIDPrefix 是平台没有返回消息 ID 时的占位前缀，见 outgoingHistoryEvent。
const localOutboundIDPrefix = "local-out-"

// recallableOutboundMessageID 判断一条历史事件能不能拿去撤回。
func recallableOutboundMessageID(event MessageEvent) string {
	if !event.Outbound {
		return ""
	}
	id := strings.TrimSpace(event.MessageID)
	if id == "" || strings.HasPrefix(id, localOutboundIDPrefix) {
		return ""
	}
	return id
}

func platformSupportsRecall(platform string) bool {
	switch NormalizePlatformID(platform) {
	case PlatformOneBotV11, PlatformTelegram:
		return true
	}
	return false
}

// resolveRecallTarget 在本会话历史里定位要撤回的消息。
//
// messageID 为空时取机器人最近发出的一条。给了 ID 就必须精确命中，命中的还必须是
// 机器人自己发的——命中别人的消息一律拒绝，这是这个能力的边界，不靠提示词守。
func (r *Runtime) resolveRecallTarget(event MessageEvent, messageID string) (MessageEvent, error) {
	if r == nil {
		return MessageEvent{}, fmt.Errorf("运行时不可用")
	}
	session := sessionKey(event)
	r.mu.RLock()
	history := append([]MessageEvent(nil), r.history[session]...)
	r.mu.RUnlock()

	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		for i := len(history) - 1; i >= 0; i-- {
			if recallableOutboundMessageID(history[i]) != "" {
				return history[i], nil
			}
		}
		return MessageEvent{}, fmt.Errorf("本次会话里没有可撤回的消息：要么我还没发过话，要么平台没有返回消息 ID")
	}
	for i := len(history) - 1; i >= 0; i-- {
		if strings.TrimSpace(history[i].MessageID) != messageID {
			continue
		}
		if !history[i].Outbound {
			return MessageEvent{}, fmt.Errorf("这条不是我发的，只能撤回我自己发出的消息")
		}
		if recallableOutboundMessageID(history[i]) == "" {
			return MessageEvent{}, fmt.Errorf("这条消息没有平台消息 ID，撤不了")
		}
		return history[i], nil
	}
	return MessageEvent{}, fmt.Errorf("本次会话里找不到消息 %s：message_id 必须取自我自己发出的消息，不要按内容猜。%s", messageID, recallCandidateHint(history))
}

// recallCandidateHint 在 message_id 没命中时，把本会话里还能撤回的自家消息列回去。
// 模型手上没有消息 ID 清单，不给候选它只会再编一个。
func recallCandidateHint(history []MessageEvent) string {
	const maxCandidates = 5
	var lines []string
	for i := len(history) - 1; i >= 0 && len(lines) < maxCandidates; i-- {
		id := recallableOutboundMessageID(history[i])
		if id == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s（%s）", id, summarizeRecallCandidate(history[i])))
	}
	if len(lines) == 0 {
		return "本会话里目前没有可撤回的自家消息"
	}
	return "可撤回的是：" + strings.Join(lines, "、")
}

func summarizeRecallCandidate(event MessageEvent) string {
	text := strings.TrimSpace(PlainText(event.Segments))
	if text == "" {
		text = strings.TrimSpace(event.RawMessage)
	}
	text = strings.Join(strings.Fields(text), " ")
	const maxRunes = 20
	runes := []rune(text)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "…"
	}
	if text == "" {
		return "无文本内容"
	}
	return text
}

// deletePlatformMessage 把撤回映射到各平台的原生动作。
func (r *Runtime) deletePlatformMessage(ctx context.Context, event MessageEvent, messageID string) (map[string]any, error) {
	switch NormalizePlatformID(r.currentPlatform(event)) {
	case PlatformOneBotV11:
		return r.callOneBotAPIForEvent(ctx, event, "delete_msg", map[string]any{"message_id": oneBotIDParam(messageID)})
	case PlatformTelegram:
		chatID := firstNonEmpty(strings.TrimSpace(event.GroupID), strings.TrimSpace(event.UserID))
		if chatID == "" {
			return nil, fmt.Errorf("缺少会话 ID")
		}
		return r.callPlatformAPIForEvent(ctx, event, "deleteMessage", map[string]any{
			"chat_id":    chatID,
			"message_id": oneBotIDParam(messageID),
		})
	}
	return nil, fmt.Errorf("当前平台不支持撤回")
}

func (t *dianaPlatformTool) runRecall(ctx context.Context, input map[string]any, access string, owner bool) (string, error) {
	platform := t.runtime.currentPlatform(t.event)
	if !platformSupportsRecall(platform) {
		err := fmt.Errorf("当前平台不支持撤回消息，原消息仍在，请直接发更正内容并说明前一条说错了")
		t.runtime.recordPlatformInterfaceOperation(t.event, platformOpRecall, access, owner, "", err)
		return "", err
	}
	target, err := t.runtime.resolveRecallTarget(t.event, configToolString(input, "message_id"))
	if err != nil {
		t.runtime.recordPlatformInterfaceOperation(t.event, platformOpRecall, access, owner, "", err)
		return "", err
	}
	messageID := recallableOutboundMessageID(target)

	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	data, callErr := t.runtime.deletePlatformMessage(callCtx, t.event, messageID)
	if callErr != nil {
		// 超时限、没权限、消息已不存在都走这里。平台原话照抄给模型，让它知道原消息
		// 还在，只能改口说「前面那条说错了」，不能宣称已撤回。
		wrapped := fmt.Errorf("撤回失败：%w；原消息仍在群里，不要声称已经撤回，直接说明前一条说错了并给出更正", callErr)
		t.runtime.recordPlatformInterfaceOperation(t.event, platformOpRecall, access, owner, messageID, wrapped)
		return "", wrapped
	}
	t.runtime.recordPlatformInterfaceOperation(t.event, platformOpRecall, access, owner, messageID, nil)
	return t.marshal(platformOpRecall, access, map[string]any{
		"message_id": messageID,
		"data":       data,
		"message":    "已撤回我自己发出的那条消息。接着把更正内容正常发出来。",
	})
}
