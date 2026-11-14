// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/SuInk/diana/model/agent"
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
	// 戳一戳、通话记录的 ID 是本地编的，平台上没有这条消息。
	if !event.Outbound || isNarrationHistoryEvent(event) {
		return ""
	}
	id := strings.TrimSpace(event.MessageID)
	if id == "" || strings.HasPrefix(id, localOutboundIDPrefix) {
		return ""
	}
	return id
}

func platformSupportsRecall(platform string) bool {
	return platformSupportsOperation(platform, platformOpRecall)
}

// resolveRecallTarget 在本会话历史里定位要撤回的消息。
//
// messageID 为空时取机器人最近发出的一条。给了 ID 就必须精确命中，命中的还必须是
// 机器人自己发的——命中别人的消息一律拒绝，这是这个能力的边界，不靠提示词守。
//
// cursor 只在 message_id 没命中时有用：报错里的候选按单次上限分页，cursor 是上一页
// 最后一条候选的消息 ID，从它之后（更早的）接着列。
func (r *Runtime) resolveRecallTarget(ctx context.Context, event MessageEvent, messageID, cursor string) (MessageEvent, error) {
	if r == nil {
		return MessageEvent{}, fmt.Errorf("运行时不可用")
	}
	session := sessionKey(event)
	r.mu.RLock()
	store := r.messageStore
	history := append([]MessageEvent(nil), r.history[session]...)
	r.mu.RUnlock()

	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		for i := len(history) - 1; i >= 0; i-- {
			if recallableOutboundMessageID(history[i]) != "" {
				return history[i], nil
			}
		}
		if store != nil {
			loadCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			stored, err := store.ListRecentMessageEvents(loadCtx, session, 80)
			cancel()
			if err == nil {
				for i := len(stored) - 1; i >= 0; i-- {
					if recallableOutboundMessageID(stored[i]) != "" {
						return stored[i], nil
					}
				}
			}
		}
		return MessageEvent{}, fmt.Errorf("本次会话里没有可撤回的消息：要么我还没发过话，要么平台没有返回消息 ID")
	}
	checkTarget := func(candidate MessageEvent) (MessageEvent, error) {
		if !candidate.Outbound {
			return MessageEvent{}, fmt.Errorf("这条不是我发的，只能撤回我自己发出的消息；要撤回群成员（含当前发言者本人）发出的消息请改用 operation=\"recall_messages\"")
		}
		if recallableOutboundMessageID(candidate) == "" {
			return MessageEvent{}, fmt.Errorf("这条消息没有平台消息 ID，撤不了")
		}
		return candidate, nil
	}
	for i := len(history) - 1; i >= 0; i-- {
		if strings.TrimSpace(history[i].MessageID) != messageID {
			continue
		}
		return checkTarget(history[i])
	}
	if lookup, ok := store.(MessageEventLookupStore); ok {
		loadCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		found, exists, err := lookup.FindMessageEvent(loadCtx, session, messageID)
		cancel()
		if err == nil && exists {
			return checkTarget(found)
		}
	}
	head := fmt.Sprintf("本次会话里找不到消息 %s：message_id 必须取自我自己发出的消息，不要按内容猜。", messageID)
	budget := agent.ToolOutputBudget(ctx)
	if budget <= 0 {
		budget = agent.ToolOutputMaxChars
	}
	return MessageEvent{}, errors.New(head + recallCandidateHint(history, strings.TrimSpace(cursor), budget-utf8.RuneCountInString(head)))
}

// recallCandidateNoteReserve 给候选列表末尾的续读说明留的字数。
const recallCandidateNoteReserve = 200

