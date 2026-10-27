// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// Keep execution state separate from the model's account of what it has done.
// This temporary request context is rebuilt from actual callbacks; it is never
// saved as user speech, a notebook fact, or another source for history search.
// It is evidence guidance, not a semantic factuality classifier.
func newReplyRunHooks() agent.RunHooks {
	type execution struct {
		Tool   string            `json:"tool"`
		Status string            `json:"status"`
		Target map[string]string `json:"target,omitempty"`
	}
	var records []execution
	return agent.RunHooks{
		AfterToolCall: func(_ context.Context, step agent.Step) {
			status := "success"
			if step.Error != "" {
				status = "failed"
			}
			if step.Skipped {
				status = "not_executed"
			}
			target := make(map[string]string)
			// Do not copy arbitrary inputs, credentials, long documents, or output.
			for _, key := range []string{"operation", "scope", "group_id", "target_user_id", "user_id"} {
				if value, ok := step.Input[key].(string); ok && value != "" {
					target[key] = truncateChatHistoryText(value, 100)
				}
			}
			records = append(records, execution{Tool: step.Tool, Status: status, Target: target})
		},
		PrepareRequest: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateRequest, error) {
			start := max(0, len(records)-12)
			data, err := json.Marshal(struct {
				Total   int         `json:"total"`
				Omitted int         `json:"omitted"`
				Recent  []execution `json:"recent"`
			}{len(records), start, records[start:]})
			if err != nil {
				return req, err
			}
			note := llm.Message{Role: llm.RoleSystem, Priority: llm.MessagePrioritySystem, Content: "【本轮工具执行状态，由运行时生成】\n" + string(data)}
			// Keep the current user input last, but never split a native tool batch.
			at := len(req.Messages)
			if at > 0 && req.Messages[at-1].Role == llm.RoleUser {
				at--
			}
			messages := make([]llm.Message, 0, len(req.Messages)+1)
			messages = append(messages, req.Messages[:at]...)
			messages = append(messages, note)
			req.Messages = append(messages, req.Messages[at:]...)
			return req, nil
		},
	}
}

// toolExecutionStatusRule 说明「本轮工具执行状态」怎么读，逐字不变，放在稳定头部；
// 每步变化的回执 JSON 留在尾部。
const toolExecutionStatusRule = "「本轮工具执行状态」由运行时生成，不是资料正文。success 仅表示对应工具返回成功，具体结论仍须核对其结果、对象、实际范围和完整性；failed/not_executed 不算查证成功。total=0 表示本轮尚无工具执行回执，不表示已有历史事实都无效，但不能声称本轮已经查询或完成操作。旧回复中的自述不等于执行回执，也不是人物资料或统计的独立证据。"
