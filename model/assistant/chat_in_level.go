package assistant

import (
	"strings"
	"time"
)

// ChatInLevel 是闲聊插话的回复欲望档位。它只决定判定的松紧，不改变“插话必须有实质
// 内容”这条硬约束：任何档位下附和、复读和寒暄都不会被放行。
type ChatInLevel string

const (
	// ChatInLevelOff 完全关闭闲聊插话，行为与加入本功能之前一致。
	ChatInLevelOff ChatInLevel = "off"
	// ChatInLevelLow 只在很有把握时偶尔插一句。
	ChatInLevelLow ChatInLevel = "low"
	// ChatInLevelMedium 日常群聊里比较自然的插话频率。
	ChatInLevelMedium ChatInLevel = "medium"
	// ChatInLevelHigh 明显更主动，适合希望机器人多参与的群。
	ChatInLevelHigh ChatInLevel = "high"
	// ChatInLevelMax 几乎只要有话可说就插，容易刷屏，慎用。
	ChatInLevelMax ChatInLevel = "max"
)

// defaultChatInLevel 默认取最保守的开启档位：插话打断的是别人的对话，宁可让用户
// 主动往上调，也不要一上线就抢话。
const defaultChatInLevel = ChatInLevelLow

// chatInSettings 是某个事件最终生效的插话判定参数。
type chatInSettings struct {
	Enabled   bool
	Level     ChatInLevel
	Threshold float64
	Chance    float64
	Cooldown  time.Duration
}

type chatInLevelPreset struct {
	Threshold float64
	Chance    float64
	Cooldown  time.Duration
}

// chatInLevelPresets 各档位的默认判定参数。阈值越低、采样率越高、冷却越短，机器人
// 越爱说话。
var chatInLevelPresets = map[ChatInLevel]chatInLevelPreset{
	ChatInLevelLow:    {Threshold: 0.95, Chance: 0.35, Cooldown: 10 * time.Minute},
	ChatInLevelMedium: {Threshold: 0.88, Chance: 0.60, Cooldown: 5 * time.Minute},
	ChatInLevelHigh:   {Threshold: 0.80, Chance: 0.85, Cooldown: 2 * time.Minute},
	ChatInLevelMax:    {Threshold: 0.70, Chance: 1.00, Cooldown: 30 * time.Second},
}

// ChatInLevels 返回可选档位，供 WebUI 和聊天内工具展示。
func ChatInLevels() []ChatInLevel {
	return []ChatInLevel{ChatInLevelOff, ChatInLevelLow, ChatInLevelMedium, ChatInLevelHigh, ChatInLevelMax}
}

// Label 返回档位的中文说明。
func (level ChatInLevel) Label() string {
	switch level.Normalized() {
	case ChatInLevelOff:
		return "关闭：从不主动插话"
	case ChatInLevelLow:
		return "保守：很有把握时偶尔插一句"
	case ChatInLevelMedium:
		return "适中：日常群聊里自然参与"
	case ChatInLevelHigh:
		return "活跃：明显更主动"
	case ChatInLevelMax:
		return "话痨：有话可说就插，容易刷屏"
	default:
		return string(level)
	}
}

// Normalized 把任意输入收敛成合法档位，无法识别时返回空值交由调用方补默认档。
func (level ChatInLevel) Normalized() ChatInLevel {
	switch strings.ToLower(strings.TrimSpace(string(level))) {
	case "off", "none", "disabled", "关闭", "关":
		return ChatInLevelOff
	case "low", "conservative", "保守", "低":
		return ChatInLevelLow
	case "medium", "normal", "适中", "中":
		return ChatInLevelMedium
	case "high", "active", "活跃", "高":
		return ChatInLevelHigh
	case "max", "chatty", "话痨", "最高":
		return ChatInLevelMax
	default:
		return ""
	}
}

// Valid 表示该档位可以直接使用。
func (level ChatInLevel) Valid() bool {
	return level.Normalized() != ""
}

// chatInSettingsFrom 解析开关、档位和自定义覆盖，得到最终生效参数。显式设置的
// threshold/chance/cooldown 覆盖档位预设，这样“多档位”和“精细自定义”可以共存。
func chatInSettingsFrom(enabled *bool, level ChatInLevel, threshold, chance float64, cooldownSeconds int) chatInSettings {
	normalized := level.Normalized()
	if normalized == "" {
		normalized = defaultChatInLevel
	}
	settings := chatInSettings{Enabled: boolValue(enabled, true), Level: normalized}
	if normalized == ChatInLevelOff {
		settings.Enabled = false
	}
	preset := chatInLevelPresets[normalized]
	if preset.Threshold <= 0 {
		preset = chatInLevelPresets[defaultChatInLevel]
	}
	settings.Threshold = preset.Threshold
	settings.Chance = preset.Chance
	settings.Cooldown = preset.Cooldown
	if threshold > 0 && threshold <= 1 {
		settings.Threshold = threshold
	}
	if chance > 0 && chance <= 1 {
		settings.Chance = chance
	}
	if cooldownSeconds > 0 {
		settings.Cooldown = time.Duration(cooldownSeconds) * time.Second
	}
	return settings
}

// chatInReplyPrompt 是闲聊插话专用的回复约束。没人在叫机器人，所以这条回复必须靠
// 内容本身站住，语气也要像顺口接一句，而不是被提问后作答。
const chatInReplyPrompt = `本次回复是主动插话：没有人 @ 你或向你提问，是你自己判断这里有一句值得说的话。请遵守：
只说那句有实质内容的话——具体事实、明确纠正、有用信息或确实接住上文的梗，说完就停。
不要复述别人刚说过的内容，不要附和、捧场、总结全场或发表泛泛感想。
控制在一到两句，像群友顺口接一句，不要用"我来补充一下""需要我帮你查吗"这类开场白和收尾问句。
不要追问、不要索要更多信息、不要试图把话题接管过来；对方没有义务回你。
如果写到一半发现自己其实没有实质内容可说，直接输出简短拒绝说明并附加 [[DIANA_REFUSE_CURRENT]]，不要硬凑一句。`

// clampChatInRatio 归一化自定义比例。0 表示未设置，交给档位预设决定。
func clampChatInRatio(value float64) float64 {
	switch {
	case value <= 0:
		return 0
	case value > 1:
		return 1
	default:
		return value
	}
}

// chatInSettings 返回本条配置生效的闲聊插话参数。
func (cfg BotConfig) chatInSettings() chatInSettings {
	return chatInSettingsFrom(cfg.ChatInEnabled, cfg.ChatInLevel, cfg.ChatInThreshold, cfg.ChatInChance, cfg.ChatInCooldownSeconds)
}
