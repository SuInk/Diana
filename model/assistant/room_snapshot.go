// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"sync"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// RoomSnapshotRenderer renders a bounded server-owned scene, never an admin page.
type RoomSnapshotRenderer func(context.Context, RoomState) ([]byte, error)

func (r *Runtime) SetRoomSnapshotRenderer(renderer RoomSnapshotRenderer) {
	r.rooms.mu.Lock()
	defer r.rooms.mu.Unlock()
	r.rooms.renderer = renderer
}

// NewRoomSnapshotRenderer uses only fixed asset paths in the packaged frontend.
// No browser, LLM image generation, network or user-controlled filesystem paths.
func NewRoomSnapshotRenderer(open func(string) (io.ReadCloser, error)) RoomSnapshotRenderer {
	var once sync.Once
	var background, sprites image.Image
	var loadErr error
	load := func(name string) (image.Image, error) {
		f, err := open(name)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		raw, err := io.ReadAll(io.LimitReader(f, 8<<20+1))
		if err != nil {
			return nil, err
		}
		if len(raw) > 8<<20 {
			return nil, fmt.Errorf("room asset too large")
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		if cfg.Width < 4 || cfg.Height < 1 || cfg.Width > 4096 || cfg.Height > 4096 {
			return nil, fmt.Errorf("invalid room asset dimensions")
		}
		return png.Decode(bytes.NewReader(raw))
	}
	return func(ctx context.Context, state RoomState) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !ValidRoomPose(state.Pose) {
			return nil, fmt.Errorf("invalid room pose")
		}
		once.Do(func() {
			background, loadErr = load("/room/whale-bedroom.png")
			if loadErr != nil {
				return
			}
			sprites, loadErr = load("/room/whale-poses.png")
		})
		if loadErr != nil {
			return nil, fmt.Errorf("默认房间素材不可用，请构建包含小窝素材的完整 WebUI：%w", loadErr)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		out := image.NewRGBA(image.Rect(0, 0, 960, 672))
		xdraw.BiLinear.Scale(out, image.Rect(0, 32, 960, 672), background, background.Bounds(), draw.Src, nil)
		index := map[string]int{"rest": 0, "phone": 1, "desk": 2}[state.Pose]
		cellWidth := sprites.Bounds().Dx() / 4
		crop := image.Rect(index*cellWidth, 0, (index+1)*cellWidth, sprites.Bounds().Dy())
		x, y := 960*73/100, 32+640*78/100
		if state.Pose == "desk" {
			x, y = 960*29/100, 32+640*76/100
		}
		width := 960 * 27 / 100
		height := width * sprites.Bounds().Dy() / cellWidth
		xdraw.BiLinear.Scale(out, image.Rect(x-width/2, y-height, x-width/2+width, y), sprites, crop, draw.Over, nil)
		draw.Draw(out, image.Rect(0, 0, 960, 32), image.NewUniform(color.RGBA{23, 55, 42, 255}), image.Point{}, draw.Src)
		d := font.Drawer{Dst: out, Src: image.White, Face: basicfont.Face7x13, Dot: fixed.P(12, 21)}
		d.DrawString(fmt.Sprintf("DEFAULT ROOM | %s | %s | tasks: %d | rev: %d | not a browser screenshot", state.Pose, state.Phase, state.ActiveTasks, state.Revision))
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var b bytes.Buffer
		if err := png.Encode(&b, out); err != nil {
			return nil, err
		}
		return b.Bytes(), nil
	}
}

func (t *dianaRoomTool) sendState(ctx context.Context, op string, state RoomState) (string, error) {
	r := t.runtime
	if t.event.Kind != EventKindGroup && t.event.Kind != EventKindPrivate {
		return "", fmt.Errorf("仅可向当前群聊或私聊发送小窝状态")
	}
	if (t.event.Kind == EventKindGroup && t.event.GroupID == "") || (t.event.Kind == EventKindPrivate && t.event.UserID == "") {
		return "", fmt.Errorf("当前会话缺少发送目标")
	}
	var picture []byte
	if op == "snapshot" {
		r.rooms.mu.Lock()
		renderer := r.rooms.renderer
		r.rooms.mu.Unlock()
		if renderer == nil {
			return "", fmt.Errorf("当前部署尚未配置房间状态图渲染器，未发送图片")
		}
		var err error
		picture, err = renderer(ctx, state)
		if err != nil {
			return "", err
		}
		if len(picture) == 0 || len(picture) > 8<<20 {
			return "", fmt.Errorf("房间状态图为空或过大，未发送")
		}
	}
	// Rendering can take time. Recheck cancellation, task ownership and the gate before sending.
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !OwnSpaceEnabled(r.effectiveConfigForEvent(t.event)) {
		return "", fmt.Errorf("小窝已关闭，未发送")
	}
	if _, err := t.state(ctx, "get", ""); err != nil {
		return "", err
	}
	var err error
	if op == "snapshot" {
		err = r.sendPNGImage(ctx, t.event, picture)
	} else {
		pose := map[string]string{"rest": "床边休息", "phone": "看手机", "desk": "书桌前"}[state.Pose]
		phase := map[string]string{"idle": "待机", "thinking": "生成回复", "tool": "调用工具", "completed": "回复生成结束", "cancelled": "已取消", "error": "遇到错误", "expired": "状态过期", "disabled": "未开启"}[state.Phase]
		text := fmt.Sprintf("小窝状态：%s\n回复任务：%s（%d 个活跃任务）\n这是虚拟房间状态，不代表电脑操作或消息投递结果。", pose, phase, state.ActiveTasks)
		err = r.sendOutgoing(ctx, t.event, OutgoingMessage{Text: text})
	}
	if err != nil {
		return "", fmt.Errorf("小窝状态发送失败：%w", err)
	}
	result, err := json.Marshal(map[string]any{"ok": true, "sent": true, "operation": op, "room": state, "appearance": "server_default", "notice": "已向当前会话发送，无需重复发送。状态图是默认插画合成，不是浏览器实拍，不包含浏览器本地自定义；数据为调用时的快照。"})
	return string(result), err
}
