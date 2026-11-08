// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/SuInk/diana/model/desktopctl"
	"github.com/SuInk/diana/model/qqcall"
)

// 把 SnowLuma 容器的屏幕接进「桌面控制」。
//
// 主动打电话、发起群通话这类事要在 QQ 界面上点，界面会改版，写死流程和坐标都不通用。
// 桌面控制本来就是让模型看截图、读元素、点击输入去操作电脑的那一套（白名单、写权限、
// 限流、人工接管、持久任务都有），只是它原来只管 Diana 所在的那台机器。这里把 agent
// 那块 X 屏幕注册成桌面控制的一个执行器「QQ 容器」，指令沿着已有的 agent 连接走
// （qqcall 的 desktop / desktop_result），不另开端口。
//
// 注册的前提是桌面控制总开关开着；QQ 窗口要在应用白名单里（bundle 是 x11.qq），写操作
// 要打开 write_enabled，这些都按桌面控制原有的规矩来。

const (
	qqDesktopHelperID = "qqcall-agent"
	// qqDesktopAttachEvery 是没注册上时多久再试一次：总开关可能是后来才打开的。
	qqDesktopAttachEvery = 30 * time.Second
)

type qqDesktopBridge struct {
	set *qqCallSet

	mu      sync.Mutex
	seq     int
	pending map[string]chan qqcall.Message
	conn    *desktopctl.Connection
}

func newQQDesktopBridge(set *qqCallSet) *qqDesktopBridge {
	return &qqDesktopBridge{set: set, pending: map[string]chan qqcall.Message{}}
}

// hubFor 取运行时的桌面控制控制面；没注入时返回 nil。
func hubFor(runtime *Runtime) *desktopctl.Hub {
	if runtime == nil {
		return nil
	}
	runtime.mu.RLock()
	bridge := runtime.desktopControl
	runtime.mu.RUnlock()
	hub, _ := bridge.(*desktopctl.Hub)
	return hub
}

// keepAttached 在 agent 连着的时候保持注册：总开关后来打开、连接被关掉了都会补上。
func (b *qqDesktopBridge) keepAttached(ctx context.Context, runtime *Runtime) {
	ticker := time.NewTicker(qqDesktopAttachEvery)
	defer ticker.Stop()
	for {
		b.attach(ctx, runtime)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (b *qqDesktopBridge) attach(ctx context.Context, runtime *Runtime) {
	hub := hubFor(runtime)
	if hub == nil || !hub.Policy().Enabled {
		return
	}
	b.mu.Lock()
	conn := b.conn
	b.mu.Unlock()
	if conn != nil {
		if _, alive := hub.Connection(conn.ID()); alive {
			return
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	c, _, err := hub.AttachLocal(ctx, b, desktopctl.Hello{
		HelperID:   qqDesktopHelperID,
		HelperName: "QQ 容器",
		Platform:   "linux",
		Label:      "SnowLuma 容器里的 Linux QQ（通话插件）",
	}, desktopctl.TokenInfo{Name: "QQ 语音通话插件"})
	if err != nil {
		log.Printf("diana: qq voice call: 接进桌面控制失败: %v", err)
		return
	}
	b.mu.Lock()
	b.conn = c
	b.mu.Unlock()
}

// detach 是 agent 断线：注销连接，在飞的指令立刻失败。
func (b *qqDesktopBridge) detach() {
	b.mu.Lock()
	conn := b.conn
	b.conn = nil
	pending := b.pending
	b.pending = map[string]chan qqcall.Message{}
	b.mu.Unlock()
	for _, ch := range pending {
		select {
		case ch <- qqcall.Message{Type: qqcall.TypeDesktopResult, Error: "和通话插件断线了"}:
		default:
		}
	}
	if conn != nil {
		conn.Close()
	}
}

func (b *qqDesktopBridge) deliver(msg qqcall.Message) {
	b.mu.Lock()
	ch, ok := b.pending[msg.ID]
	delete(b.pending, msg.ID)
	b.mu.Unlock()
	if ok {
		ch <- msg
	}
}

// call 下发一条指令，等 agent 回执，把 Data 解到 out。
func (b *qqDesktopBridge) call(ctx context.Context, op string, cmd desktopctl.Command, out any) error {
	params, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.seq++
	id := "d" + strconv.Itoa(b.seq)
	ch := make(chan qqcall.Message, 1)
	b.pending[id] = ch
	b.mu.Unlock()
	forget := func() {
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
	}
	if err := b.set.send(qqcall.Message{Type: qqcall.TypeDesktop, ID: id, Op: op, Params: params}); err != nil {
		forget()
		return err
	}
	select {
	case <-ctx.Done():
		forget()
		return ctx.Err()
	case reply := <-ch:
		if reply.Error != "" {
			return errors.New(reply.Error)
		}
		if out == nil || len(reply.Data) == 0 {
			return nil
		}
		return json.Unmarshal(reply.Data, out)
	}
}

// 下面实现 desktopctl.Adapter 及元素读取、滚动两个可选能力。

func (b *qqDesktopBridge) ListWindows(ctx context.Context) ([]desktopctl.WindowInfo, error) {
	var payload desktopctl.WindowsPayload
	err := b.call(ctx, desktopctl.OpWindowsList, desktopctl.Command{Op: desktopctl.OpWindowsList}, &payload)
	return payload.Windows, err
}

func (b *qqDesktopBridge) Screenshot(ctx context.Context, windowID string) (desktopctl.ScreenshotPayload, error) {
	var payload desktopctl.ScreenshotPayload
	err := b.call(ctx, desktopctl.OpWindowScreenshot, desktopctl.Command{Op: desktopctl.OpWindowScreenshot, WindowID: windowID}, &payload)
	return payload, err
}

func (b *qqDesktopBridge) action(ctx context.Context, op string, cmd desktopctl.Command) (desktopctl.ActionResult, error) {
	cmd.Op = op
	var payload desktopctl.ActionResult
	err := b.call(ctx, op, cmd, &payload)
	return payload, err
}

func (b *qqDesktopBridge) Click(ctx context.Context, cmd desktopctl.Command) (desktopctl.ActionResult, error) {
	return b.action(ctx, desktopctl.OpWindowClick, cmd)
}

func (b *qqDesktopBridge) TypeText(ctx context.Context, cmd desktopctl.Command) (desktopctl.ActionResult, error) {
	return b.action(ctx, desktopctl.OpWindowType, cmd)
}

func (b *qqDesktopBridge) PressKey(ctx context.Context, cmd desktopctl.Command) (desktopctl.ActionResult, error) {
	return b.action(ctx, desktopctl.OpWindowKey, cmd)
}

func (b *qqDesktopBridge) Scroll(ctx context.Context, cmd desktopctl.Command) (desktopctl.ActionResult, error) {
	return b.action(ctx, desktopctl.OpWindowScroll, cmd)
}

func (b *qqDesktopBridge) Elements(ctx context.Context, cmd desktopctl.Command) (desktopctl.ElementsPayload, error) {
	cmd.Op = desktopctl.OpWindowElements
	var payload desktopctl.ElementsPayload
	err := b.call(ctx, desktopctl.OpWindowElements, cmd, &payload)
	return payload, err
}

var _ desktopctl.Adapter = (*qqDesktopBridge)(nil)
