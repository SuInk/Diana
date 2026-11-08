// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"

	"github.com/SuInk/diana/model/callbridge"
	"github.com/SuInk/diana/model/desktopctl"
)

// 容器屏幕的指令通道。
//
// 主动打电话、发起群通话要在客户端界面上点，界面会改版，写死流程和坐标都不通用，所以
// 交给模型看截图、读元素去点（call_screen 工具，见 call_screen.go）。指令沿已有的
// agent 连接走（callbridge 的 desktop / desktop_result），载荷沿用桌面控制的 window.*
// 格式，不另开端口。
//
// 它不接进宿主机的「桌面控制」：操作的是容器里的客户端，不是 Diana 所在的那台机器，
// 能不能操作是通话插件自己的设置（screen_control），和宿主机桌面控制的总开关无关。

type callDesktopBridge struct {
	set *bridgeCallSet

	mu      sync.Mutex
	seq     int
	pending map[string]chan callbridge.Message
}

func newCallDesktopBridge(set *bridgeCallSet) *callDesktopBridge {
	return &callDesktopBridge{set: set, pending: map[string]chan callbridge.Message{}}
}

// detach 是 agent 断线：在飞的指令立刻失败。
func (b *callDesktopBridge) detach() {
	b.mu.Lock()
	pending := b.pending
	b.pending = map[string]chan callbridge.Message{}
	b.mu.Unlock()
	for _, ch := range pending {
		select {
		case ch <- callbridge.Message{Type: callbridge.TypeDesktopResult, Error: "和通话插件断线了"}:
		default:
		}
	}
}

func (b *callDesktopBridge) deliver(msg callbridge.Message) {
	b.mu.Lock()
	ch, ok := b.pending[msg.ID]
	delete(b.pending, msg.ID)
	b.mu.Unlock()
	if ok {
		ch <- msg
	}
}

// call 下发一条指令，等 agent 回执，把 Data 解到 out。
func (b *callDesktopBridge) call(ctx context.Context, op string, cmd desktopctl.Command, out any) error {
	cmd.Op = op
	params, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.seq++
	id := "d" + strconv.Itoa(b.seq)
	ch := make(chan callbridge.Message, 1)
	b.pending[id] = ch
	b.mu.Unlock()
	forget := func() {
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
	}
	if err := b.set.send(callbridge.Message{Type: callbridge.TypeDesktop, ID: id, Op: op, Params: params}); err != nil {
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
