package assistant

import (
	"regexp"
	"strings"
)

// slashCommandHead 认「/命令词」开头的消息：斜杠后紧跟字母、数字、下划线或汉字，可带
// Telegram 式的 @机器人名，之后是空白或结尾。「/Users/x 报错了」这种路径、「/ 你好」
// 这种斜杠后带空格的都不算。
var slashCommandHead = regexp.MustCompile(`^/[\p{L}\p{N}_]+(@[\w.]+)?(\s|$)`)

func looksLikeSlashCommand(text string) bool {
	return slashCommandHead.MatchString(strings.TrimSpace(text))
}

// foreignCommand 报告这条群消息是不是一条明显的斜杠指令、却不是这台机器人内置的：
// 插件指令、链接解析、状态卡片和主人命令都不认它。这种消息是发给群里别的机器人的
// （/ai、/查谱、/tarot@xxx），意图识别看不出指令归谁，曾把别的机器人的 /ai off
// 当成给自己的、真关掉了本群接话。所以这类消息不进答复流程，也不交给模型判断。
// @本机、回复本机或 /命令@本机 的（平台都会标成 ToMe）是在叫自己，照常进答复流程。
// 私聊里没有别的机器人，不拦。
func (r *Runtime) foreignCommand(event MessageEvent, text string) bool {
	if event.Kind != EventKindGroup || event.ToMe {
		return false
	}
	candidates := r.burstCommandTexts(event, firstNonEmpty(strings.TrimSpace(text), directedInboundText(event)))
	command := false
	for _, candidate := range candidates {
		if looksLikeSlashCommand(candidate) {
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
