// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/vrchat"
)

// VRChat 的四个工具只在插件启用时挂上。查状态谁都能用；说话、换表情、移动
// 会在房间里被所有人看见，默认只挂给主人，插件里打开「群成员可以操控」才放开。
const (
	dianaVRChatStatusToolName     = "vrchat_status"
	dianaVRChatChatboxToolName    = "vrchat_chatbox"
	dianaVRChatExpressionToolName = "vrchat_set_expression"
	dianaVRChatMoveToolName       = "vrchat_move"

	// vrchatStatusParamLimit 控制交给模型的自定义参数条数：Avatar 动辄上百个
	// 参数，全给只会把上下文撑满。
	vrchatStatusParamLimit  = 12
	vrchatMaxExpressionHold = 10 * time.Minute
)

// vrchatControlToolNames 是会改变房间里所见内容的工具。
var vrchatControlToolNames = []string{dianaVRChatChatboxToolName, dianaVRChatExpressionToolName, dianaVRChatMoveToolName}

// newDianaVRChatTools 按权限返回本轮可用的 VRChat 工具，以及因权限被拒的工具名。
func newDianaVRChatTools(plugin *VRChatPlugin, settings SettingValues, owner bool) ([]agent.Tool, []string) {
	if plugin == nil {
		return nil, nil
	}
	tools := []agent.Tool{&dianaVRChatStatusTool{plugin: plugin}}
	if !owner && !settings.Bool(vrchatSettingMemberControl, false) {
		return tools, vrchatControlToolNames
	}
	cfg, _ := vrchatConfigFromSettings(settings)
	tools = append(tools,
		&dianaVRChatChatboxTool{plugin: plugin},
		&dianaVRChatExpressionTool{plugin: plugin, names: cfg.Expressions.Names()},
		&dianaVRChatMoveTool{plugin: plugin, maxHold: cfg.InputMaxHold},
	)
	return tools, nil
}

type vrchatToolResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	Detail  any    `json:"detail,omitempty"`
}

func marshalVRChatResult(result vrchatToolResult) (string, error) {
	data, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// vrchatFailure 把桥的错误写成给模型看的结果，而不是工具报错：VRChat 没开、
// 表情名写错都是正常情况，模型要能据此换个说法，而不是当成系统故障。
func vrchatFailure(err error) (string, error) {
	message := err.Error()
	if errors.Is(err, vrchat.ErrDisabled) {
		message = "VRChat 联动没有在运行。"
	}
	return marshalVRChatResult(vrchatToolResult{Message: message})
}

// ---- vrchat_status ----

type dianaVRChatStatusTool struct {
	plugin *VRChatPlugin
}

func (t *dianaVRChatStatusTool) Name() string { return dianaVRChatStatusToolName }

func (t *dianaVRChatStatusTool) Description() string {
	return `查看机器人在 VRChat 里的当前状态：当前 Avatar、是否 AFK/坐下/静音、正在显示的表情、最近变化的 Avatar 参数、聊天框最后一句和排队情况、正在进行的移动。` +
		`群里有人问「你在 VRChat 干嘛」「现在是什么形象」时用它，按结果如实转述。` +
		`状态来自 VRChat 通过 OSC 发回的数据：最后收包时间很久以前说明 VRChat 可能没开或没开 OSC，不要编造在房间里的活动。拍不了房间截图。`
}

func (t *dianaVRChatStatusTool) InputSchema() map[string]any {
	return toolObjectSchema(nil, map[string]any{})
}

type vrchatStatusView struct {
	Running        bool              `json:"running"`
	Listening      bool              `json:"listening"`
	LastReceived   string            `json:"last_received,omitempty"`
	AvatarID       string            `json:"avatar_id,omitempty"`
	AvatarSince    string            `json:"avatar_since,omitempty"`
	States         map[string]any    `json:"states,omitempty"`
	Expression     string            `json:"expression,omitempty"`
	ExpressionFrom string            `json:"expression_source,omitempty"`
	RecentParams   []vrchatParamView `json:"recent_params,omitempty"`
	LastChatbox    string            `json:"last_chatbox,omitempty"`
	ChatboxPending int               `json:"chatbox_pending,omitempty"`
	ActiveInputs   []string          `json:"active_inputs,omitempty"`
	Expressions    []string          `json:"available_expressions,omitempty"`
	Notes          []string          `json:"notes,omitempty"`
}

type vrchatParamView struct {
	Name    string `json:"name"`
	Value   any    `json:"value"`
	Updated string `json:"updated"`
}

func (t *dianaVRChatStatusTool) Run(context.Context, map[string]any) (string, error) {
	if t == nil || t.plugin == nil {
		return "", fmt.Errorf("vrchat status: plugin is not configured")
	}
	status := t.plugin.bridge.Status(vrchatStatusParamLimit)
	now := time.Now()
	view := vrchatStatusView{
		Running:        status.Enabled,
		Listening:      status.Listening,
		AvatarID:       status.AvatarID,
		States:         status.Builtin,
		Expression:     status.Expression,
		LastChatbox:    status.LastChatbox,
		ChatboxPending: status.ChatboxPending,
		ActiveInputs:   status.ActiveInputs,
		Expressions:    status.Expressions,
	}
	if status.Expression != "" {
		view.ExpressionFrom = "agent"
		if status.ExpressionMood {
			view.ExpressionFrom = "mood"
		}
	}
	if status.LastPacketAt != nil {
		view.LastReceived = vrchatAgo(now, *status.LastPacketAt)
	}
	if status.AvatarChangedAt != nil {
		view.AvatarSince = vrchatAgo(now, *status.AvatarChangedAt)
	}
	for _, param := range status.Params {
		view.RecentParams = append(view.RecentParams, vrchatParamView{Name: param.Name, Value: param.Value, Updated: vrchatAgo(now, param.UpdatedAt)})
	}
	switch {
	case !status.Enabled:
		view.Notes = append(view.Notes, "OSC 桥没有运行。")
	case !status.Listening:
		view.Notes = append(view.Notes, "没有在监听 VRChat 发回的数据，只能说明机器人这边发出过什么。")
	case status.LastPacketAt == nil:
		view.Notes = append(view.Notes, "还没收到过 VRChat 的数据：VRChat 可能没开，或没打开 OSC。")
	}
	if status.AvatarID == "" && status.Listening {
		view.Notes = append(view.Notes, "VRChat 只在切换 Avatar 时发 Avatar ID，桥启动后还没切换过就不知道当前是哪个。")
	}
	message := "已读取 VRChat 状态。"
	if !status.Enabled {
		message = "VRChat 联动没有在运行。"
	}
	return marshalVRChatResult(vrchatToolResult{OK: status.Enabled, Message: message, Detail: view})
}

func vrchatAgo(now, at time.Time) string {
	elapsed := now.Sub(at)
	switch {
	case elapsed < time.Minute:
		return fmt.Sprintf("%d 秒前", int(elapsed.Seconds()))
	case elapsed < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(elapsed.Minutes()))
	case elapsed < 48*time.Hour:
		return fmt.Sprintf("%d 小时前", int(elapsed.Hours()))
	default:
		return at.Format("2006-01-02 15:04")
	}
}

