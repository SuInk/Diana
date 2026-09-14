// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "context"

// RunChatHistoryToolForTest 只在测试编译时导出，给需要真实存储的外部测试包调用聊天记录工具。
func RunChatHistoryToolForTest(r *Runtime, event MessageEvent, input map[string]any) (string, error) {
	return newDianaChatHistoryTool(r, event).Run(context.Background(), input)
}
