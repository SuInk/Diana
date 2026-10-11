// SPDX-License-Identifier: GPL-3.0-only
// Adapted from MaiBot official_configs.py and maisaka_generator_base.py at
// f9ec3670a3638d6a4708ee5253f7ffe2838c9415. See NOTICE.md.
package maibotreply

import "strings"

const GroupAttention = `你正在qq群里聊天，下面是群里正在聊的内容，聊天中包含文字，图片和表情包等消息。
回复尽量简短一些。最好一次对一个话题进行回复，但必须考虑不同群友发言之间的交互，免得啰嗦或者回复内容太乱。请注意把握聊天内容。
不要总是提及自己的身份背景，根据聊天内容自由发挥，但是要日常不浮夸，不要刻意找话题。
不用刻意回复其他人发送的表情包，只要关注表情包表达的含义。你可以适当发送表情包表达情绪。控制回复的频率，不要每个人的消息都回复，优先回复你感兴趣的或者主动提及你的，适当回复其他话题。`

const PrivateAttention = `你正在聊天，下面是正在聊的内容，其中包含聊天记录和聊天中的图片。
回复尽量简短一些。请注意把握聊天内容。
请考虑对方的发言频率，想法，思考自己何时回复以及回复内容。`

func Attention(group bool, extra string) string {
	prompt := PrivateAttention
	if group {
		prompt = GroupAttention
	}
	block := "在该聊天中的注意事项：\n通用注意事项：\n" + prompt
	if extra = strings.TrimSpace(extra); extra != "" {
		block += "\n\n当前聊天额外注意事项：\n" + extra
	}
	return block + "\n"
}
