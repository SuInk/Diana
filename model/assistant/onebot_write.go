package assistant

import (
	"context"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

func writeOneBotJSON(ctx context.Context, conn *websocket.Conn, writeMu *sync.Mutex, value any) error {
	if err := lockOutboundDelivery(ctx, writeMu); err != nil {
		return err
	}
	defer writeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	// 写超时固定，不跟调用方 ctx 取最小值：ctx 只约束排队等锁。
	// gorilla 写超时后连接即不可用，若剩余预算被排队耗尽，一次可选的
	// typing 刷新就会关掉整条共享连接。
	if err := conn.SetWriteDeadline(time.Now().Add(oneBotWriteTimeout)); err != nil {
		return err
	}
	if err := conn.WriteJSON(value); err != nil {
		// 写超时后 gorilla 连接不可复用，关闭以唤醒读循环并触发重连。
		_ = conn.Close()
		return err
	}
	return nil
}
