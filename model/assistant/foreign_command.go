package assistant

import (
	"regexp"
	"strings"
)

// foreignCommandHead 只认有明确命令前缀的写法。斜杠命令可带 Telegram 式的
// @机器人名；点号命令只认常见的小写短命令，避免把“.NET 9”当成机器人指令；#
// 只认整条消息就是一个命令词，避免吞掉后面还有正文的话题标签。
// 「/etc/hosts 报错了」这种路径、「/ 你好」这种斜杠后带空格的都不算。
var foreignCommandHead = regexp.MustCompile(`^(?:/[\p{L}\p{N}_]+(?:@[\w.]+)?(?:\s|$)|![\p{L}\p{N}_]+(?:\s|$)|\.[a-z][a-z0-9_]{0,15}(?:\s|$)|#[\p{L}\p{N}_]+$)`)

func looksLikeForeignCommand(text string) bool {
	text = strings.TrimSpace(text)
	return !isStatusCommand(text) && foreignCommandHead.MatchString(text)
}

// foreignCommand 报告这条群消息是不是一条明显的外部指令、却不是这台机器人内置的：
// 插件指令、链接解析、状态卡片和主人命令都不认它。这种消息是发给群里别的机器人的
// （/ai、#签到、.r、!help），意图识别看不出指令归谁，曾把别的机器人的 /ai off
// 当成给自己的、真关掉了本群接话。所以这类消息不进答复流程，也不交给模型判断，
// 且不需要先认出目标账号是不是机器人。普通 @、回复和群聊仍交给接话判断，可以评价。
// @本机、回复本机或 /命令@本机 的（平台都会标成 ToMe）是在叫自己，照常进答复流程。
// 私聊里没有别的机器人，不拦。
func (r *Runtime) foreignCommand(event MessageEvent, text string) bool {
	if event.Kind != EventKindGroup || event.ToMe {
		return false
	}
	candidates := r.burstCommandTexts(event, firstNonEmpty(strings.TrimSpace(text), directedInboundText(event)))
	command := false
	for _, candidate := range candidates {
		if looksLikeForeignCommand(candidate) {
			command = true
			break
		}
	}
	if !command {
		return false
	}
	if r.statusCommandActive(event, text) || r.wouldHandleOwnerCommand(event, text) ||
		r.isOwnerReplySuppressionCommand(event, text) || r.isOwnerContextResetCommand(event, text) {
		return false
	}
	for _, candidate := range candidates {
		if r.shouldHandleResolver(event, candidate) || r.shouldHandlePlugin(event, candidate) {
			return false
		}
	}
	return true
}
