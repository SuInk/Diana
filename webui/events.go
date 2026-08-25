// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/assistant"

	"github.com/gin-gonic/gin"
)

// sseMessage 是一条待推送的 SSE 消息，data 已序列化为 JSON。
type sseMessage struct {
	Event string
	Data  []byte
}

// EventHub 是进程内的 SSE 订阅中心；订阅者写满时丢弃消息，慢客户端不会拖垮广播。
type EventHub struct {
	mu   sync.Mutex
	subs map[chan sseMessage]struct{}
}

// NewEventHub 创建 EventHub。
func NewEventHub() *EventHub {
	return &EventHub{subs: map[chan sseMessage]struct{}{}}
}

// Subscribe 注册一个订阅者并返回接收通道。
func (h *EventHub) Subscribe() chan sseMessage {
	ch := make(chan sseMessage, 32)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

// Unsubscribe 移除订阅者。
func (h *EventHub) Unsubscribe(ch chan sseMessage) {
	h.mu.Lock()
	if _, ok := h.subs[ch]; ok {
		delete(h.subs, ch)
		close(ch)
	}
	h.mu.Unlock()
}

// Publish 向所有订阅者广播一条消息；序列化失败或无订阅者时静默返回。
func (h *EventHub) Publish(event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	message := sseMessage{Event: event, Data: data}

	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- message:
		default:
			// 订阅者积压时丢弃，客户端可通过下一次 status 快照补齐。
		}
	}
}

// SubscriberCount 返回当前订阅者数量。
func (h *EventHub) SubscriberCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// PublishBotEvent 广播一条机器人事件记录。
func (h *EventHub) PublishBotEvent(event assistant.EventRecord) {
	h.Publish("bot_event", event)
}

// EventStreamHandler 通过 SSE 向前端推送状态、统计和实时事件。
type EventStreamHandler struct {
	hub         *EventHub
	runtime     statsStatusProvider
	collector   *StatsCollector
	storagePath string
}

// NewEventStreamHandler 创建 EventStreamHandler 实例。
func NewEventStreamHandler(hub *EventHub, runtime statsStatusProvider, collector *StatsCollector, storagePaths ...string) *EventStreamHandler {
	storagePath := ""
	if len(storagePaths) > 0 {
		storagePath = storagePaths[0]
	}
	return &EventStreamHandler{hub: hub, runtime: runtime, collector: collector, storagePath: storagePath}
}

// Register 注册当前模块的路由或能力。
func (h *EventStreamHandler) Register(router gin.IRouter) {
	router.GET("/api/events", h.stream)
}

// StartWatcher 启动状态观察循环：状态变化时向订阅者广播最新快照。
// interval 建议 1-3 秒；无订阅者时只做轻量比较，不产生额外负担。
func (h *EventStreamHandler) StartWatcher(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		var lastSignature string
		var lastStatsAt time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if h.hub.SubscriberCount() == 0 {
					continue
				}
				status := h.runtime.Status()
				signature := statusSignature(status)
				statusChanged := signature != lastSignature
				if statusChanged {
					lastSignature = signature
					h.hub.Publish("status", status)
				}
				if h.collector != nil && (statusChanged || time.Since(lastStatsAt) >= dashboardServerStatsCacheTTL) {
					snapshot := h.collector.SnapshotWithProfiles()
					snapshot.Bot = summarizeBotStatus(status)
					snapshot.Server = cachedDashboardServerStats(time.Now(), h.storagePath)
					h.hub.Publish("stats", snapshot)
					lastStatsAt = time.Now()
				}
			}
		}
	}()
}

// statusSignature 提取状态中影响 UI 展示的关键字段做变化检测。
func statusSignature(status assistant.RuntimeStatus) string {
	recentAt := ""
	if len(status.RecentEvents) > 0 {
		recentAt = status.RecentEvents[0].At.Format(time.RFC3339Nano)
	}
	var channels strings.Builder
	for _, channel := range status.Channels {
		fmt.Fprintf(&channels, "%s:%s:%t:%t:%t:%t:%s:%s:%s:%s|",
			channel.ProfileID,
			channel.Platform,
			channel.Connected,
			channel.AccountStatusKnown,
			channel.AccountOnline,
			channel.AccountGood,
			channel.SelfID,
			channel.LastError,
			channel.AccountStatusMessage,
			channel.UpdatedAt.Format(time.RFC3339Nano),
		)
	}
	return fmt.Sprintf("%t|%t|%s|%s|%s|%t|%t|%d|%s|%s|%s",
		status.Running,
		status.Channel.Connected,
		status.Channel.SelfID,
		status.Channel.LastError,
		channels.String(),
		status.NoneBotBridge.Enabled,
		status.NoneBotBridge.Connected,
		status.ActiveWorkers,
		status.LastError,
		status.UpdatedAt.Format(time.RFC3339Nano),
		recentAt,
	)
}

// stream 处理 SSE 连接：先推送一次全量快照，之后持续推送变更和心跳。
func (h *EventStreamHandler) stream(c *gin.Context) {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		writeError(c, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	sub := h.hub.Subscribe()
	defer h.hub.Unsubscribe(sub)

	// 建连即推送全量快照，前端无需再发起额外请求。
	if h.runtime != nil {
		status := h.runtime.Status()
		writeSSE(c, flusher, "status", status)
		if h.collector != nil {
			snapshot := h.collector.SnapshotWithProfiles()
			snapshot.Bot = summarizeBotStatus(status)
			snapshot.Server = cachedDashboardServerStats(time.Now(), h.storagePath)
			writeSSE(c, flusher, "stats", snapshot)
		}
	}

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	ctx := c.Request.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case message, ok := <-sub:
			if !ok {
				return
			}
			writeSSERaw(c, flusher, message)
		case <-heartbeat.C:
			// 注释行作为心跳，保持代理和浏览器连接活跃。
			if _, err := fmt.Fprint(c.Writer, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// writeSSE 序列化 payload 并写出一条 SSE 消息。
func writeSSE(c *gin.Context, flusher http.Flusher, event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	writeSSERaw(c, flusher, sseMessage{Event: event, Data: data})
}

// writeSSERaw 写出一条已序列化的 SSE 消息并立即 flush。
func writeSSERaw(c *gin.Context, flusher http.Flusher, message sseMessage) {
	if _, err := fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", message.Event, message.Data); err != nil {
		return
	}
	flusher.Flush()
}
