// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/SuInk/diana/model/applog"
)

const promptPersistentVoice = "你这条回复会被合成为语音发出去：写成能直接念出口的口语，不用 Markdown、列表、表情符号和括号里的动作描写，不超过 {max_chars} 字。确实要给链接、代码或长清单时照常写，这种回复会改发文字。"

var promptPersistentVoiceSpec = tailSpec("persistent_voice", "常驻语音模式", "语音合成插件的发送模式设为「常驻」时注入：告诉模型回复会被念出来，写成口语、控制字数。",
	promptPersistentVoice,
	PromptVar{Name: "max_chars", Description: "语音插件的单次合成字数上限"})

// voiceTTSToolForEvent 取这条消息生效的语音合成工具：机器人和群两级的开关、设置
// 都已经合并好。插件没开时 ok 为 false；没有插件管理器（测试和嵌入场景）时按环境
// 变量配置合成。
func (r *Runtime) voiceTTSToolForEvent(event MessageEvent) (*dianaTTSTool, bool) {
	r.mu.RLock()
	localMedia := r.localMedia
	r.mu.RUnlock()
	var plugin *VoiceTTSPlugin
	var settings SettingValues
	if r.plugins != nil {
		pluginValue, effectiveSettings, enabled := r.plugins.PluginWithSettingsForGroup(
			voiceTTSPluginID,
			r.pluginOverridesForEvent(event),
			r.pluginSettingOverridesForEvent(event),
		)
		var ok bool
		plugin, ok = pluginValue.(*VoiceTTSPlugin)
		if !enabled || !ok {
			return nil, false
		}
		settings = effectiveSettings
	}
	if plugin == nil {
		plugin = NewVoiceTTSPlugin(nil)
		plugin.SetSpeechSynthesizer(r.slotSpeechSynthesizer)
	}
	plugin.SetLocalMediaSharer(localMedia)
	return &dianaTTSTool{plugin: plugin, settings: settings}, true
}

// synthesizeVoiceReply 把一段回复合成为单条 record CQ 码。
func synthesizeVoiceReply(ctx context.Context, tool *dianaTTSTool, reply string) (string, error) {
	output, err := tool.Run(ctx, map[string]any{"text": reply})
	if err != nil {
		return "", err
	}
	cq, ok := tool.TerminalResult(output)
	if !ok || strings.TrimSpace(cq) == "" {
		return "", fmt.Errorf("语音合成未生成可发送的 record")
	}
	return cq, nil
}

// persistentVoiceConfig 返回常驻语音模式下这条消息的合成工具和配置。只有插件开着、
// 发送模式是「常驻」、平台能发 QQ 语音时才生效；插件设置本身有误（比如自定义预设
// 没填地址）也不生效，这时回复照常发文字，错误在按需调用时再暴露。
func (r *Runtime) persistentVoiceConfig(event MessageEvent) (*dianaTTSTool, voiceTTSConfig, bool) {
	if r.plugins == nil || NormalizePlatformID(event.Platform) != PlatformOneBotV11 {
		return nil, voiceTTSConfig{}, false
	}
	tool, ok := r.voiceTTSToolForEvent(event)
	if !ok || tool.settings.String(voiceTTSSettingSendMode, voiceTTSSendModeOnDemand) != voiceTTSSendModeAlways {
		return nil, voiceTTSConfig{}, false
	}
	cfg, err := voiceTTSConfigFromSettings(tool.settings)
	if err != nil {
		return nil, voiceTTSConfig{}, false
	}
	return tool, cfg, true
}

// persistentVoicePrompt 在常驻语音模式下提醒模型：回复会被念出来。
func (r *Runtime) persistentVoicePrompt(event MessageEvent, cfg BotConfig) string {
	_, voiceCfg, ok := r.persistentVoiceConfig(event)
	if !ok {
		return ""
	}
	return cfg.promptf(promptPersistentVoiceSpec, map[string]string{"max_chars": strconv.Itoa(voiceCfg.MaxChars)})
}

// persistentVoiceReply 在常驻语音模式下把回复换成语音。念不出来的回复原样返回：
// 合成失败只记日志，文字照发，不让一次 TTS 故障吞掉整条回复。
func (r *Runtime) persistentVoiceReply(ctx context.Context, event MessageEvent, reply string) string {
	tool, cfg, ok := r.persistentVoiceConfig(event)
	if !ok {
		return reply
	}
	text, ok := r.persistentVoiceText(event, reply)
	if !ok || !persistentVoiceSpeakable(text, cfg.MaxChars) {
		return reply
	}
	voiceReply, err := synthesizeVoiceReply(ctx, tool, text)
	if err != nil {
		r.recordPersistentVoiceError(ctx, event, err)
		return reply
	}
	return voiceReply
}

// persistentVoiceText 按发送层的规则还原这条回复真正会发出去的文字：发送方式前缀、
// [diana-msg] / [diana-line] 这些排版标记都在这里消费掉，不然会被原样念出来。分条
// 之间补一个句号，念的时候有停顿。开头带引用标记的回复不转：语音发出去是单独一条，
// 引用框会丢。
func (r *Runtime) persistentVoiceText(event MessageEvent, reply string) (string, bool) {
	reply, event = prepareReplyDelivery(reply, event)
	if _, _, quoted := consumeOutgoingReplyControl(normalizeDianaReplyVariants(reply)); quoted {
		return "", false
	}
	var text strings.Builder
	for _, chunk := range splitEventChatReply(reply, r.effectiveConfigForEvent(event), event) {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		text.WriteString(chunk)
		if !strings.ContainsRune("。！？!?…~～，,、；;：:", []rune(chunk)[len([]rune(chunk))-1]) {
			text.WriteString("。")
		}
	}
	return text.String(), true
}

// persistentVoiceSpeakable 判断一条回复能不能整条念出来。带图片、@、引用之类的
// 非文字段，或者带链接、代码块的，念出来就丢了东西；超过单次合成字数的，合成时会
// 被截断。这些都留给文字发。QQ 表情（face）念不出来也不影响意思，放行。
func persistentVoiceSpeakable(reply string, maxChars int) bool {
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return false
	}
	segments := TextToOneBotSegments(reply)
	var text strings.Builder
	for _, segment := range segments {
		switch segment.Type {
		case "text":
			text.WriteString(segment.Data["text"])
		case "face":
		default:
			return false
		}
	}
	plain := strings.Join(strings.Fields(text.String()), " ")
	if plain == "" {
		return false
	}
	lower := strings.ToLower(plain)
	if strings.Contains(lower, "http://") || strings.Contains(lower, "https://") || strings.Contains(plain, "```") {
		return false
	}
	return maxChars <= 0 || len([]rune(plain)) <= maxChars
}

func (r *Runtime) recordPersistentVoiceError(ctx context.Context, event MessageEvent, err error) {
	writer := r.appLogWriter()
	if writer == nil || err == nil {
		return
	}
	_ = writer.AppendLog(ctx, applog.Entry{
		Kind:    applog.KindError,
		Level:   applog.LevelError,
		Action:  "persistent_voice_reply",
		Message: "常驻语音合成失败，已回退文字回复",
		Detail:  err.Error(),
		Actor:   oneBotEventActor(event),
		Target:  event.MessageID,
		Metadata: map[string]any{
			"group_id": event.GroupID,
			"user_id":  event.UserID,
		},
	})
}
