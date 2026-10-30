package assistant

import (
	"fmt"
	"maps"
	"strings"
	"time"
)

// 连接参数（平台地址、Token、AppSecret……）和停用机器人在聊天里改错了，机器人当场
// 掉线，聊天里就再也改不回来。所以这类字段先进暂存区，主人在后续消息里确认才生效：
// 同一条消息里的 update 和 confirm 不算数，防止模型一口气自己确认。

const pendingBotFieldsTTL = 10 * time.Minute

type pendingBotFields struct {
	patch     map[string]any
	messageID string
	userID    string
	expires   time.Time
}

var stagedBotFieldPrefixes = []string{"onebot_", "telegram_", "qq_app", "qq_sandbox", "weixin_", "imessage_"}

var stagedBotFieldKeys = map[string]bool{"platform": true, "callback_path": true, "bot_account": true, "connection_profile_id": true, "enabled": true}

func botFieldNeedsConfirmation(key string) bool {
	if stagedBotFieldKeys[key] {
		return true
	}
	for _, prefix := range stagedBotFieldPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// splitStagedBotFields 拆出需要确认的字段，其余立即生效。
func splitStagedBotFields(patch map[string]any) (immediate, staged map[string]any) {
	for key, value := range patch {
		if botFieldNeedsConfirmation(key) {
			if staged == nil {
				staged = map[string]any{}
			}
			staged[key] = value
			continue
		}
		if immediate == nil {
			immediate = map[string]any{}
		}
		immediate[key] = value
	}
	return immediate, staged
}

func (r *Runtime) stageBotFields(profileID string, event MessageEvent, patch map[string]any) map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pendingBotFields == nil {
		r.pendingBotFields = map[string]pendingBotFields{}
	}
	merged := map[string]any{}
	if current, ok := r.pendingBotFields[profileID]; ok && time.Now().Before(current.expires) {
		maps.Copy(merged, current.patch)
	}
	maps.Copy(merged, patch)
	r.pendingBotFields[profileID] = pendingBotFields{patch: merged, messageID: event.MessageID, userID: event.UserID, expires: time.Now().Add(pendingBotFieldsTTL)}
	return redactedPendingPatch(merged)
}

// takePendingBotFields 取出暂存并清空；确认必须来自后续消息。
func (r *Runtime) takePendingBotFields(profileID string, event MessageEvent) (map[string]any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pending, ok := r.pendingBotFields[profileID]
	if !ok || time.Now().After(pending.expires) {
		delete(r.pendingBotFields, profileID)
		return nil, fmt.Errorf("没有待确认的改动（暂存 %d 分钟后失效）", int(pendingBotFieldsTTL/time.Minute))
	}
	if event.MessageID != "" && event.MessageID == pending.messageID {
		return nil, fmt.Errorf("暂存的改动要主人在下一条消息里明确确认后才能生效")
	}
	delete(r.pendingBotFields, profileID)
	return pending.patch, nil
}

func (r *Runtime) discardPendingBotFields(profileID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.pendingBotFields[profileID]
	delete(r.pendingBotFields, profileID)
	return ok
}

func (r *Runtime) pendingBotFieldsView(profileID string) map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()
	pending, ok := r.pendingBotFields[profileID]
	if !ok || time.Now().After(pending.expires) {
		return nil
	}
	return redactedPendingPatch(pending.patch)
}

// redactedPendingPatch 回显暂存内容时遮住凭据，只说改了哪些键。
func redactedPendingPatch(patch map[string]any) map[string]any {
	out := make(map[string]any, len(patch))
	for key, value := range patch {
		lower := strings.ToLower(key)
		if value != nil && (strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "password")) {
			out[key] = "（已填写，内容不回显）"
			continue
		}
		out[key] = value
	}
	return out
}
