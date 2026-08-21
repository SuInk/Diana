// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strconv"
	"strings"
	"time"
)

// 群准入模式。空串等同 blacklist，保证老配置行为完全不变。
const (
	GroupAdmissionBlacklist = "blacklist"
	GroupAdmissionWhitelist = "whitelist"
)

// 等级查不到时的策略。默认 allow（fail-open）。
const (
	LevelUnknownAllow = "allow"
	LevelUnknownDeny  = "deny"
)

// GroupAdmission 决定机器人在哪些群工作，全局唯一，不按群覆盖。
type GroupAdmission struct {
	// Mode 为 whitelist 时只在 AllowedGroups 里的群工作；
	// 为 blacklist（默认）时沿用原有的 DisabledGroups 语义。
	Mode string `json:"mode,omitempty"`
	// AllowedGroups 仅 whitelist 模式生效。
	AllowedGroups []string `json:"allowed_groups,omitempty"`
}

// ReplyGate 决定满足什么条件才回复，可全局配置并按群整体覆盖。
//
// 按群覆盖是整体替换而不是逐字段合并：和现有 SystemPrompt「非空即覆盖」
// 的风格一致，界面上一个「跟随全局 / 自定义」开关就能讲清楚；逐字段合并
// 会产生「时段是自己的、等级是全局的」这类难以表达的中间态。
type ReplyGate struct {
	// MinGroupLevel 是 群等级门槛（按群内活跃度累积），0 表示不限。
	// 注意这不是账号等级（太阳月亮星星），后者 OneBot 协议拿不到。
	MinGroupLevel int `json:"min_group_level,omitempty"`
	// LevelUnknownPolicy 决定拿不到等级时放行还是拦截，默认 allow。
	LevelUnknownPolicy string `json:"level_unknown_policy,omitempty"`
	// ExemptUsers 无视等级与时段门槛；BlockedUsers 始终不回。
	ExemptUsers  []string `json:"exempt_users,omitempty"`
	BlockedUsers []string `json:"blocked_users,omitempty"`

	ActiveHoursEnabled bool `json:"active_hours_enabled,omitempty"`
	// ActiveStart/ActiveEnd 为 24 小时制 HH:MM。End 小于 Start 表示跨夜，
	// 例如 22:00-06:00；两者相同视为全天开放。
	ActiveStart string `json:"active_start,omitempty"`
	ActiveEnd   string `json:"active_end,omitempty"`
	// Timezone 为 IANA 名称（如 Asia/Shanghai），留空用服务器本地时区。
	Timezone string `json:"timezone,omitempty"`
	// OwnerBypass 为 nil 时按 true 处理，主人不受时段和等级限制。
	OwnerBypass *bool `json:"owner_bypass,omitempty"`
	// QuietReply 留空表示静默期完全不出声。
	QuietReply string `json:"quiet_reply,omitempty"`
}

// WithDefaults 归一化准入配置，非法值一律退回不拦截的方向。
func (a GroupAdmission) WithDefaults() GroupAdmission {
	a.Mode = strings.ToLower(strings.TrimSpace(a.Mode))
	if a.Mode != GroupAdmissionWhitelist {
		a.Mode = GroupAdmissionBlacklist
	}
	a.AllowedGroups = cleanStrings(a.AllowedGroups)
	return a
}

// Allows 判断某个群是否在准入范围内。
func (a GroupAdmission) Allows(groupID string) bool {
	a = a.WithDefaults()
	if a.Mode != GroupAdmissionWhitelist {
		// 黑名单模式下由 DisabledGroups 负责拦截，这里一律放行。
		return true
	}
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return false
	}
	for _, allowed := range a.AllowedGroups {
		if allowed == groupID {
			return true
		}
	}
	return false
}

