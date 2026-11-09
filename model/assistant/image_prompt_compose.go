// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"strings"

	"github.com/SuInk/diana/model/llm"
)

// 画幅。图片模型按 size 出图，以前写死 1024x1024：用户要竖版海报、改一张竖版梗图，
// prompt 里写着「竖向画面」，成品还是正方形，只能裁掉或压扁。
const (
	dianaImageAspectSquare    = "square"
	dianaImageAspectPortrait  = "portrait"
	dianaImageAspectLandscape = "landscape"
)

// dianaImageSize 按格式和画幅挑 size，Gemini 会把它换算成宽高比。
//
// GIF 不看画幅：gpt-image-2.6 把所有帧拼在一张精灵图里一起画，size 指的是单帧，
// 9 帧 × 1024 拼出 3072×3072 超了像素预算，上游直接 400（实测 "No supported sheet
// fits 9 GIF frames at size '1024x1024'"）。auto 让上游按帧数挑能放下的单帧尺寸，
// 9 帧和 16 帧实测都给 512×512。
func dianaImageSize(format, aspect string) string {
	if format == llm.ImageFormatGIF {
		return "auto"
	}
	switch aspect {
	case dianaImageAspectPortrait:
		return "1024x1536"
	case dianaImageAspectLandscape:
		return "1536x1024"
	}
	return "1024x1024"
}

func normalizeDianaImageAspect(raw string) string {
	switch aspect := strings.ToLower(strings.TrimSpace(raw)); aspect {
	case dianaImageAspectPortrait, dianaImageAspectLandscape:
		return aspect
	}
	return dianaImageAspectSquare
}

// promptImageReferenceOrder 接在多图合成的提示词后面。
//
// 图片模型只收到一串没有名字的图。主模型写 prompt 时说「以截图里的头像为构图参考，
// 换成另一张参考头像里的角色」「以用户头像形象及所附图片为参考」，图片模型得自己猜
// 哪张是底图、哪张是头像。按传入顺序列出每张图的来历，这些说法就有了着落。
//
// 它替掉了以前接在每张图后面的「群聊上下文」：群名、群号、成员 QQ 号和头像链接。
// 图片模型打不开链接，号码对画图没用，只是把账号发给了图片服务；那段还写着「不要在
// 图片中加入文字，除非用户明确要求」，可图片模型看不到用户说了什么，prompt 里明确
// 要写的字反倒被它压下去。
const promptImageReferenceOrder = "参考图按传入顺序（提示词里说的底图、头像、截图等按这里对应）："

var promptImageReferenceOrderSpec = registerPrompt(PromptSpec{
	Key:     "media.image_reference_order",
	Group:   PromptGroupMedia,
	Title:   "多张参考图的顺序说明",
	Usage:   "改图时一次交给图片模型两张及以上参考图，接在提示词后面、逐张来历前面，让图片模型知道每张图是什么。",
	Default: promptImageReferenceOrder,
})

// imageReferenceOrderLines 按传入顺序给每张参考图写一行来历。来历表和图对不上（去重、
// 截断后张数变了）就不写：标错比不标更糟，会让图片模型把 A 的头像当成底图。
func imageReferenceOrderLines(used []imageEditSourceUsed, sourceCount int) []string {
	if sourceCount < 2 || len(used) == 0 {
		return nil
	}
	var labels []string
	for _, item := range used {
		count := item.Images
		if count <= 0 {
			count = 1
		}
		label := imageReferenceLabel(item)
		for range count {
			labels = append(labels, label)
		}
	}
	if len(labels) != sourceCount {
		return nil
	}
	lines := make([]string, 0, len(labels))
	for index, label := range labels {
		lines = append(lines, fmt.Sprintf("第%d张：%s", index+1, label))
	}
	return lines
}

// imageReferenceLabel 只写图的来历和昵称，不写账号 ID：ID 对图片模型没有意义。
func imageReferenceLabel(item imageEditSourceUsed) string {
	user := strings.TrimSpace(item.User)
	from := ""
	if user != "" {
		from = "（" + user + " 发的）"
	}
	switch item.Kind {
	case imageSourceKindCurrentMessage:
		return "当前消息里的图片" + from
	case imageSourceKindQuoted, imageSourceKindQuotedChain:
		return "被引用消息里的图片" + from
	case avatarSourceBot:
		return "机器人自己的头像"
	case avatarSourceSender:
		if user != "" {
			return "发送者 " + user + " 的头像"
		}
		return "发送者的头像"
	case avatarSourceGroup:
		return "群头像"
	case "member_avatar":
		if user != "" {
			return user + " 的头像"
		}
		return "一位群成员的头像"
	}
	return "聊天里的一张图片" + from
}

// composeImageEditPrompt 是真正交给图片模型的改图提示词：一次合成多张参考图时，
// 在模型写的 prompt 后面补上每张图的来历；单张或逐张模式原样返回。
func composeImageEditPrompt(prompt string, used []imageEditSourceUsed, sourceCount int, cfg BotConfig) string {
	prompt = strings.TrimSpace(prompt)
	lines := imageReferenceOrderLines(used, sourceCount)
	if len(lines) == 0 {
		return prompt
	}
	return prompt + "\n\n" + cfg.prompt(promptImageReferenceOrderSpec) + "\n" + strings.Join(lines, "\n")
}
