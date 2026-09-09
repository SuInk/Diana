package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/SuInk/diana/model/applog"
)

const dianaUsageToolName = "diana.llm_usage"

type dianaUsageTool struct {
	runtime *Runtime
	event   MessageEvent
	now     func() time.Time
}

func (t *dianaUsageTool) Name() string { return dianaUsageToolName }
func (t *dianaUsageTool) Description() string {
	return "仅主人可查询当前 Diana 实例最近 24 小时的 LLM Token 用量，跨机器人与模型合计。主人问最近一天消耗多少 token、输入输出量或缓存命中时调用。以工具实际结果回答，不估算、不当作供应商账户账单，也不要称为今天零点以来的用量。"
}
func (t *dianaUsageTool) InputSchema() map[string]any { return toolObjectSchema(nil, map[string]any{}) }
func (t *dianaUsageTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("用量查询运行时不可用")
	}
	if !t.runtime.relationshipPolicy(ctx, t.event).Owner {
		return "", fmt.Errorf("只有主人可以查看 Token 用量")
	}
	if len(input) != 0 {
		return "", fmt.Errorf("用量查询不接受参数，固定查询最近 24 小时")
	}
	reader, ok := t.runtime.appLogWriter().(applog.UsageReader)
	if !ok {
		return "", fmt.Errorf("用量日志存储不可用，不能确定 Token 用量")
	}
	now := time.Now()
	if t.now != nil {
		now = t.now()
	}
	stats, err := reader.LLMUsageSince(ctx, now.Add(-24*time.Hour), now)
	if err != nil {
		return "", fmt.Errorf("读取用量统计失败: %w", err)
	}
	body, err := json.Marshal(map[string]any{
		"scope": "当前 Diana 实例全部机器人与模型",
		"usage": stats,
		"notes": "仅统计窗口内已写入日志的调用；包含路由、子任务等用途，不仅是最终回复。缓存命中已包含在输入量中，不要重复相加。日志清理或供应商未返回用量会使统计不完整；当前尚未完成的回复不包含在内。不是账户账单或剩余额度。",
	})
	return string(body), err
}