// recallCandidateHint 在 message_id 没命中时，把本会话里还能撤回的自家消息列回去，
// 新到旧、正文给全。模型手上没有消息 ID 清单，不给候选它只会再编一个。候选多到装不下
// budget 字时整条分页：cursor 是上一页最后一条候选的 ID，从它之后接着列，并写明还剩
// 多少、怎么续读。按 ID 续读而不是按序号：期间新发的消息排在最前，不会把后面挤得错位。
func recallCandidateHint(history []MessageEvent, cursor string, budget int) string {
	type candidate struct{ id, line string }
	var candidates []candidate
	for i := len(history) - 1; i >= 0; i-- {
		id := recallableOutboundMessageID(history[i])
		if id == "" {
			continue
		}
		candidates = append(candidates, candidate{id: id, line: fmt.Sprintf("%s（%s）", id, summarizeRecallCandidate(history[i]))})
	}
	if len(candidates) == 0 {
		return "本会话里目前没有可撤回的自家消息"
	}
	start := 0
	if cursor != "" {
		start = -1
		for i, item := range candidates {
			if item.id == cursor {
				start = i + 1
				break
			}
		}
		if start < 0 {
			return fmt.Sprintf("cursor=%s 对应的消息已不在本会话的近期记录里；不传 cursor 再调一次，从最新的候选看起（共 %d 条）", cursor, len(candidates))
		}
		if start >= len(candidates) {
			return fmt.Sprintf("可撤回的候选已经列完了（共 %d 条），cursor=%s 之后没有更早的", len(candidates), cursor)
		}
	}
	budget -= recallCandidateNoteReserve
	end, used := start, 0
	for end < len(candidates) {
		size := utf8.RuneCountInString(candidates[end].line) + 1
		if end > start && used+size > budget {
			break
		}
		used += size
		end++
	}
	lines := make([]string, 0, end-start)
	for _, item := range candidates[start:end] {
		lines = append(lines, item.line)
	}
	hint := fmt.Sprintf("可撤回的共 %d 条（新到旧），这里是第 %d～%d 条：%s", len(candidates), start+1, end, strings.Join(lines, "、"))
	if remaining := len(candidates) - end; remaining > 0 {
		hint += fmt.Sprintf("。单次结果上限装不下，还有 %d 条更早的没列：要看就再调 recall，message_id 照旧、传 cursor=%s（没命中不会撤回任何消息）；没看完之前不要断言没有那条", remaining, candidates[end-1].id)
	}
	return hint
}

func summarizeRecallCandidate(event MessageEvent) string {
	text := strings.TrimSpace(PlainText(event.Segments))
	if text == "" {
		text = strings.TrimSpace(event.RawMessage)
	}
	text = strings.Join(strings.Fields(text), " ")
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
	case PlatformQQOfficial:
		path, err := qqOfficialDeleteMessagePath(event, messageID)
		if err != nil {
			return nil, err
		}
		return r.callPlatformAPIForEvent(ctx, event, "DELETE "+path, nil)
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
	messageID, cursor := strings.TrimSpace(configToolString(input, "message_id")), strings.TrimSpace(configToolString(input, "cursor"))
	if cursor != "" && messageID == "" {
		// message_id 为空会撤回最近一条；cursor 只用来翻候选，不能让翻页变成误撤。
		err := fmt.Errorf("cursor 只用来翻看可撤回的候选，必须同时给 message_id；没有确定要撤哪条之前不要省略 message_id")
		t.runtime.recordPlatformInterfaceOperation(t.event, platformOpRecall, access, owner, "", err)
		return "", err
	}
	target, err := t.runtime.resolveRecallTarget(ctx, t.event, messageID, cursor)
	if err != nil {
		t.runtime.recordPlatformInterfaceOperation(t.event, platformOpRecall, access, owner, "", err)
		return "", err
	}
	messageID = recallableOutboundMessageID(target)
	summary := summarizeRecallCandidate(target)

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
		"message_id":       messageID,
		"recalled_summary": summary,
		"data":             data,
		"message":          fmt.Sprintf("已撤回我自己发出的消息 %s（%s）。接着把更正内容正常发出来。", messageID, summary),
	})
}
