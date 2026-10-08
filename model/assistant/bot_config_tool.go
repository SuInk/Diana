package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/applog"
)

const botConfigToolName = "bot_config"

type BotSettingsConfigSaver interface {
	SaveBotSettings(BotConfig, BotSettingsUpdate) (BotConfig, error)
}

type dianaBotConfigTool struct {
	runtime *Runtime
	event   MessageEvent
	admin   bool // Set only by the authenticated WebUI control-plane entry point.
}

func newDianaBotConfigTool(r *Runtime, event MessageEvent) *dianaBotConfigTool {
	return &dianaBotConfigTool{runtime: r, event: event}
}
func (*dianaBotConfigTool) Name() string { return botConfigToolName }
func (*dianaBotConfigTool) Description() string {
	return "读取 Diana 脱敏配置、诊断，修改接话和入群欢迎。群级设置需主人或实时核验的群管理员，机器人默认设置和诊断仅主人。开启或关闭欢迎修改 welcome_enabled，不创建事件任务。保存成功才报告生效。"
}
func (*dianaBotConfigTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation":                    toolEnumParam("update 局部修改，保存成功才算已改。", "get", "update"),
		"scope":                        toolEnumParam("settings 默认群聊 group、私聊 bot；诊断省略或传 bot。不支持跨机器人或群。", "group", "bot"),
		"section":                      toolEnumParam("默认 settings：接话和欢迎设置，可局部 update；其他部分仅主人 get 脱敏诊断。", "settings", "all", "bot", "llm", "skills", "runtime", "paths"),
		"desire_level":                 toolEnumParam("回复欲望；off 关主动插话，明确请求仍回复。", "off", "low", "medium", "high", "max"),
		"relevance_level":              toolEnumParam("on：明确跟机器人说话时回应。", "on", "off"),
		"chat_level":                   toolEnumParam("闲聊档位，按本群近一小时的插话需求动态控制频率，受冷却限制；always 不限频率。", "off", "minimal", "low", "medium", "high", "always"),
		"cooldown_seconds":             toolIntParam("主动闲聊冷却秒数，0 关闭。", 0, 3600),
		"minimum_reply_member_level":   toolIntParam("最低回复成员等级，仅 OneBot 群。", 0, maximumReplyMemberLevel),
		"welcome_enabled":              toolBoolParam("内置入群欢迎开关；重复开启仅更新配置，不创建事件任务。"),
		"welcome_message":              toolStringParam("固定欢迎词，也作为 LLM 或模板模式的回落文本。"),
		"welcome_mode":                 toolEnumParam("固定文本、随机模板或 LLM 按人设生成。", "fixed", "template", "llm"),
		"welcome_templates":            toolStringArrayParam("欢迎模板池，最多 50 条，每条最多 200 字。"),
		"welcome_llm_cooldown_seconds": toolIntParam("每群 LLM 欢迎冷却秒数；0 使用机器人或系统默认值，冷却期间回落文本。", 0, 86400),
	})
}

