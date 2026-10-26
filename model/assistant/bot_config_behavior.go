package assistant

import (
	"fmt"
	"sort"
	"strings"
)

// behaviorSetting 是一项可以用自然语言改的行为设置。
//
// bot_config 原本只认接话档位和入群欢迎，WebUI「行为」页里其余开关（换行分条、
// 打字延迟、回复字数上限、随机复读、禁言期间的处理……）在聊天里说了也改不动，
// 模型只能回一句「请去后台改」。这里把它们收成一张表：每项给出群级和机器人级
// 字段的读写，bot_config 的 behavior 参数按表校验、写入，读取时按表列出实际值。
//
// 群级值写 null 表示清掉本群覆盖、跟随机器人设置；机器人级没有上一层，不接受 null。
type behaviorSetting struct {
	key         string
	description string
	kind        string // bool / int
	min, max    int
	groupBool   func(*GroupConfig) **bool
	botBool     func(*BotConfig) **bool
	groupInt    func(*GroupConfig) *int
	botInt      func(*BotConfig) *int
	// boolDefault 是字段为 nil 时实际生效的值，只用于读取展示。
	boolDefault bool
	// random 是随机复读的子项，读写都落在 RandomRepeatConfig 上。
	random func(*RandomRepeatConfig) any
}

func boolBehavior(key, description string, fallback bool, group func(*GroupConfig) **bool, bot func(*BotConfig) **bool) behaviorSetting {
	return behaviorSetting{key: key, description: description, kind: "bool", boolDefault: fallback, groupBool: group, botBool: bot}
}

func intBehavior(key, description string, min, max int, group func(*GroupConfig) *int, bot func(*BotConfig) *int) behaviorSetting {
	return behaviorSetting{key: key, description: description, kind: "int", min: min, max: max, groupInt: group, botInt: bot}
}

func randomRepeatBehavior(key, description, kind string, min, max int, field func(*RandomRepeatConfig) any) behaviorSetting {
	return behaviorSetting{key: key, description: description, kind: kind, min: min, max: max, random: field}
}