// WithDefaults 归一化回复门槛配置。
func (g ReplyGate) WithDefaults() ReplyGate {
	if g.MinGroupLevel < 0 {
		g.MinGroupLevel = 0
	}
	g.LevelUnknownPolicy = strings.ToLower(strings.TrimSpace(g.LevelUnknownPolicy))
	if g.LevelUnknownPolicy != LevelUnknownDeny {
		g.LevelUnknownPolicy = LevelUnknownAllow
	}
	g.ExemptUsers = cleanStrings(g.ExemptUsers)
	g.BlockedUsers = cleanStrings(g.BlockedUsers)
	g.ActiveStart = strings.TrimSpace(g.ActiveStart)
	g.ActiveEnd = strings.TrimSpace(g.ActiveEnd)
	g.Timezone = strings.TrimSpace(g.Timezone)
	g.QuietReply = strings.TrimSpace(g.QuietReply)
	// 时段配置本身不合法就别开着，否则会静默拦下所有消息。
	if g.ActiveHoursEnabled {
		if _, ok := parseClockMinutes(g.ActiveStart); !ok {
			g.ActiveHoursEnabled = false
		}
		if _, ok := parseClockMinutes(g.ActiveEnd); !ok {
			g.ActiveHoursEnabled = false
		}
	}
	return g
}

// Clone 深拷贝，避免配置与 payload 之间共享底层切片。
func (g *ReplyGate) Clone() *ReplyGate {
	if g == nil {
		return nil
	}
	copied := *g
	copied.ExemptUsers = append([]string(nil), g.ExemptUsers...)
	copied.BlockedUsers = append([]string(nil), g.BlockedUsers...)
	if g.OwnerBypass != nil {
		bypass := *g.OwnerBypass
		copied.OwnerBypass = &bypass
	}
	return &copied
}

// IsBlocked 判断用户是否在黑名单里。
func (g ReplyGate) IsBlocked(userID string) bool {
	return containsString(g.BlockedUsers, strings.TrimSpace(userID))
}

// IsExempt 判断用户是否豁免等级与时段门槛。
func (g ReplyGate) IsExempt(userID string) bool {
	return containsString(g.ExemptUsers, strings.TrimSpace(userID))
}

// OwnerBypassEnabled 在未显式配置时返回 true——否则时段或等级配错了，
// 主人自己也会被挡在门外，而 OneBot 侧没有任何补救手段。
func (g ReplyGate) OwnerBypassEnabled() bool {
	if g.OwnerBypass == nil {
		return true
	}
	return *g.OwnerBypass
}

// Location 解析时区，失败退回服务器本地时区。
func (g ReplyGate) Location() *time.Location {
	if g.Timezone == "" {
		return time.Local
	}
	loc, err := time.LoadLocation(g.Timezone)
	if err != nil {
		return time.Local
	}
	return loc
}

// WithinActiveHours 判断当前时刻是否在允许回复的时段内。
func (g ReplyGate) WithinActiveHours(now time.Time) bool {
	if !g.ActiveHoursEnabled {
		return true
	}
	start, okStart := parseClockMinutes(g.ActiveStart)
	end, okEnd := parseClockMinutes(g.ActiveEnd)
	if !okStart || !okEnd {
		// 配置不合法时不拦截，避免把机器人静默锁死。
		return true
	}
	if start == end {
		// 起止相同视为全天开放。
		return true
	}
	local := now.In(g.Location())
	current := local.Hour()*60 + local.Minute()
	if start < end {
		return current >= start && current < end
	}
	// 跨夜时段，例如 22:00-06:00。
	return current >= start || current < end
}

// LevelAllows 判断群等级是否达标。known 为 false 表示等级查不到，
// 此时按 LevelUnknownPolicy 处理而不是当成 0 拒绝。
func (g ReplyGate) LevelAllows(level int, known bool) bool {
	if g.MinGroupLevel <= 0 {
		return true
	}
	if !known {
		return g.LevelUnknownPolicy != LevelUnknownDeny
	}
	return level >= g.MinGroupLevel
}

// parseClockMinutes 把 HH:MM 解析成当天的分钟偏移。
func parseClockMinutes(value string) (int, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return 0, false
	}
	hour, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || hour < 0 || hour > 23 {
		return 0, false
	}
	minute, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || minute < 0 || minute > 59 {
		return 0, false
	}
	return hour*60 + minute, true
}

func containsString(list []string, value string) bool {
	if value == "" {
		return false
	}
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