func (t *dianaBotConfigTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("机器人运行时不可用")
	}
	op := t.CanonicalOperation(input)
	if op != "get" && op != "update" {
		return "", fmt.Errorf("operation 必须为 get 或 update")
	}
	base, err := t.runtime.modelConfigForEvent(t.event)
	if err != nil {
		return "", err
	}
	owner := t.admin || base.IsOwnerEvent(t.event)
	section := strings.TrimSpace(configToolString(input, "section"))
	if section == "" {
		section = "settings"
	}
	if section != "settings" {
		if op != "get" {
			return "", fmt.Errorf("只有 section=settings 支持修改配置")
		}
		if !owner {
			return "", fmt.Errorf("只有机器人主人可以读取完整配置和运行诊断")
		}
		if scope := strings.TrimSpace(configToolString(input, "scope")); scope != "" && scope != "bot" {
			return "", fmt.Errorf("诊断查询只支持 scope=bot")
		}
		for key := range input {
			if key != "operation" && key != "section" && key != "scope" {
				return "", fmt.Errorf("诊断查询不支持参数 %q", key)
			}
		}
		return t.runtime.readConfigDiagnostics(t.event, section)
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
	} else if !owner {
		return "", fmt.Errorf("只有机器人主人可以读取或修改机器人级设置")
	}
	prefs := effective.participationPreferences()
	minimum := group.MinimumReplyMemberLevel
	if op == "update" {
		allowed := map[string]bool{"operation": true, "scope": true, "section": true, "desire_level": true, "relevance_level": true, "substance_level": true, "chat_level": true, "cooldown_seconds": true, "minimum_reply_member_level": true,
			"welcome_enabled": true, "welcome_message": true, "welcome_mode": true, "welcome_templates": true, "welcome_llm_cooldown_seconds": true}
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
			if level == "off" {
				prefs.RelevanceLevel = "off"
				prefs.ChatLevel = "off"
			} else {
				prefs.ChatLevel = level
				if level == "max" {
					prefs.ChatLevel = "always"
				}
				if prefs.RelevanceLevel == "off" {
					prefs.RelevanceLevel = "on"
				}
			}
			changed = true
		}
		for _, field := range []struct {
			key    string
			target **int
		}{{"substance_level", &prefs.SubstanceThreshold}} {
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
		for _, field := range []struct {
			key    string
			target *string
		}{{"relevance_level", &prefs.RelevanceLevel}, {"chat_level", &prefs.ChatLevel}} {
			if raw, exists := input[field.key]; exists {
				level, ok := raw.(string)
				if field.key == "relevance_level" && ok && level != "on" && level != "off" {
					return "", fmt.Errorf("relevance_level 只能是 on 或 off")
				}
				if !ok || (field.key != "relevance_level" && !validParticipationLevel(level)) {
					return "", fmt.Errorf("无效评分档位 %s", field.key)
				}
				if level == "extreme" {
					level = "high" // 「频繁参与」已去掉，见 ratingLevels
				}
				*field.target = level
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
		update, err := parseWelcomeSettingsUpdate(input)
		if err != nil {
			return "", err
		}
		if participationChanged {
			update.Participation = copyParticipation(&prefs)
		}
		if !changed && !update.UpdatesWelcome() {
			return "", fmt.Errorf("至少提供一项要修改的机器人设置")
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
			group = update.applyToGroup(group)
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
			if changed {
				t.runtime.cancelProactiveReplyBatch(t.event)
				t.runtime.recordGroupReplyPolicyChanged(ctx, t.event, role, saved)
			}
		} else {
			if err := t.runtime.saveBotSettings(base, update); err != nil {
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
			_ = writer.AppendLog(ctx, applog.Entry{Kind: applog.KindOperation, Level: applog.LevelInfo, Action: "bot_config_update", Message: "机器人设置已保存并生效", Actor: oneBotEventActor(t.event), Target: base.ID, Metadata: map[string]any{"scope": scope, "group_id": t.event.GroupID, "bot_profile_id": base.ID, "participation": prefs, "welcome": welcomeSettingsFromConfig(effective)}})
		}
	}
	groupID, message := "", "已读取实际生效机器人设置"
	if scope == "group" {
		groupID = t.event.GroupID
	}
	if op == "update" {
		message = "已保存并应用机器人设置"
	}
	data, err := json.Marshal(map[string]any{"ok": true, "action": op, "section": section, "scope": scope, "bot_profile_id": base.ID, "group_id": groupID, "operator_role": role, "participation": prefs, "welcome": welcomeSettingsFromConfig(effective), "minimum_reply_member_level": minimum, "message": message})
	return string(data), err
}

func (r *Runtime) saveBotSettings(expected BotConfig, update BotSettingsUpdate) error {
	r.modelConfigMu.Lock()
	defer r.modelConfigMu.Unlock()
	r.mu.RLock()
	saver, ok := r.configSaver.(BotSettingsConfigSaver)
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("配置存储不支持机器人设置更新")
	}
	saved, err := saver.SaveBotSettings(expected, update)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.profileConfigs == nil {
		r.profileConfigs = map[string]BotConfig{}
	}
	r.profileConfigs[saved.ID] = saved
	r.updatedAt = time.Now()
	return nil
}

// CanonicalOperation 是 Run 实际执行的操作，按操作拦截时用同一套换算。
func (*dianaBotConfigTool) CanonicalOperation(input map[string]any) string {
	return strings.TrimSpace(configToolString(input, "operation"))
}
