package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/applog"
)

const botParticipationToolName = "diana.bot_config"

type BotParticipationConfigSaver interface {
	SaveParticipation(BotConfig, ParticipationPreferences) (BotConfig, error)
}

type dianaBotParticipationTool struct {
	runtime *Runtime
	event   MessageEvent
}

func newDianaBotParticipationTool(r *Runtime, event MessageEvent) *dianaBotParticipationTool {
	return &dianaBotParticipationTool{runtime: r, event: event}
}
func (*dianaBotParticipationTool) Name() string { return botParticipationToolName }
func (*dianaBotParticipationTool) Description() string {
	return "读取或修改 Diana 自身的回复欲望、相关度门槛、实质性门槛和主动闲聊冷却。get 读取，update 局部修改；scope=group 只改当前群（主人或实时核验的群管理员），scope=bot 改消息所属机器人（仅主人）。关闭话痨或主动插话用 desire_level=off，降低活跃程度用 low；不操作平台禁言、不修改插件或模型。"
}
func (*dianaBotParticipationTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation":                  toolEnumParam("读取或局部更新；只有保存成功才报告已修改。", "get", "update"),
		"scope":                      toolEnumParam("群聊默认 group，私聊默认 bot。不能指定其他机器人或群。", "group", "bot"),
		"desire_level":               toolEnumParam("回复欲望；off 关闭主动插话，明确请求仍可回复。仅修改欲望，保留门槛和冷却。", "off", "low", "medium", "high", "max"),
		"relevance_level":            toolEnumParam("相关度门槛，低/中/高/极高对应 40/60/80/90 分。", "low", "medium", "high", "max"),
		"substance_level":            toolEnumParam("闲聊实质性门槛，越高越严格。", "low", "medium", "high", "max"),
		"cooldown_seconds":           toolIntParam("主动闲聊冷却，0 关闭冷却。", 0, 3600),
		"minimum_reply_member_level": toolIntParam("仅 OneBot 群支持的最低回复成员等级。", 0, maximumReplyMemberLevel),
	})
}

