// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "time"

// 同一个人在凌晨三点和下午三点说话不是一个样子：话会变少、反应会慢、句子会松。
// 机器人全天一个语气，是那种说不出哪里不对但就是不像人的地方之一。
//
// 这里只调「精力和节奏」，不碰身份和口癖。原因是它对所有表达风格生效：给助手
// 风格塞一句「困了」会很怪，但「深夜话少一点、句子短一点」放在哪一档都成立。
// 具体口癖属于风格和人设，各自那边已经有地方写了。
//
// 默认关闭。按时钟改变语气是能被用户感知的行为变化，不该在升级后突然发生。

type dayPart int

const (
	dayPartLateNight dayPart = iota
	dayPartMorning
	dayPartDaytime
	dayPartEvening
)

// dayPartAt 按小时分四段。边界取整点，不做渐变——渐变在提示词这个粒度上没有意义，
// 模型读到的只是一段文字。
func dayPartAt(now time.Time) dayPart {
	switch hour := now.Hour(); {
	case hour < 5:
		return dayPartLateNight
	case hour < 9:
		return dayPartMorning
	case hour < 18:
		return dayPartDaytime
	default:
		return dayPartEvening
	}
}

const (
	promptDayPartLateNight = "现在是深夜。你这个点是醒着的，但精力不多：话比白天少，句子更短更松，反应慢半拍，容易顺着对方的情绪走而不是急着解决问题。可以提到你这边晚了、困了，但不要每条都提；这是你自己的作息，不是对方的——没有依据说明对方也在这个时区时，别断言他那边是深夜，也别催他睡或说他熬夜。对方这个点还在说话，多半是有心事或者睡不着，别催他，也别装得很精神。"
	promptDayPartMorning   = "现在是清早。你刚醒不久，脑子还没完全开机：反应比平时慢一点，句子短，可以有点迷糊。别装出一副精神饱满的样子，也别因为迷糊就把正事答错——需要动脑的问题照常答准，只是语气松一些。刚醒的是你：没有依据说明对方也在这个时区时，别默认他也刚起床。"
	// 只写语气，不写篇幅。这一档从 18:00 一直盖到午夜，正好是群聊最热闹的时段，
	// 以前那句「话可以多一点，更愿意闲聊和展开」和群聊里的「尽量简短」、插话
	// 节奏里的「一两句说完」正面冲突：线上抓到的主动插话中位数 75 字、26%
	// 超过两句，晚上的长度失控基本都能追到这句话。
	promptDayPartEvening = "现在是晚上。一天的事忙完了，你比白天松弛，更愿意接梗，但回复长度照旧。"
)

var (
	promptDayPartLateNightSpec = tailSpec("daypart.late_night", "时段语气：深夜", "开启时段语气、机器人这边 0–5 点时放在尾部。", promptDayPartLateNight)
	promptDayPartMorningSpec   = tailSpec("daypart.morning", "时段语气：清早", "开启时段语气、机器人这边 5–9 点时放在尾部。", promptDayPartMorning)
	promptDayPartEveningSpec   = tailSpec("daypart.evening", "时段语气：晚上", "开启时段语气、机器人这边 18 点以后时放在尾部。白天不注入任何时段语气。", promptDayPartEvening)
)

// spec 返回这个时段对应的语气条目。白天是基线，没有条目。
func (part dayPart) spec() *PromptSpec {
	switch part {
	case dayPartLateNight:
		return promptDayPartLateNightSpec
	case dayPartMorning:
		return promptDayPartMorningSpec
	case dayPartEvening:
		return promptDayPartEveningSpec
	}
	return nil
}

// prompt 返回这个时段的内置语气，按配置覆盖的版本见 dayPartToneForConfig。
func (part dayPart) prompt() string {
	return part.promptWith(nil)
}

func (part dayPart) promptWith(overrides PromptOverrides) string {
	spec := part.spec()
	if spec == nil {
		return ""
	}
	return overrides.text(spec)
}

// dayPartTonePrompt 返回这一刻要注入的时段语气。关掉、或者时段没有主张时返回空串。
func dayPartTonePrompt(enabled bool, now time.Time) string {
	if !enabled {
		return ""
	}
	// 白天不注入任何东西：那是基线，各风格自己的描述说的就是白天的样子，
	// 再补一句「现在是白天，正常说话」只是白占 token。
	return dayPartAt(now).prompt()
}

// dayPartToneForConfig 按机器人配置算出这一刻的时段语气。
//
// 时区复用回复门槛那份（ReplyGate.Timezone）：一台机器人不该有两个「几点了」。
// 门槛没配时 Location() 退回服务器本地时区。
func dayPartToneForConfig(cfg BotConfig, now time.Time) string {
	// 接管档下「怎么说话」全归正文，时段语气这个开关跟着一起不生效。
	if cfg.PersonaMode.ownsPersonaVoice() || !boolValue(cfg.DaypartToneEnabled, false) {
		return ""
	}
	location := time.Local
	if cfg.ReplyGate != nil {
		location = cfg.ReplyGate.Location()
	}
	// 白天不注入任何东西，理由同 dayPartTonePrompt。
	return dayPartAt(now.In(location)).promptWith(cfg.PromptOverrides)
}
