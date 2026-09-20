// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/applog"
)

// 待发私聊：发不出去的那条先存着，等加上好友再自动送出去。
//
// QQ 上给非好友发私聊只有临时会话一条路，而临时会话可以被对方的隐私设置关掉，
// 也不是每个 OneBot 实现都支持。走不通时的选择只有两个：当场告诉对方「发不了」，
// 让他记得回头再要一次；或者把内容存下来，等关系建立了自己发出去。前者把已经
// 做完的活儿又丢回给人，正是这个工具本来要解决的事。
//
// 好友关系怎么建立不归它管：请求仍然要主人在 onebot_requests 里同意，这里只负责
// 在关系建立的那一刻把欠着的话补上。自动发的是内容，不是好友请求——机器人不会
// 因为有东西要发就替主人放人进来。
const (
	// pendingDirectMessageTTL 是托管内容的保质期。七天没加上好友，那句话多半已经
	// 过期了：与其某天突然弹出一条没头没尾的旧消息，不如让它安静作废。
	pendingDirectMessageTTL = 7 * 24 * time.Hour
	// pendingDirectMessagePerUser 限制同一个人最多攒几条。攒成一串在加上好友的
	// 瞬间一起轰出去，比发不出去更吓人。
	pendingDirectMessagePerUser = 3
	pendingDirectMessageTimeout = 20 * time.Second
	// pendingDirectMessagePurgeInterval 是过期条目的清理间隔。它们已经作废，
	// 多躺几个钟头不影响任何行为。
	pendingDirectMessagePurgeInterval = 24 * time.Hour
)

// PendingDirectMessage 是一条等着关系建立后再发的私聊。
type PendingDirectMessage struct {
	ID        string `json:"id"`
	ProfileID string `json:"profile_id"`
	Platform  string `json:"platform,omitempty"`
	UserID    string `json:"user_id"`
	// SourceSession 记下是从哪条会话托付的，投递时写进事件流，管理员才查得到
	// 这条私聊的来历。
	SourceSession string    `json:"source_session,omitempty"`
	Message       string    `json:"message"`
	CreatedAt     time.Time `json:"created_at"`
	ExpiresAt     time.Time `json:"expires_at"`
}

// PendingDirectMessageStore 持久化待发私聊。没有配置存储时这项能力安静降级：
// 发不出去就直说发不出去，不假装存下了。
type PendingDirectMessageStore interface {
	SavePendingDirectMessage(ctx context.Context, item PendingDirectMessage) (PendingDirectMessage, error)
	// CountPendingDirectMessages 只数没过期的。
	CountPendingDirectMessages(ctx context.Context, profileID, userID string, now time.Time) (int, error)
	// TakePendingDirectMessages 取出并删除某个人名下所有没过期的待发私聊。
	// 取和删必须在同一个事务里：好友通知和主人审批可能前后脚到达，各取一次就
	// 会把同一条话发两遍。
	TakePendingDirectMessages(ctx context.Context, profileID, userID string, now time.Time) ([]PendingDirectMessage, error)
	PurgeExpiredPendingDirectMessages(ctx context.Context, now time.Time) (int, error)
}

func (r *Runtime) SetPendingDirectMessageStore(store PendingDirectMessageStore) {
	r.mu.Lock()
	r.pendingDirect = store
	r.mu.Unlock()
}

func (r *Runtime) pendingDirectMessageStore() PendingDirectMessageStore {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.pendingDirect
}

// parkPendingDirectMessage 把发不出去的那条存起来，等加上好友再发。
func (r *Runtime) parkPendingDirectMessage(ctx context.Context, source MessageEvent, event MessageEvent, message string) error {
	store := r.pendingDirectMessageStore()
	if store == nil {
		return fmt.Errorf("没有配置待发私聊存储，存不下来")
	}
	now := r.clock()
	count, err := store.CountPendingDirectMessages(ctx, event.ProfileID, event.UserID, now)
	if err != nil {
		return err
	}
	if count >= pendingDirectMessagePerUser {
		return fmt.Errorf("已经有 %d 条在等着发给这个人了，先让对方加上好友再说", count)
	}
	item := PendingDirectMessage{
		ProfileID:     strings.TrimSpace(event.ProfileID),
		Platform:      NormalizePlatformID(event.Platform),
		UserID:        strings.TrimSpace(event.UserID),
		SourceSession: sessionKey(source),
		Message:       message,
		CreatedAt:     now,
		ExpiresAt:     now.Add(pendingDirectMessageTTL),
	}
	stored, err := store.SavePendingDirectMessage(ctx, item)
	if err != nil {
		return err
	}
	r.recordPendingDirectMessageParked(ctx, source, stored)
	return nil
}

