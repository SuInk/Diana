package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/applog"
)

const dianaUsageToolName = "llm_usage"

type dianaUsageTool struct {
	runtime *Runtime
	event   MessageEvent
	now     func() time.Time
}

func (t *dianaUsageTool) Name() string { return dianaUsageToolName }
func (t *dianaUsageTool) Description() string {
	return "查询真实 LLM Token 用量与群排行，仅主人。默认实例最近24小时；可查当前群或指定群、机器人和时间窗口。包含后台调用，未标记归属的历史用量不分配给群。"
}
func (t *dianaUsageTool) InputSchema() map[string]any {
	return toolObjectSchema(nil, map[string]any{
		"scope":      toolEnumParam("统计范围，默认 all；group 配合 group_id。", "all", "current_group", "group"),
		"group_id":   toolStringParam("指定群 ID。"),
		"profile_id": toolStringParam("机器人 ID；群查询默认当前机器人。"),
		"hours":      toolIntParam("距今小时数，默认24，最多2160。", 1, 2160),
	})
}
func (t *dianaUsageTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("用量查询运行时不可用")
	}
	if !t.runtime.relationshipPolicy(ctx, t.event).Owner {
		return "", fmt.Errorf("只有主人可以查看 Token 用量")
	}
	for key := range input {
		if key != "scope" && key != "group_id" && key != "profile_id" && key != "hours" {
			return "", fmt.Errorf("未知用量查询参数 %q", key)
		}
	}
	scope := strings.TrimSpace(configToolString(input, "scope"))
	group := strings.TrimSpace(configToolString(input, "group_id"))
	if scope == "" {
		if group != "" {
			scope = "group"
		} else {
			scope = "all"
		}
	}
	filter := applog.UsageFilter{ProfileID: strings.TrimSpace(configToolString(input, "profile_id"))}
	switch scope {
	case "all":
		if group != "" {
			return "", fmt.Errorf("all 范围不能指定 group_id")
		}
	case "current_group":
		if t.event.Kind != EventKindGroup || t.event.GroupID == "" {
			return "", fmt.Errorf("当前会话不是群聊")
		}
		if group != "" && group != t.event.GroupID {
			return "", fmt.Errorf("current_group 不能指定别的群")
		}
		filter.GroupID = t.event.GroupID
	case "group":
		if group == "" {
			return "", fmt.Errorf("group 范围需要 group_id")
		}
		filter.GroupID = group
	default:
		return "", fmt.Errorf("未知用量范围 %q", scope)
	}
	if filter.GroupID != "" && filter.ProfileID == "" {
		filter.ProfileID = t.runtime.eventProfileID(t.event)
	}
	if filter.GroupID != "" && scope == "current_group" {
		filter.Platform = t.runtime.currentPlatform(t.event)
	}
	hours := 24
	if value, ok := input["hours"]; ok {
		switch v := value.(type) {
		case float64:
			if v != float64(int(v)) {
				return "", fmt.Errorf("hours 必须是整数")
			}
			hours = int(v)
		case int:
			hours = v
		default:
			return "", fmt.Errorf("hours 必须是整数")
		}
		if hours < 1 || hours > 2160 {
			return "", fmt.Errorf("hours 必须在1到2160之间")
		}
	}
	writer := t.runtime.appLogWriter()
	now := time.Now()
	if t.now != nil {
		now = t.now()
	}
	since := now.Add(-time.Duration(hours) * time.Hour)
	var stats applog.UsageSummary
	var groups []applog.GroupTokenUsage
	var err error
	if reader, ok := writer.(applog.UsageReportReader); ok {
		var report applog.UsageReport
		report, err = reader.LLMUsageReport(ctx, filter, since, now)
		stats, groups = report.Usage, report.Groups
	} else if scope == "all" && filter.ProfileID == "" {
		if reader, ok := writer.(applog.UsageReader); ok {
			stats, err = reader.LLMUsageSince(ctx, since, now)
		} else {
			err = fmt.Errorf("用量日志存储不可用")
		}
	} else {
		err = fmt.Errorf("用量日志存储不支持按群筛选")
	}
	if err != nil {
		return "", fmt.Errorf("读取用量统计失败: %w", err)
	}
	body, err := json.Marshal(map[string]any{
		"scope": scope, "profile_id": filter.ProfileID, "group_id": filter.GroupID, "usage": stats, "groups": groups,
		"notes": "仅统计已记录调用；未标记机器人或群归属的历史记录不会分配到群；私聊与未归属后台调用计入实例合计，不进群排行。缓存命中已包含在输入量中，不重复相加；供应商未报用量会使 Token 合计偏少。不是供应商账单或剩余额度。",
	})
	return string(body), err
}
