// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/vrchat"
)

// VRChat 联动做成默认关闭的内置插件：绝大多数部署根本没有 VRChat 客户端，
// 开着只会白占一个 UDP 端口。插件本身不处理消息，它负责三件事：按开关启停
// 进程级的 OSC 桥、给 Agent 挂 vrchat_* 工具、回复后按心情驱动表情和同步聊天框。
const (
	vrchatPluginID = "official.vrchat-osc"

	vrchatSettingHost            = "host"
	vrchatSettingSendPort        = "send_port"
	vrchatSettingListenEnabled   = "listen_enabled"
	vrchatSettingListenHost      = "listen_host"
	vrchatSettingListenPort      = "listen_port"
	vrchatSettingChatboxInterval = "chatbox_interval"
	vrchatSettingChatboxSound    = "chatbox_sound"
	vrchatSettingMirrorReply     = "chatbox_mirror_reply"
	vrchatSettingMoodExpression  = "mood_expression"
	vrchatSettingExpressions     = "expressions"
	vrchatSettingInputMaxSeconds = "input_max_seconds"
	vrchatSettingMemberControl   = "member_control"
)

// VRChatPluginID 对外导出，WebUI 用它取桥的实时状态。
const VRChatPluginID = vrchatPluginID

// VRChatPlugin 持有进程里唯一的 OSC 桥。
type VRChatPlugin struct {
	bridge *vrchat.Bridge

	mu       sync.Mutex
	problems []string
	applyErr string
}

func NewVRChatPlugin() *VRChatPlugin {
	return &VRChatPlugin{bridge: vrchat.NewBridge()}
}

func (p *VRChatPlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID:              vrchatPluginID,
		Name:            "VRChat 联动（OSC）",
		Version:         "0.1.0",
		Description:     "通过 VRChat 的 OSC 接口让机器人在虚拟房间里有身体：往聊天框发字、按情绪驱动 Avatar 表情参数、短时操控移动和转向，并读取当前 Avatar 与参数状态供群聊里查询。需要在 VRChat 里打开 OSC，默认发往本机 9000 端口、监听 9001 端口。",
		Official:        true,
		BuiltIn:         true,
		DefaultDisabled: true,
		Permissions:     []string{"network:udp", "vrchat:osc"},
		Settings: []PluginSettingSpec{
			{
				Key:         vrchatSettingHost,
				Label:       "VRChat 地址",
				Description: "运行 VRChat 的机器。和 Diana 在同一台电脑上就保持 127.0.0.1；在另一台机器上要填它的局域网地址，并在 VRChat 启动参数里用 --osc 指定回传地址，见使用说明。",
				Type:        PluginSettingTypeString,
				Default:     vrchat.DefaultHost,
			},
			{
				Key:         vrchatSettingSendPort,
				Label:       "发送端口",
				Description: "VRChat 接收 OSC 的端口，默认 9000。",
				Type:        PluginSettingTypeNumber,
				Default:     vrchat.DefaultSendPort,
				Min:         settingRange(1),
				Max:         settingRange(65535),
			},
			{
				Key:         vrchatSettingListenEnabled,
				Label:       "监听 VRChat 状态",
				Description: "接收 VRChat 发回的 Avatar 切换和参数变化，群里问「在干嘛」时才答得上来。端口被别的 OSC 工具占用时关掉它，发送不受影响。",
				Type:        PluginSettingTypeBool,
				Default:     true,
			},
			{
				Key:         vrchatSettingListenHost,
				Label:       "监听地址",
				Description: "默认只收本机的包。VRChat 在另一台机器上时改成 0.0.0.0，同时注意局域网里谁都能往这个端口发包。",
				Type:        PluginSettingTypeString,
				Default:     vrchat.DefaultHost,
			},
			{
				Key:         vrchatSettingListenPort,
				Label:       "监听端口",
				Description: "VRChat 发出 OSC 的端口，默认 9001。",
				Type:        PluginSettingTypeNumber,
				Default:     vrchat.DefaultListenPort,
				Min:         settingRange(1),
				Max:         settingRange(65535),
			},
			{
				Key:         vrchatSettingChatboxInterval,
				Label:       "聊天框分段间隔",
				Description: "聊天框一段最多 144 字，长文会拆成几段依次显示。VRChat 对聊天框限速，间隔太短的段会被丢掉，最短 1.5 秒。",
				Type:        PluginSettingTypeNumber,
				Default:     vrchat.DefaultChatboxInterval.Seconds(),
				Min:         settingRange(vrchat.MinChatboxInterval.Seconds()),
				Max:         settingRange(10),
				Step:        0.5,
				Unit:        "秒",
			},
			{
				Key:         vrchatSettingChatboxSound,
				Label:       "聊天框提示音",
				Description: "每条新消息的第一段在 VRChat 里响一下提示音。",
				Type:        PluginSettingTypeBool,
				Default:     false,
			},
			{
				Key:         vrchatSettingMirrorReply,
				Label:       "回复同步到聊天框",
				Description: "机器人在群里的每条回复也显示在 VRChat 聊天框里，房间里的人都看得到。私聊回复从不同步。只想同步某个群就保持这里关闭，在那个群的设置里单独打开。",
				Type:        PluginSettingTypeBool,
				Default:     false,
			},
			{
				Key:         vrchatSettingMoodExpression,
				Label:       "心情驱动表情",
				Description: "机器人配置里开了「心情」时，每次回复后按当前心情套用映射表里的「开心」「低落」「平静」。Agent 刚用工具指定过的表情两分钟内不会被心情覆盖。",
				Type:        PluginSettingTypeBool,
				Default:     true,
			},
			{
				Key:         vrchatSettingExpressions,
				Label:       "表情映射表",
				Description: "表情名到 Avatar 参数的映射，Agent 只能用这里写了的表情。参数名要和你的 Avatar 的 Expression Parameters 一致，默认模板只是示例。",
				Type:        PluginSettingTypeText,
				Default:     vrchat.DefaultExpressionMap,
				Rows:        10,
			},
			{
				Key:         vrchatSettingInputMaxSeconds,
				Label:       "移动单次最长",
				Description: "Agent 操控移动和转向时一次最多按住多久，到点自动松开。最长 10 秒。",
				Type:        PluginSettingTypeNumber,
				Default:     vrchat.DefaultInputHold.Seconds(),
				Min:         settingRange(0.5),
				Max:         settingRange(vrchat.MaxInputHold.Seconds()),
				Step:        0.5,
				Unit:        "秒",
			},
			{
				Key:         vrchatSettingMemberControl,
				Label:       "群成员可以操控",
				Description: "关着时只有主人能让机器人在 VRChat 里说话、换表情、移动；群成员只能查看状态。打开后谁都能指挥，房间里的效果所有人都看得到。",
				Type:        PluginSettingTypeBool,
				Default:     false,
			},
		},
	}
}

