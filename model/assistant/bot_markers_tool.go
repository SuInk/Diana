package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

type botMarkersSaver interface {
	SaveMarkedBotIDs(profileID string, ids []string) error
}

type dianaBotMarkersTool struct {
	runtime *Runtime
	event   MessageEvent
}

func (t *dianaBotMarkersTool) Name() string { return "diana.bot_markers" }
func (t *dianaBotMarkersTool) Description() string {
	return "主人管理当前机器人的手动机器人名单，支持 QQ 和 Telegram。只有主人明确要求标记、取消标记或查询时调用；不要根据聊天内容自行给用户贴机器人标签。scope=bot 对本机所有群生效，scope=group 仅当前群。标记后默认抑制其群消息，语义上向本机接话时仍可回应。使用真实账号 ID；昵称不确定时查询成员或请主人指定，不猜测 ID。引用某人消息后说把这个标记为机器人时，可以省略 user_id 使用被引用者。"
}
func (t *dianaBotMarkersTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation", "scope"}, map[string]any{
		"operation": toolEnumParam("操作", "mark", "unmark", "list"),
		"scope":     toolEnumParam("bot 为当前机器人所有群；group 为当前群", "bot", "group"),
		"user_id":   toolStringParam("目标账号 ID。省略时使用当前引用消息的发送者，不使用昵称猜测。"),
	})
}
func (t *dianaBotMarkersTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("机器人运行时不可用")
	}
	r := t.runtime
	baseEvent := t.event
	baseEvent.Kind = EventKindPrivate
	baseEvent.GroupID = ""
	base := r.effectiveConfigForEvent(baseEvent)
	if !base.IsOwnerEvent(t.event) {
		return "", fmt.Errorf("只有本机主人可以管理机器人标记，群管理员无此权限")
	}
	scope := configToolString(input, "scope")
	if scope != "bot" && scope != "group" {
		return "", fmt.Errorf("scope 必须为 bot 或 group")
	}
	if scope == "group" && (t.event.Kind != EventKindGroup || t.event.GroupID == "") {
		return "", fmt.Errorf("群级标记只能在目标群内操作")
	}
	op := configToolString(input, "operation")
	if op != "list" && op != "mark" && op != "unmark" {
		return "", fmt.Errorf("operation 必须为 mark、unmark 或 list")
	}
	group, _ := r.groupConfigForEvent(t.event)
	if group.GroupID == "" {
		group = DefaultGroupConfig(t.event.GroupID, base)
	}
	group.BotProfileID = base.ID
	ids := base.MarkedBotIDs
	if scope == "group" {
		ids = group.MarkedBotIDs
	}
	id := strings.TrimSpace(configToolString(input, "user_id"))
	if op != "list" {
		if id == "" && t.event.Quoted != nil {
			id = strings.TrimSpace(t.event.Quoted.UserID)
		}
		if id == "" {
			return "", fmt.Errorf("请指定目标账号 ID 或引用目标消息")
		}
		for _, c := range id {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c == '-') {
				return "", fmt.Errorf("目标必须是账号 ID，不能使用昵称")
			}
		}
		if op == "mark" && (id == base.OwnerID || id == base.BotAccount || id == t.event.SelfID) {
			return "", fmt.Errorf("不能将主人或本机标记为其他机器人")
		}
		ids = append([]string(nil), ids...)
		if op == "mark" {
			ids = cleanStrings(append(ids, id))
		} else {
			ids = slices.DeleteFunc(ids, func(v string) bool { return v == id })
		}
		if scope == "group" {
			group.MarkedBotIDs = ids
			r.mu.RLock()
			writer, ok := r.groupConfigs.(GroupConfigWriter)
			r.mu.RUnlock()
			if !ok {
				return "", fmt.Errorf("当前未接入可写群配置存储")
			}
			if _, err := writer.SaveGroupConfig(group, base); err != nil {
				return "", err
			}
		} else {
			var err error
			ids, err = r.updateMarkedBotID(base.ID, t.event.UserID, id, op == "mark")
			if err != nil {
				return "", err
			}
			base.MarkedBotIDs = ids
		}
	}
	result := map[string]any{"scope": scope, "operation": op, "user_id": id, "marked_bot_ids": ids, "effective_marked_bot_ids": cleanStrings(append(append([]string(nil), base.MarkedBotIDs...), group.MarkedBotIDs...))}
	encoded, err := json.Marshal(result)
	return string(encoded), err
}

func (r *Runtime) updateMarkedBotID(profileID, actorID, userID string, marked bool) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	saver, ok := r.configSaver.(botMarkersSaver)
	if !ok {
		return nil, fmt.Errorf("当前未接入可确认持久化结果的机器人配置存储")
	}
	profile, exists := r.profileConfigs[profileID]
	if !exists {
		if r.cfg.ID != profileID {
			return nil, fmt.Errorf("目标机器人不存在")
		}
		profile = r.cfg
	}
	if profile.OwnerID == "" || profile.OwnerID != actorID {
		return nil, fmt.Errorf("只有本机主人可以修改机器人标记")
	}
	ids := append([]string(nil), profile.MarkedBotIDs...)
	if marked {
		ids = cleanStrings(append(ids, userID))
	} else {
		ids = slices.DeleteFunc(ids, func(id string) bool { return id == userID })
	}
	if err := saver.SaveMarkedBotIDs(profileID, ids); err != nil {
		return nil, err
	}
	profile.MarkedBotIDs = append([]string(nil), ids...)
	if r.profileConfigs == nil {
		r.profileConfigs = map[string]BotConfig{}
	}
	r.profileConfigs[profileID] = profile
	if r.cfg.ID == profileID {
		r.cfg.MarkedBotIDs = append([]string(nil), ids...)
	}
	r.updatedAt = time.Now()
	return ids, nil
}
