// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "strings"

// 视觉模型拿不到图片时不会报错：请求照常返回 200，正文是一段「我没收到图片，请重新
// 发送」。这段话和一条正常描述长得一模一样，于是被当成视觉事实按图片内容哈希写进缓存。
// 之后同一张图再出现，任何路径都直接命中缓存、不再调模型，机器人便一直坚称用户没发图，
// 换成能看图的模型也救不回来。
//
// 2026-09-21 生产上就是这样：一张已经正常入库、WebUI 能看到缩略图的群图，Diana 回复
// 「图片又没传过来」。根因是 vision 分组里混进了不支持图片输入的模型（网关把 image_url
// 段整个丢掉，请求的 input_tokens 每次都恰好是纯文本那一档），运行时看不出区别，只能
// 从正文认出来。
//
// 所以在写任何缓存之前先把这类回答判成失败：宁可这一轮没有描述，也不要把错误答案永久
// 留下。已经落库的旧行由 PurgeRefusedImageDescriptions 清掉。

// visionRefusalHeadRunes 限定只看回答的开头，visionRefusalSentenceBreaks 进一步限定只看
// 第一句。拒答一定开门见山，两道限制都满足才算数。
//
// 两道都需要：一段描述聊天截图的正文很容易在第二句里转述「图片未收到，麻烦重发」，靠
// 「只看第一句」挡掉；而一句没有句号的长英文描述（"a terminal showing: no image found
// in cache"）整句都是第一句，靠 32 字的窗口挡掉。误判只是丢掉这一次的描述、下次重新
// 识别，代价远小于把一句拒答永久缓存成视觉事实。
const visionRefusalHeadRunes = 32

var visionRefusalSentenceBreaks = []rune{'。', '.', '！', '!', '？', '?', '\n', '：', ':'}

// visionRefusalMissingMarkers 是「没拿到」这一层语义，必须和下面的图片主语同时出现。
var visionRefusalMissingMarkers = []string{
	"未收到", "没有收到", "没收到", "未能收到", "未接收到",
	"未附带", "没有附带", "未提供", "没有提供",
	"未检测到", "没有检测到",
	"看不到", "看不见", "无法查看", "无法看到", "无法获取", "无法读取", "无法访问",
	"无法生成描述", "没有可解析",
}

// visionRefusalSubjectMarkers 是图片主语。写全「图片」而不是单字「图」：模型看得见图
// 但读不出图上小字时会说「看不到图中的文字」，那是一条有效描述的开头，不能当成拒答。
var visionRefusalSubjectMarkers = []string{"图片", "图像", "image", "picture", "attachment"}

// visionRefusalEnglishPhrases 已经自带主语，单独命中即可。
var visionRefusalEnglishPhrases = []string{
	"no image", "not receive", "didn't receive", "did not receive",
	"don't see any image", "do not see any image", "don't see an image",
	"unable to view", "unable to see", "cannot see the image", "can't see the image",
	"no attachment", "wasn't provided", "was not provided",
}

// VisionDescriptionRefused 判断视觉模型的回答是不是「我没收到图片」这类拒答。
func VisionDescriptionRefused(text string) bool {
	// 弯引号换成直引号，"don't" 和 "don’t" 才能用同一条规则匹配。
	head := strings.ToLower(strings.ReplaceAll(visionRefusalHead(text), "’", "'"))
	if head == "" {
		return false
	}
	if containsAnyMarker(head, visionRefusalEnglishPhrases) {
		return true
	}
	return containsAnyMarker(head, visionRefusalMissingMarkers) &&
		containsAnyMarker(head, visionRefusalSubjectMarkers)
}

func containsAnyMarker(text string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// visionRefusalHead 取开头 visionRefusalHeadRunes 个字，并在其中第一个句末标点处截断。
// 冒号也算句末：拒答常写成「未收到任何图片内容：当前消息中……」，后半句已经是解释了。
func visionRefusalHead(text string) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) > visionRefusalHeadRunes {
		runes = runes[:visionRefusalHeadRunes]
	}
	for index, current := range runes {
		for _, separator := range visionRefusalSentenceBreaks {
			if current == separator {
				return string(runes[:index])
			}
		}
	}
	return string(runes)
}
