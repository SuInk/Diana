package assistant

import (
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/SuInk/diana/internal/secretmask"
)

// 连接参数（平台地址、Token、AppSecret……）和停用机器人在聊天里改错了，机器人当场
// 掉线，聊天里就再也改不回来。所以这类字段先进暂存区，主人在后续消息里确认才生效：
// 同一条消息里的 update 和 confirm 不算数，防止模型一口气自己确认。

const pendingBotFieldsTTL = 10 * time.Minute

type pendingBotFields struct {
	patch     map[string]any
	messageID string
	userID    string
	session   string
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
	secretmask.RegisterChatConfigSecrets(patch)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pendingBotFields == nil {
		r.pendingBotFields = map[string]pendingBotFields{}
	}
	delete(r.pendingConsoleConfig, configPendingKey(event))
	merged := map[string]any{}
	if current, ok := r.pendingBotFields[configPendingKey(event)]; ok && current.userID == event.UserID && current.session == configConfirmationSession(event) && time.Now().Before(current.expires) {
		maps.Copy(merged, current.patch)
	}
	maps.Copy(merged, patch)
	r.pendingBotFields[configPendingKey(event)] = pendingBotFields{patch: merged, messageID: event.MessageID, userID: event.UserID, session: configConfirmationSession(event), expires: time.Now().Add(pendingBotFieldsTTL)}
	return redactedPendingPatch(merged)
}

// takePendingBotFields 取出暂存并清空；确认必须来自后续消息。
func (r *Runtime) takePendingBotFields(profileID string, event MessageEvent) (map[string]any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pending, ok := r.pendingBotFields[configPendingKey(event)]
	if !ok || time.Now().After(pending.expires) {
		delete(r.pendingBotFields, configPendingKey(event))
		return nil, fmt.Errorf("没有待确认的改动（暂存 %d 分钟后失效）", int(pendingBotFieldsTTL/time.Minute))
	}
	if event.UserID == "" || event.UserID != pending.userID || pending.session != configConfirmationSession(event) {
		return nil, fmt.Errorf("请由发起改动的用户在原会话里确认")
	}
	if event.MessageID == "" || event.MessageID == pending.messageID {
		return nil, fmt.Errorf("暂存的改动要主人在下一条消息里明确确认后才能生效")
	}
	delete(r.pendingBotFields, configPendingKey(event))
	return pending.patch, nil
}

func (r *Runtime) discardPendingBotFields(profileID string, event MessageEvent) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.pendingBotFields[configPendingKey(event)]
	delete(r.pendingBotFields, configPendingKey(event))
	return ok
}

func (r *Runtime) pendingBotFieldsView(profileID string, event MessageEvent) map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()
	pending, ok := r.pendingBotFields[configPendingKey(event)]
	if !ok || time.Now().After(pending.expires) {
		return nil
	}
	return redactedPendingPatch(pending.patch)
}

// redactedPendingPatch 回显暂存内容时遮住凭据，只说改了哪些键。
func redactedPendingPatch(patch map[string]any) map[string]any {
	return secretmask.ChatConfigValue(patch).(map[string]any)
}

func configConfirmationSession(event MessageEvent) string {
	return strings.Join([]string{event.ProfileID, event.Platform, event.PlatformScope, event.ContextNamespace, string(event.Kind), event.GroupID, event.MessageThreadID}, "\x00")
}

// Only an explicit standalone response is confirmation; quoted text and tool
// arguments cannot authorize a pending change. No confirmation code is needed.
func isConfigConfirmation(text string) bool {
	text = strings.Trim(strings.TrimSpace(text), "。！! .\n\t")
	switch strings.ToLower(text) {
	case "确认", "确认修改", "确认更改", "确认执行", "确认应用", "确认保存", "确定", "同意", "confirm", "yes":
		return true
	}
	return false
}

func configPendingKey(event MessageEvent) string {
	return configConfirmationSession(event) + "\x00" + event.UserID
}