// Handle 不处理消息：能力都在工具和回复后的钩子里。
func (p *VRChatPlugin) Handle(context.Context, PluginRequest) (*PluginResponse, error) {
	return nil, nil
}

// PluginStateChanged 跟着插件开关和全局设置启停 OSC 桥。
func (p *VRChatPlugin) PluginStateChanged(enabled bool, settings SettingValues) {
	cfg, problems := vrchatConfigFromSettings(settings)
	err := p.bridge.Apply(enabled, cfg)
	p.mu.Lock()
	p.problems = problems
	p.applyErr = ""
	if err != nil {
		p.applyErr = err.Error()
	}
	p.mu.Unlock()
	if err != nil {
		log.Printf("diana: vrchat osc bridge: %v", err)
	}
}

// VRChatStatus 是 WebUI 看到的桥状态：桥的快照加上映射表的解析问题。
type VRChatStatus struct {
	vrchat.Status
	MappingProblems []string `json:"mapping_problems,omitempty"`
	ApplyError      string   `json:"apply_error,omitempty"`
}

// Status 返回桥的实时状态。
func (p *VRChatPlugin) Status() VRChatStatus {
	status := VRChatStatus{Status: p.bridge.Status(32)}
	p.mu.Lock()
	status.MappingProblems = append([]string(nil), p.problems...)
	status.ApplyError = p.applyErr
	p.mu.Unlock()
	return status
}

func vrchatConfigFromSettings(settings SettingValues) (vrchat.Config, []string) {
	expressions, problems := vrchat.ParseExpressionMap(settings.String(vrchatSettingExpressions, vrchat.DefaultExpressionMap))
	cfg := vrchat.Config{
		SendAddress: net.JoinHostPort(
			strings.TrimSpace(settings.String(vrchatSettingHost, vrchat.DefaultHost)),
			strconv.Itoa(settings.Int(vrchatSettingSendPort, vrchat.DefaultSendPort)),
		),
		ChatboxInterval: vrchatSeconds(settings, vrchatSettingChatboxInterval, vrchat.DefaultChatboxInterval),
		ChatboxSound:    settings.Bool(vrchatSettingChatboxSound, false),
		InputMaxHold:    vrchatSeconds(settings, vrchatSettingInputMaxSeconds, vrchat.DefaultInputHold),
		Expressions:     expressions,
	}
	if settings.Bool(vrchatSettingListenEnabled, true) {
		cfg.ListenAddress = net.JoinHostPort(
			strings.TrimSpace(settings.String(vrchatSettingListenHost, vrchat.DefaultHost)),
			strconv.Itoa(settings.Int(vrchatSettingListenPort, vrchat.DefaultListenPort)),
		)
	}
	return cfg, problems
}

func vrchatSeconds(settings SettingValues, key string, fallback time.Duration) time.Duration {
	seconds, ok := numberValue(settings[key])
	if !ok || seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds * float64(time.Second))
}

// vrchatMoodExpression 把心情分换成映射表里的表情名，档位线和语气注入共用。
func vrchatMoodExpression(score float64) string {
	switch {
	case score >= moodHappyThreshold:
		return vrchat.ExpressionHappy
	case score <= moodLowThreshold:
		return vrchat.ExpressionLow
	default:
		return vrchat.ExpressionNeutral
	}
}

// afterReplyVRChat 是回复发出后的钩子：心情驱动表情、按需把回复同步到聊天框。
// 桥的发送都是本机 UDP 或入队，不会拖慢回复链路。
func (r *Runtime) afterReplyVRChat(event MessageEvent, reply string) {
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return
	}
	pluginValue, settings, enabled := r.pluginWithSettingsForEvent(vrchatPluginID, event)
	if !enabled {
		return
	}
	plugin, ok := pluginValue.(*VRChatPlugin)
	if !ok {
		return
	}
	cfg := r.effectiveConfigForEvent(event)
	if settings.Bool(vrchatSettingMoodExpression, true) && boolValue(cfg.MoodEnabled, false) {
		plugin.bridge.ApplyMood(vrchatMoodExpression(r.moodScore(event.ProfileID, r.clock())))
	}
	// 私聊的回复不同步：房间里的人都看得见聊天框，私聊内容不该被公开。
	if settings.Bool(vrchatSettingMirrorReply, false) && event.Kind == EventKindGroup {
		if _, err := plugin.bridge.Chatbox(reply, true); err != nil {
			log.Printf("diana: vrchat chatbox mirror: %v", err)
		}
	}
}