// ---- vrchat_chatbox ----

type dianaVRChatChatboxTool struct {
	plugin *VRChatPlugin
}

func (t *dianaVRChatChatboxTool) Name() string { return dianaVRChatChatboxToolName }

func (t *dianaVRChatChatboxTool) Description() string {
	return `在 VRChat 的聊天框（头顶气泡）里显示文字，房间里所有人都看得到。` +
		`operation=send 发送 text：超过 144 字会自动分段按间隔依次显示，不用自己拆；replace=true 会丢掉还没显示完的旧段。` +
		`typing_on/typing_off 切换「正在输入」指示；clear 清空聊天框。` +
		`只在用户要你在 VRChat 里说话、或明确需要房间里的人看到时使用；聊天平台上的回复照常写，不要把这里当成回复渠道。`
}

func (t *dianaVRChatChatboxTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("send 发送文字；typing_on/typing_off 切换输入指示；clear 清空。", "send", "typing_on", "typing_off", "clear"),
		"text":      toolStringParam("operation=send 时要显示的文字。"),
		"replace":   toolBoolParam("为 true 时先丢掉还没显示完的旧段。"),
	})
}

func (t *dianaVRChatChatboxTool) Run(_ context.Context, input map[string]any) (string, error) {
	if t == nil || t.plugin == nil {
		return "", fmt.Errorf("vrchat chatbox: plugin is not configured")
	}
	bridge := t.plugin.bridge
	switch operation := strings.TrimSpace(configToolString(input, "operation")); operation {
	case "send":
		replace, _ := input["replace"].(bool)
		result, err := bridge.Chatbox(configToolString(input, "text"), replace)
		if err != nil {
			return vrchatFailure(err)
		}
		message := fmt.Sprintf("已排进聊天框，共 %d 段。", result.Segments)
		if result.Dropped > 0 {
			message += fmt.Sprintf("队列已满，最后 %d 段没有排上。", result.Dropped)
		}
		return marshalVRChatResult(vrchatToolResult{OK: true, Message: message, Detail: result})
	case "typing_on", "typing_off":
		if err := bridge.SetTyping(operation == "typing_on"); err != nil {
			return vrchatFailure(err)
		}
		return marshalVRChatResult(vrchatToolResult{OK: true, Message: "已切换输入指示。"})
	case "clear":
		if err := bridge.ClearChatbox(); err != nil {
			return vrchatFailure(err)
		}
		return marshalVRChatResult(vrchatToolResult{OK: true, Message: "已清空聊天框。"})
	default:
		return marshalVRChatResult(vrchatToolResult{Message: "operation 只能是 send、typing_on、typing_off 或 clear。"})
	}
}