var behaviorSettings = []behaviorSetting{
	boolBehavior("reply_line_split_enabled", "按换行把回复拆成多条发送", false,
		func(g *GroupConfig) **bool { return &g.ReplyLineSplitEnabled }, func(c *BotConfig) **bool { return &c.ReplyLineSplitEnabled }),
	boolBehavior("natural_reply_split_enabled", "长回复按语义自然拆成几个气泡", true,
		func(g *GroupConfig) **bool { return &g.NaturalReplySplitEnabled }, func(c *BotConfig) **bool { return &c.NaturalReplySplitEnabled }),
	boolBehavior("typing_delay_enabled", "按字数模拟打字延迟再发出", false,
		func(g *GroupConfig) **bool { return &g.TypingDelayEnabled }, func(c *BotConfig) **bool { return &c.TypingDelayEnabled }),
	boolBehavior("forward_reply_enabled", "超长回复改用合并转发卡片发送", true,
		func(g *GroupConfig) **bool { return &g.ForwardReplyEnabled }, func(c *BotConfig) **bool { return &c.ForwardReplyEnabled }),
	boolBehavior("social_reply_enabled", "接话时也回应寒暄、玩笑这类社交性发言", false,
		func(g *GroupConfig) **bool { return &g.SocialReplyEnabled }, func(c *BotConfig) **bool { return &c.SocialReplyEnabled }),
	boolBehavior("style_filter_enabled", "发送前过滤 AI 腔用语", true,
		func(g *GroupConfig) **bool { return &g.StyleFilterEnabled }, func(c *BotConfig) **bool { return &c.StyleFilterEnabled }),
	boolBehavior("recall_reply_auto_delete_enabled", "对方撤回触发消息时，自动撤回机器人对它的回复", false,
		func(g *GroupConfig) **bool { return &g.RecallReplyAutoDeleteEnabled }, func(c *BotConfig) **bool { return &c.RecallReplyAutoDeleteEnabled }),
	intBehavior("recall_reply_auto_delete_delay_seconds", "上一项的时间窗（秒）：回复发出后多久内对方撤回才跟着撤", 1, maximumRecallReplyTTLSeconds,
		func(g *GroupConfig) *int { return &g.RecallReplyTTLSeconds }, func(c *BotConfig) *int { return &c.RecallReplyTTLSeconds }),
	intBehavior("max_reply_chars", "单次回复的最大字数", 50, 20000,
		func(g *GroupConfig) *int { return &g.MaxReplyChars }, func(c *BotConfig) *int { return &c.MaxReplyChars }),
	intBehavior("reply_max_bubbles", "一次回复最多拆成几条消息", 1, 20,
		func(g *GroupConfig) *int { return &g.ReplyMaxBubbles }, func(c *BotConfig) *int { return &c.ReplyMaxBubbles }),
	boolBehavior("muted_reply_pause_enabled", "机器人被禁言时暂停回复", true,
		func(g *GroupConfig) **bool { return &g.MutedReplyPauseEnabled }, func(c *BotConfig) **bool { return &c.MutedReplyPauseEnabled }),
	boolBehavior("muted_voice_transcription_enabled", "禁言期间照常把语音转成文字", true,
		func(g *GroupConfig) **bool { return &g.MutedVoiceTranscriptionEnabled }, func(c *BotConfig) **bool { return &c.MutedVoiceTranscriptionEnabled }),
	boolBehavior("muted_image_description_enabled", "禁言期间照常识别图片", true,
		func(g *GroupConfig) **bool { return &g.MutedImageDescriptionEnabled }, func(c *BotConfig) **bool { return &c.MutedImageDescriptionEnabled }),
	boolBehavior("muted_reply_judgment_enabled", "禁言期间照常判断该不该回（只记录，不发送）", false,
		func(g *GroupConfig) **bool { return &g.MutedReplyJudgmentEnabled }, func(c *BotConfig) **bool { return &c.MutedReplyJudgmentEnabled }),
	randomRepeatBehavior("random_repeat_enabled", "群里连续几人发同一句话时随机跟着复读", "bool", 0, 0,
		func(r *RandomRepeatConfig) any { return &r.Enabled }),
	randomRepeatBehavior("random_repeat_threshold", "随机复读：至少几人连续发同一句才可能跟", "int", 2, 10,
		func(r *RandomRepeatConfig) any { return &r.Threshold }),
	randomRepeatBehavior("random_repeat_probability", "随机复读：达到人数后跟读的概率（百分比）", "int", 0, 100,
		func(r *RandomRepeatConfig) any { return &r.Probability }),
	randomRepeatBehavior("random_repeat_cooldown_seconds", "随机复读：-1 表示同一轮只跟一次，0 不冷却，正数为两次复读的最短间隔秒数；概率照常计算", "int", -1, 600,
		func(r *RandomRepeatConfig) any { return &r.CooldownSeconds }),
}

func behaviorSettingByKey(key string) (behaviorSetting, bool) {
	for _, setting := range behaviorSettings {
		if setting.key == key {
			return setting, true
		}
	}
	return behaviorSetting{}, false
}

// behaviorSettingsSchema 是 bot_config 的 behavior 参数。
func behaviorSettingsSchema() map[string]any {
	properties := make(map[string]any, len(behaviorSettings))
	for _, setting := range behaviorSettings {
		if setting.random != nil {
			setting.description += "；群级 null 恢复整组继承，不可混用具体值"
		}
		switch setting.kind {
		case "bool":
			properties[setting.key] = map[string]any{"type": []string{"boolean", "null"}, "description": setting.description}
		case "int":
			properties[setting.key] = map[string]any{"type": []string{"integer", "null"}, "minimum": setting.min, "maximum": setting.max, "description": setting.description}
		}
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"description":          "回复行为设置，只写要改的项；群级传 null 表示清掉本群覆盖、跟随机器人设置。",
		"properties":           properties,
	}
}

// behaviorValue 是一项设置校验后的值；clear 表示群级清掉覆盖。
type behaviorValue struct {
	setting behaviorSetting
	clear   bool
	flag    bool
	number  int
}

