// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "fmt"

// MaxInputCharsUnlimited 表示单次输入不限制长度，也是默认值。
const MaxInputCharsUnlimited = -1

// MinMaxInputChars 是填了上限时的最小值，防止填得太小把正常消息也截断。
const MinMaxInputChars = 1000

// truncateInputText 把超长的当前消息截到上限，并在末尾告诉模型后面还有内容、
// 怎么读全文。不加说明的话，模型会把半截消息当成完整的来回答。
func truncateInputText(text, messageID string, limit int) string {
	runes := []rune(text)
	if limit <= 0 || len(runes) <= limit {
		return text
	}
	notice := fmt.Sprintf("\n……（这条消息共 %d 字，超过单次输入上限 %d 字，以上只是开头", len(runes), limit)
	if messageID != "" {
		notice += fmt.Sprintf("；需要后面的内容时调 chat_history，action=around、message_id=%s 读全文", messageID)
	}
	notice += "；没读全文就回答时，要告诉对方你只看了开头"
	return string(runes[:limit]) + notice + "）"
}