// ---- vrchat_set_expression ----

type dianaVRChatExpressionTool struct {
	plugin *VRChatPlugin
	names  []string
}

func (t *dianaVRChatExpressionTool) Name() string { return dianaVRChatExpressionToolName }

func (t *dianaVRChatExpressionTool) Description() string {
	return `切换机器人在 VRChat 里的 Avatar 表情或动作（按主人配置的映射表写 Avatar 参数）。` +
		`想在房间里表现情绪时用它，例如被夸了换「开心」、看不懂换「疑惑」、犯困换「趴桌」。` +
		`hold_seconds 大于 0 时到点自动回到「平静」；不填就一直保持，直到下次切换。只能用 expression 枚举里列出的名字。`
}

func (t *dianaVRChatExpressionTool) InputSchema() map[string]any {
	expression := toolStringParam("表情名。")
	if len(t.names) > 0 {
		expression = toolEnumParam("表情名，来自映射表。", t.names...)
	}
	return toolObjectSchema([]string{"expression"}, map[string]any{
		"expression":   expression,
		"hold_seconds": toolNumberParam("保持多少秒后回到「平静」，0 表示一直保持。", 0, vrchatMaxExpressionHold.Seconds()),
	})
}

func (t *dianaVRChatExpressionTool) Run(_ context.Context, input map[string]any) (string, error) {
	if t == nil || t.plugin == nil {
		return "", fmt.Errorf("vrchat expression: plugin is not configured")
	}
	name := strings.TrimSpace(configToolString(input, "expression"))
	if name == "" {
		return marshalVRChatResult(vrchatToolResult{Message: "需要 expression。"})
	}
	var hold time.Duration
	if seconds, ok := numberValue(input["hold_seconds"]); ok && seconds > 0 {
		hold = min(time.Duration(seconds*float64(time.Second)), vrchatMaxExpressionHold)
	}
	expression, err := t.plugin.bridge.SetExpression(name, hold)
	if err != nil {
		return vrchatFailure(err)
	}
	message := fmt.Sprintf("已切换为「%s」。", expression.Name())
	if hold > 0 {
		message += fmt.Sprintf("%d 秒后回到「%s」。", int(math.Round(hold.Seconds())), vrchat.ExpressionNeutral)
	}
	return marshalVRChatResult(vrchatToolResult{OK: true, Message: message, Detail: expression})
}

// ---- vrchat_move ----

type dianaVRChatMoveTool struct {
	plugin  *VRChatPlugin
	maxHold time.Duration
}

func (t *dianaVRChatMoveTool) Name() string { return dianaVRChatMoveToolName }

func (t *dianaVRChatMoveTool) Description() string {
	return fmt.Sprintf(`在 VRChat 里短时操控移动和视角：前后左右走、左右转、跑、跳，stop 立即停下。`+
		`每次最多按住 %.1f 秒，到点自动松开，不会一直走下去；要走远就分几次调用。`+
		`只在用户明确让你在房间里动一动时使用。`, t.maxHold.Seconds())
}

func (t *dianaVRChatMoveTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"action"}, map[string]any{
		"action":  toolEnumParam("forward/backward/left/right 移动，turn_left/turn_right 转向，run 奔跑，jump 跳一下，stop 全部松开。", vrchat.ActionNames()...),
		"seconds": toolNumberParam("按住多少秒，默认 1 秒，超过上限会被截短。jump 和 stop 忽略它。", 0.1, t.maxHold.Seconds()),
	})
}

func (t *dianaVRChatMoveTool) Run(_ context.Context, input map[string]any) (string, error) {
	if t == nil || t.plugin == nil {
		return "", fmt.Errorf("vrchat move: plugin is not configured")
	}
	action := strings.TrimSpace(configToolString(input, "action"))
	var hold time.Duration
	if seconds, ok := numberValue(input["seconds"]); ok && seconds > 0 {
		hold = time.Duration(seconds * float64(time.Second))
	}
	held, err := t.plugin.bridge.Input(action, hold)
	if err != nil {
		return vrchatFailure(err)
	}
	if action == "stop" {
		return marshalVRChatResult(vrchatToolResult{OK: true, Message: "已全部松开。"})
	}
	return marshalVRChatResult(vrchatToolResult{OK: true, Message: fmt.Sprintf("已执行 %s，%.1f 秒后自动松开。", action, held.Seconds())})
}
