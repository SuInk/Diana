// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"errors"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// 都是变量而不是常量：测试要把它们缩到毫秒级，不然一条用例要等一分半。
var (
	// oneBotReadTimeout 是「多久没收到对端任何东西就认定这条连接已经死了」。
	// NapCat 默认每 30 秒推一次 heartbeat 元事件，我们自己也每 30 秒 ping 一次，
	// 90 秒还是一片安静基本只剩一种解释。
	oneBotReadTimeout = 90 * time.Second
	// oneBotPingInterval 是主动探测的间隔。对端只是闲着没消息时，靠这个把读超时
	// 续上，不会被误杀。
	oneBotPingInterval = 30 * time.Second
	oneBotWriteTimeout = 10 * time.Second
)

// startOneBotKeepalive 给一条 OneBot WebSocket 装上心跳。
//
// 没有它的时候，对端如果是被硬断的（容器被 kill、宿主机休眠、NAT 表项过期），
// TCP 那边永远不会来 FIN，ReadMessage 就一直阻塞下去。连接看上去还「连着」，
// 实际再也收不到消息；反向模式只有一个连接位，这个位子被死连接占着，NapCat
// 重连上来只会拿到 409，机器人就此哑掉——线上那次就是这么停的。
//
// 返回的 refresh 要在每次成功读到帧之后调一次：对端可能不回 pong（协议要求回，
// 但没必要赌），只要它还在发心跳元事件，这条连接就是活的，不该被读超时打断。
// stop 可以重复调用。
func startOneBotKeepalive(conn *websocket.Conn, writeMu *sync.Mutex) (refresh func(), stop func()) {
	// 先取到本地：这几个值是变量（测试要缩短它们），之后的读都在别的协程里，
	// 读全局就会和测试收尾时的还原撞成数据竞争。
	readTimeout, pingInterval := oneBotReadTimeout, oneBotPingInterval
	refresh = func() {
		_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
	}
	refresh()
	conn.SetPongHandler(func(string) error {
		refresh()
		return nil
	})
	// 对端也可能反过来 ping 我们。gorilla 的默认 ping handler 会回 pong，但它不走
	// writeMu，和 CallAPI 的写撞在一起就是并发写 panic。
	conn.SetPingHandler(func(payload string) error {
		refresh()
		err := writeOneBotControl(conn, writeMu, websocket.PongMessage, []byte(payload))
		if errors.Is(err, websocket.ErrCloseSent) {
			return nil
		}
		return err
	})

	done := make(chan struct{})
	go func() {
		defer recoverGoroutinePanic("onebot.keepalive")
		ticker := time.NewTicker(pingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if err := writeOneBotControl(conn, writeMu, websocket.PingMessage, nil); err != nil {
					// 写都写不出去了，连接已经没救。关掉它让读循环退出，把连接位交出来。
					_ = conn.Close()
					return
				}
			}
		}
	}()
	return refresh, sync.OnceFunc(func() { close(done) })
}

func writeOneBotControl(conn *websocket.Conn, writeMu *sync.Mutex, messageType int, payload []byte) error {
	writeMu.Lock()
	defer writeMu.Unlock()
	return conn.WriteControl(messageType, payload, time.Now().Add(oneBotWriteTimeout))
}

// oneBotReadError 把读超时翻译成人话。状态页上原样显示 "i/o timeout" 只会让人
// 以为是网络抖动，其实是对端整整 90 秒没吭声、连接被我们主动判死。
func oneBotReadError(err error) string {
	if err == nil {
		return ""
	}
	var netErr interface{ Timeout() bool }
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "连接长时间没有任何响应，已断开等待重连"
	}
	return err.Error()
}