// flushPendingDirectMessages 把某个人名下欠着的私聊补发出去。
//
// 触发点有两个：OneBot 的 friend_add 通知，以及主人在 onebot_requests 里同意好友
// 请求。两个都留着——通知覆盖「主人在手机上直接同意」，审批覆盖「实现不报这条
// 通知」。取出即删除，所以两条路谁先到都只发一次。
func (r *Runtime) flushPendingDirectMessages(ctx context.Context, event MessageEvent) {
	store := r.pendingDirectMessageStore()
	userID := strings.TrimSpace(event.UserID)
	if store == nil || userID == "" {
		return
	}
	// 名册缓存还说「不是好友」的话，补发出去的消息又会被推去走临时会话。
	r.forgetOneBotFriendRoster(event)
	takeCtx, cancel := context.WithTimeout(ctx, pendingDirectMessageTimeout)
	items, err := store.TakePendingDirectMessages(takeCtx, strings.TrimSpace(event.ProfileID), userID, r.clock())
	cancel()
	if err != nil {
		applogWarn(ctx, r, "pending_direct_message_take_failed", "待发私聊取用失败", err)
		return
	}
	for _, item := range items {
		r.deliverPendingDirectMessage(ctx, event, item)
	}
}

// deliverPendingDirectMessage 发一条补发的私聊。
//
// 内容在取出时已经从库里删掉了：发送失败会损失这条话，但比进程在发送和删除之间
// 崩掉、下次启动再发一遍强——同一段小作文隔几天重来一遍，比没收到更难解释。
// 失败不是静悄悄的：整段内容进运行日志，主人查得到丢了什么。
func (r *Runtime) deliverPendingDirectMessage(ctx context.Context, trigger MessageEvent, item PendingDirectMessage) {
	event := MessageEvent{
		Kind:             EventKindPrivate,
		Platform:         firstNonEmpty(item.Platform, trigger.Platform),
		ProfileID:        firstNonEmpty(item.ProfileID, trigger.ProfileID),
		ContextNamespace: trigger.ContextNamespace,
		SelfID:           trigger.SelfID,
		UserID:           item.UserID,
		Time:             r.clock().Unix(),
	}
	sendCtx, cancel := context.WithTimeout(ctx, pendingDirectMessageTimeout)
	_, err := r.sendDecorated(sendCtx, event, item.Message, outboundDecoration{})
	cancel()
	if err == nil {
		r.record(EventRecord{
			At:        time.Now(),
			Kind:      EventKindPrivate,
			Platform:  event.Platform,
			ProfileID: event.ProfileID,
			UserID:    event.UserID,
			Text:      "[pending_direct_message] " + item.SourceSession,
			Reply:     item.Message,
			Handled:   true,
			Outcome:   "pending_direct_message",
			Decision:  "replied",
			Reason:    "加上好友后补发之前发不出去的私聊",
		})
	}
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	entry := applog.Entry{
		Kind:    applog.KindOperation,
		Level:   applog.LevelInfo,
		Action:  "pending_direct_message_sent",
		Message: "加上好友后补发了之前存下的私聊",
		Target:  item.UserID,
		Metadata: map[string]any{
			"user_id":        item.UserID,
			"source_session": item.SourceSession,
			"parked_at":      item.CreatedAt.Format(time.RFC3339),
			"preview":        truncateRunesFromStart(item.Message, 200),
		},
		CreatedAt: time.Now(),
	}
	if err != nil {
		entry.Kind = applog.KindError
		entry.Level = applog.LevelError
		entry.Message = "补发存下的私聊失败，这条内容已经丢失"
		entry.Detail = err.Error()
		// 失败时留全文：库里已经没有了，日志是唯一还能找回这段话的地方。
		entry.Metadata["message"] = item.Message
	}
	_ = writer.AppendLog(ctx, entry)
}

func (r *Runtime) recordPendingDirectMessageParked(ctx context.Context, source MessageEvent, item PendingDirectMessage) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	_ = writer.AppendLog(ctx, applog.Entry{
		Kind:    applog.KindOperation,
		Level:   applog.LevelInfo,
		Action:  "pending_direct_message_parked",
		Message: "私聊发不出去，内容已存下等加上好友再发",
		Actor:   oneBotEventActor(source),
		Target:  item.UserID,
		Metadata: map[string]any{
			"user_id":        item.UserID,
			"source_session": item.SourceSession,
			"expires_at":     item.ExpiresAt.Format(time.RFC3339),
			"preview":        truncateRunesFromStart(item.Message, 200),
		},
		CreatedAt: time.Now(),
	})
}

func applogWarn(ctx context.Context, r *Runtime, action, message string, err error) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	_ = writer.AppendLog(ctx, applog.Entry{
		Kind:      applog.KindError,
		Level:     applog.LevelError,
		Action:    action,
		Message:   message,
		Detail:    err.Error(),
		CreatedAt: time.Now(),
	})
}

// runPendingDirectMessagePurgeLoop 定期清掉过期的托管内容。
//
// 取用时会顺手清掉那个人名下的过期条目，但「一直没来加好友」的人永远不会触发
// 取用——那些条目只能靠这条循环收走。一天一次足够：它们已经作废，多躺几个钟头
// 不影响任何行为。
func (r *Runtime) runPendingDirectMessagePurgeLoop(ctx context.Context) {
	ticker := time.NewTicker(pendingDirectMessagePurgeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			store := r.pendingDirectMessageStore()
			if store == nil {
				continue
			}
			purgeCtx, cancel := context.WithTimeout(ctx, pendingDirectMessageTimeout)
			_, err := store.PurgeExpiredPendingDirectMessages(purgeCtx, r.clock())
			cancel()
			if err != nil {
				applogWarn(ctx, r, "pending_direct_message_purge_failed", "过期待发私聊清理失败", err)
			}
		}
	}
}
