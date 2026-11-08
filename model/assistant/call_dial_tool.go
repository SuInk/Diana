// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/SuInk/diana/model/callbridge"
)

// 机器人主动打 电话。
//
// 拨号本身交给桌面控制：模型在「通话容器」那块屏幕上看截图、点按钮，客户端界面怎么改都
// 不用改代码。这个工具只做桌面控制做不了的那一半：先登记「接下来拨出去的那通电话是
// 打给谁、为什么打」。agent 报来拨出去的电话（outgoing）时按这份登记认领，接通后开
// 通话会话，并让机器人先开口说明来意。没登记过的拨出电话（有人在 noVNC 里拨的）
// 不归机器人管。
//
// 只给主人用：打电话会让对方的手机响，群通话会通知群里的人。

const (
	phoneCallToolName = "phone_call"
	// bridgeCallIntentTTL 是登记之后多久内拨出去的电话算数：模型操作界面拨号一般半分钟内。
	bridgeCallIntentTTL = 3 * time.Minute
)

type bridgeCallIntent struct {
	target bridgeCallTarget
	at     time.Time
}

type phoneCallTool struct {
	runtime *Runtime
	event   MessageEvent
}

func (t *phoneCallTool) Name() string { return phoneCallToolName }

func (t *phoneCallTool) Description() string {
	return "打语音电话：在私聊里是打给主人，在群里是在当前群发起群语音通话并邀请主人。主人让你打电话、发起语音时用。" +
		"调用后照返回的步骤用桌面控制（desktop_*，连接「通话容器」）在客户端里拨号；对方接通后通话自动接管，你先开口。"
}

func (t *phoneCallTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"reason"}, map[string]any{
		"reason": toolStringParam("为什么打这通电话、接通后要说什么，一句话"),
	})
}

type phoneCallToolResult struct {
	OK         bool   `json:"ok"`
	Connection string `json:"connection"`
	Steps      string `json:"steps"`
}

func (t *phoneCallTool) Run(_ context.Context, input map[string]any) (string, error) {
	reason := strings.TrimSpace(configToolString(input, "reason"))
	if reason == "" {
		return "", errors.New("reason 要写：为什么打这通电话")
	}
	r := t.runtime
	plugin, _, enabled := r.plugins.PluginWithSettingsForProfile(callPluginID, t.event.ProfileID)
	bridge, ok := plugin.(*CallPlugin)
	if !enabled || !ok {
		return "", errors.New("通话插件没开，打不了电话")
	}
	calls := bridge.currentCalls()
	if calls == nil || calls.currentLink() == nil {
		return "", errors.New("没连上容器里的通话插件，打不了电话")
	}
	runtime, change := bridge.settings()
	ready := callPluginReadiness(context.Background(), runtime, change)
	if ready.problem != "" {
		return "", errors.New("现在打不了：" + ready.problem)
	}
	if ready.profileID != t.event.ProfileID {
		return "", errors.New("容器里登录的账号不是这台机器人，打不了")
	}
	connection := calls.desktop.connectionID()
	if connection == "" {
		return "", errors.New("「通话容器」还没接进桌面控制：到控制台「设置 → 桌面控制」打开总开关和写操作，并给这台机器人开桌面控制")
	}
	target := bridgeCallTarget{profileID: ready.profileID, ownerID: ready.ownerID, dialed: true, reason: reason}
	var steps string
	if t.event.Kind == EventKindGroup && strings.TrimSpace(t.event.GroupID) != "" {
		target.groupID, target.groupName = t.event.GroupID, t.event.GroupName
		steps = "在客户端主窗口里：点顶部搜索框，输入群号 " + t.event.GroupID + " 回车打开群聊；点群聊标题栏右侧的电话图标（语音通话）；" +
			"在弹出的选成员框里输入主人的账号 " + ready.ownerID + " 回车勾上，再点「确定」。每一步先截图或读元素看清楚再点。"
	} else {
		steps = "在客户端主窗口里：点顶部搜索框，输入主人的账号 " + ready.ownerID + " 回车打开私聊；点会话标题栏右侧的电话图标（语音通话）。每一步先截图或读元素看清楚再点。"
	}
	calls.expectOutgoing(target)
	body, err := json.Marshal(phoneCallToolResult{OK: true, Connection: connection, Steps: steps + "客户端主窗口被收起来时，点托盘里的客户端小图标拿回来。拨出去以后不用等，对方接通后通话会自动接上。"})
	return string(body), err
}

// currentCalls 是当前到 agent 的通话集合；插件没连时为 nil。
func (p *CallPlugin) currentCalls() *bridgeCallSet {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// expectOutgoing 登记接下来拨出去的那通电话。
func (s *bridgeCallSet) expectOutgoing(target bridgeCallTarget) {
	s.mu.Lock()
	s.intent = &bridgeCallIntent{target: target, at: time.Now()}
	s.mu.Unlock()
}

// outgoing 认领 agent 报来的拨出电话：有登记就记成机器人打的，接通后由 connected 开会话。
func (s *bridgeCallSet) outgoing(msg callbridge.Message) {
	s.mu.Lock()
	intent := s.intent
	if intent != nil && time.Since(intent.at) <= bridgeCallIntentTTL {
		s.intent = nil
		s.answered[msg.Call] = intent.target
	} else {
		intent = nil
	}
	s.mu.Unlock()
	if intent == nil {
		s.plugin.noteCall("容器里拨出了一通电话，不是机器人打的，不管")
		return
	}
	s.plugin.noteCall("机器人拨出了%s，等对方接", intent.target.describe())
}

// connectionID 是「通话容器」在桌面控制里的连接 ID；没接进去时为空。
func (b *callDesktopBridge) connectionID() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn == nil {
		return ""
	}
	return b.conn.ID()
}

// bridgeCallOpener 是机器人主动打的电话接通后交给通话的第一句「旁白」，让它先开口。
func bridgeCallOpener(target bridgeCallTarget) string {
	return "（电话接通了。这通是你主动打过去的，原因：" + target.reason + "。你先开口。）"
}