func parseBehaviorSettings(raw any, scope string) ([]behaviorValue, error) {
	values, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("behavior 必须是对象")
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]behaviorValue, 0, len(keys))
	for _, key := range keys {
		setting, ok := behaviorSettingByKey(key)
		if !ok {
			return nil, fmt.Errorf("不支持的行为设置 %q，可改：%s", key, behaviorSettingsCatalog())
		}
		value := behaviorValue{setting: setting}
		if values[key] == nil {
			if scope != "group" {
				return nil, fmt.Errorf("%s：机器人级设置没有上一层可跟随，不能传 null", key)
			}
			value.clear = true
			out = append(out, value)
			continue
		}
		switch setting.kind {
		case "bool":
			flag, ok := values[key].(bool)
			if !ok {
				return nil, fmt.Errorf("%s 必须是 true 或 false", key)
			}
			value.flag = flag
		case "int":
			number, err := groupToolInteger(values[key])
			if err != nil || number < setting.min || number > setting.max {
				return nil, fmt.Errorf("%s 必须是 %d–%d 的整数", key, setting.min, setting.max)
			}
			value.number = number
		}
		out = append(out, value)
	}

	randomClear, randomSet := false, false
	for _, value := range out {
		if value.setting.random != nil {
			if value.clear {
				randomClear = true
			} else {
				randomSet = true
			}
		}
	}
	if randomClear && randomSet {
		return nil, fmt.Errorf("随机复读使用整组覆盖，不能同时恢复继承和设置具体值；请拆成两次操作")
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("behavior 至少要有一项")
	}
	return out, nil
}

func (v behaviorValue) setRandom(target *RandomRepeatConfig) {
	switch field := v.setting.random(target).(type) {
	case *bool:
		*field = v.flag
	case *int:
		*field = v.number
	}
}

// applyBehaviorToGroup 把校验过的值写进群配置。随机复读是一整份结构：群里第一次
// 改其中一项时，从当前实际生效的那份（机器人级）拷一份再改，其余项保持原样。
// 清掉其中任一项就是整份回到跟随机器人。
func applyBehaviorToGroup(group GroupConfig, effective BotConfig, values []behaviorValue) GroupConfig {
	for _, value := range values {
		setting := value.setting
		switch {
		case setting.random != nil:
			if value.clear {
				group.RandomRepeat = nil
				continue
			}
			current := effective.RandomRepeat.normalized()
			if group.RandomRepeat != nil {
				current = group.RandomRepeat.normalized()
			}
			value.setRandom(&current)
			group.RandomRepeat = copyRandomRepeat(&current)
		case setting.groupBool != nil:
			if value.clear {
				*setting.groupBool(&group) = nil
			} else {
				*setting.groupBool(&group) = boolPointer(value.flag)
			}
		case setting.groupInt != nil:
			if value.clear {
				*setting.groupInt(&group) = 0
			} else {
				*setting.groupInt(&group) = value.number
			}
		}
	}
	return group
}

// applyBehaviorToBot 是机器人级的同一件事，在配置存储的锁里调用。
func applyBehaviorToBot(cfg BotConfig, values []behaviorValue) BotConfig {
	for _, value := range values {
		setting := value.setting
		switch {
		case setting.random != nil:
			current := cfg.RandomRepeat.normalized()
			value.setRandom(&current)
			cfg.RandomRepeat = copyRandomRepeat(&current)
		case setting.botBool != nil:
			*setting.botBool(&cfg) = boolPointer(value.flag)
		case setting.botInt != nil:
			*setting.botInt(&cfg) = value.number
		}
	}
	return cfg
}

// behaviorSettingsView 列出每项实际生效的值，读取和改完回报都用它。group 非空时
// 另外标出哪些项是本群单独覆盖的，模型据此能说清「本群关了，机器人默认是开的」。
func behaviorSettingsView(effective BotConfig, group *GroupConfig) map[string]any {
	values := make(map[string]any, len(behaviorSettings))
	overridden := []string{}
	random := effective.RandomRepeat.normalized()
	for _, setting := range behaviorSettings {
		switch {
		case setting.random != nil:
			switch field := setting.random(&random).(type) {
			case *bool:
				values[setting.key] = *field
			case *int:
				values[setting.key] = *field
			}
			if group != nil && group.RandomRepeat != nil {
				overridden = append(overridden, setting.key)
			}
		case setting.botBool != nil:
			values[setting.key] = boolValue(*setting.botBool(&effective), setting.boolDefault)
			if group != nil && *setting.groupBool(group) != nil {
				overridden = append(overridden, setting.key)
			}
		case setting.botInt != nil:
			values[setting.key] = *setting.botInt(&effective)
			if group != nil && *setting.groupInt(group) != 0 {
				overridden = append(overridden, setting.key)
			}
		}
	}
	view := map[string]any{"values": values}
	if group != nil {
		view["group_overrides"] = overridden
	}
	return view
}

// behaviorSettingsCatalog 列出能改的项，报错时提示模型。
func behaviorSettingsCatalog() string {
	keys := make([]string, 0, len(behaviorSettings))
	for _, setting := range behaviorSettings {
		keys = append(keys, setting.key)
	}
	return strings.Join(keys, "、")
}