func (t *dianaBotParticipationTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("机器人运行时不可用")
	}
	scope := strings.TrimSpace(configToolString(input, "scope"))
	if scope == "" {
		if t.event.Kind == EventKindGroup {
			scope = "group"
		} else {
			scope = "bot"
		}
	}
	if scope != "group" && scope != "bot" {
		return "", fmt.Errorf("scope 必须为 group 或 bot")
	}
	op := strings.TrimSpace(configToolString(input, "operation"))
	if op != "get" && op != "update" {
		return "", fmt.Errorf("operation 必须为 get 或 update")
	}
	base, err := t.runtime.modelConfigForEvent(t.event)
	if err != nil {
		return "", err
	}
	role := "bot_owner"
	var group GroupConfig
	effective := base
	if scope == "group" {
		if t.event.Kind != EventKindGroup || t.event.GroupID == "" {
			return "", fmt.Errorf("群级配置只能在当前群操作")
		}
		authEvent := t.event
		if !base.IsOwnerEvent(authEvent) {
			authEvent.SenderRole = ""
		}
		role, err = t.runtime.canConfigureGroup(ctx, authEvent)
		if err != nil {
			return "", err
		}
		var ok bool
		group, ok = t.runtime.groupConfigForEvent(t.event)
		if !ok {
			group = DefaultGroupConfig(t.event.GroupID, base)
		}
		group.BotProfileID = base.ID
		effective = t.runtime.effectiveConfigForEvent(t.event)
	} else if !base.IsOwnerEvent(t.event) {
		return "", fmt.Errorf("只有机器人主人可以修改机器人级回复设置")
	}
	prefs := effective.participationPreferences()
	minimum := group.MinimumReplyMemberLevel
	if op == "update" {
		allowed := map[string]bool{"operation": true, "scope": true, "desire_level": true, "relevance_level": true, "substance_level": true, "cooldown_seconds": true, "minimum_reply_member_level": true}
		for key := range input {
			if !allowed[key] {
				return "", fmt.Errorf("不支持配置项 %q", key)
			}
		}
		changed := false
		if raw, ok := input["desire_level"]; ok {
			level, ok := raw.(string)
			if !ok {
				return "", fmt.Errorf("desire_level 必须是档位名称")
			}
			desire, valid := map[string]int{"off": 0, "low": 25, "medium": 50, "high": 75, "max": 100}[level]
			if !valid {
				return "", fmt.Errorf("无效回复欲望档位")
			}
			prefs.Desire = desire
			changed = true
		}
		for _, field := range []struct {
			key    string
			target **int
		}{{"relevance_level", &prefs.RelevanceThreshold}, {"substance_level", &prefs.SubstanceThreshold}} {
			if raw, ok := input[field.key]; ok {
				level, _ := raw.(string)
				score, valid := map[string]int{"low": 40, "medium": 60, "high": 80, "max": 90}[level]
				if !valid {
					return "", fmt.Errorf("无效门槛档位 %s", field.key)
				}
				*field.target = &score
				changed = true
			}
		}
		if raw, ok := input["cooldown_seconds"]; ok {
			seconds, e := groupToolInteger(raw)
			if e != nil || seconds < 0 || seconds > 3600 {
				return "", fmt.Errorf("cooldown_seconds 必须为 0–3600 的整数")
			}
			prefs.CooldownSeconds = seconds
			changed = true
		}
		participationChanged := changed
		if raw, ok := input["minimum_reply_member_level"]; ok {
			if scope != "group" || !IsOneBotPlatform(base.Platform) {
				return "", fmt.Errorf("最低成员等级仅适用于 OneBot 群")
			}
			minimum, err = groupToolInteger(raw)
			if err != nil || minimum < 0 || minimum > maximumReplyMemberLevel {
				return "", fmt.Errorf("最低成员等级超出范围")
			}
			changed = true
		}
		if !changed {
			return "", fmt.Errorf("至少提供一项要修改的回复设置")
		}
		if scope == "group" {
			if participationChanged {
				group.Participation = copyParticipation(&prefs)
				group.ChatInLevel = prefs.replyLevel()
				group.ChatInEnabled = boolPointer(prefs.Desire > 0)
				group.ResponseMode = ResponseModeCustom
				group.NaturalInterjectionEnabled = boolPointer(false)
			}
			group.MinimumReplyMemberLevel = minimum
			t.runtime.mu.RLock()
			writer, ok := t.runtime.groupConfigs.(GroupConfigWriter)
			t.runtime.mu.RUnlock()
			if !ok {
				return "", fmt.Errorf("当前未接入可写的群配置存储")
			}
			saved, e := writer.SaveGroupConfig(group, base)
			if e != nil {
				return "", e
			}
			t.runtime.cancelProactiveReplyBatch(t.event)
			t.runtime.recordGroupReplyPolicyChanged(ctx, t.event, role, saved)
		} else {
			if err := t.runtime.saveBotParticipation(base, prefs); err != nil {
				return "", err
			}
		}
		if scope == "group" {
			effective = t.runtime.effectiveConfigForEvent(t.event)
		} else {
			effective, err = t.runtime.modelConfigForEvent(t.event)
			if err != nil {
				return "", err
			}
		}
		prefs = effective.participationPreferences()
		if writer := t.runtime.appLogWriter(); writer != nil {
			_ = writer.AppendLog(ctx, applog.Entry{Kind: applog.KindOperation, Level: applog.LevelInfo, Action: "diana.bot_config.update", Message: "回复设置已保存并生效", Actor: oneBotEventActor(t.event), Target: base.ID, Metadata: map[string]any{"scope": scope, "group_id": t.event.GroupID, "bot_profile_id": base.ID, "participation": prefs}})
		}
	}
	groupID, message := "", "已读取实际生效回复设置"
	if scope == "group" {
		groupID = t.event.GroupID
	}
	if op == "update" {
		message = "已保存并应用回复设置"
	}
	data, err := json.Marshal(map[string]any{"ok": true, "action": op, "scope": scope, "bot_profile_id": base.ID, "group_id": groupID, "operator_role": role, "participation": prefs, "minimum_reply_member_level": minimum, "message": message})
	return string(data), err
}

func (r *Runtime) saveBotParticipation(expected BotConfig, prefs ParticipationPreferences) error {
	r.modelConfigMu.Lock()
	defer r.modelConfigMu.Unlock()
	r.mu.RLock()
	saver, ok := r.configSaver.(BotParticipationConfigSaver)
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("配置存储不支持机器人回复设置更新")
	}
	saved, err := saver.SaveParticipation(expected, prefs)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cfg.ID == saved.ID {
		r.cfg = saved
	}
	if r.profileConfigs == nil {
		r.profileConfigs = map[string]BotConfig{}
	}
	r.profileConfigs[saved.ID] = saved
	r.updatedAt = time.Now()
	return nil
}
