// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

// 她长什么样：默认就是她自己的头像。
//
// 内置人设都不写外貌——同一份人设会套在不同的机器人上，每台的头像不一样，写死一种
// 发色发型，画出来就和群里看到的头像对不上。以前人设里不写，模型被问「你长什么样」
// 「画一下你自己」时只能现编。头像才是大家天天看到的她，所以没写长相时就照头像来；
// 看头像、拿头像当底图的办法两个工具里都现成有，这里只是把它们指给模型。
//
// 头像地址不写进提示词：Telegram 这类平台没有公开链接，写了模型也会照着格式编一个。
// 由 remote_image 的 view_avatar 和 image 的 identity_sources 按平台去取。
const promptSelfAvatar = "你的形象默认就是你自己的头像。人设里没写长相时，有人问你长什么样，先用 remote_image 的 action=view_avatar、avatar_source=" + avatarSourceBot + " 看一眼自己的头像再照着说，别凭空描述；让你画自己、或者画的图里要有你，用 image 的 operation=edit、identity_sources 填 " + avatarSourceBot + "，以你的头像为底来画。人设里写了长相的，以人设为准。"

var promptSelfAvatarSpec = registerPrompt(PromptSpec{
	Key:     "reply.self_avatar",
	Group:   PromptGroupReplyBase,
	Title:   "默认形象：自己的头像",
	Usage:   "能看头像（remote_image）时紧跟在人设后面：没写长相就照机器人自己的头像说、照头像画。",
	Default: promptSelfAvatar,
})
