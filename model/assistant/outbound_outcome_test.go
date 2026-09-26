// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SuInk/diana/model/applog"
	"github.com/gorilla/websocket"
)

const (
	outcomeTestSelfID  = "10000"
	outcomeTestGroupID = "20005"
	outcomeTestUserID  = "10001"
)

// ambiguousOutboundChannel 按脚本给出每次发送的结果，并模拟 OneBot 的历史接口。
type ambiguousOutboundChannel struct {
	mu           sync.Mutex
	outcomes     []error
	sent         []OutgoingMessage
	onSend       func(attempt int)
	history      []map[string]any
	historyErr   error
	historyCalls []string
	nextID       int
	offline      atomic.Bool
}

func ambiguousSendError(action string) error {
	return &outboundOutcomeUnknownError{action: action, cause: context.DeadlineExceeded}
}

func (c *ambiguousOutboundChannel) OutboundBackoffEnabled() bool { return true }

func (c *ambiguousOutboundChannel) Connect(context.Context, EventHandler) error { return nil }

func (c *ambiguousOutboundChannel) Send(ctx context.Context, msg OutgoingMessage) error {
	_, err := c.SendWithResult(ctx, msg)
	return err
}

func (c *ambiguousOutboundChannel) SendWithResult(_ context.Context, msg OutgoingMessage) (map[string]any, error) {
	c.mu.Lock()
	c.sent = append(c.sent, msg)
	attempt := len(c.sent)
	var err error
	if len(c.outcomes) > 0 {
		err = c.outcomes[0]
		c.outcomes = c.outcomes[1:]
	}
	c.nextID++
	id := 60000 + c.nextID
	hook := c.onSend
	c.mu.Unlock()
	if hook != nil {
		hook(attempt)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"message_id": id}, nil
}

func (c *ambiguousOutboundChannel) CallAPI(_ context.Context, action string, _ map[string]any) (map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch action {
	case "get_group_list":
		return map[string]any{"items": []any{map[string]any{"group_id": outcomeTestGroupID}}}, nil
	case "get_group_msg_history", "get_friend_msg_history":
		c.historyCalls = append(c.historyCalls, action)
		if c.historyErr != nil {
			return nil, c.historyErr
		}
		messages := make([]any, 0, len(c.history))
		for _, item := range c.history {
			messages = append(messages, item)
		}
		return map[string]any{"messages": messages}, nil
	}
	return map[string]any{}, nil
}

func (c *ambiguousOutboundChannel) Status() ChannelStatus {
	return ChannelStatus{Connected: !c.offline.Load(), SelfID: outcomeTestSelfID}
}

func (c *ambiguousOutboundChannel) Close() error { return nil }

func (c *ambiguousOutboundChannel) sentCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.sent)
}

func (c *ambiguousOutboundChannel) historyCallCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.historyCalls)
}

type failingOutcomeAppLogs struct {
	mu      sync.Mutex
	actions []string
}

func (w *failingOutcomeAppLogs) AppendLog(_ context.Context, entry applog.Entry) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.actions = append(w.actions, entry.Action)
	return errors.New("database is locked")
}

func newAmbiguousOutcomeRuntime(t *testing.T, channel Channel) *Runtime {
	t.Helper()
	runtime := NewRuntime(BotConfig{BotAccount: outcomeTestSelfID}, channel, NewPluginManager(), nil, nil, nil, nil)
	// 等回推的窗口压到毫秒级：用例里的回推要么在发送返回前就到了，要么根本不来。
	runtime.outboundEchoes.echoWait = 20 * time.Millisecond
	runtime.outboundEchoes.historyTimeout = time.Second
	return runtime
}

func captureStdLog(t *testing.T) *lockedBuffer {
	t.Helper()
	buffer := &lockedBuffer{}
	previous := log.Writer()
	log.SetOutput(buffer)
	t.Cleanup(func() { log.SetOutput(previous) })
	return buffer
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func groupOutcomeEvent() MessageEvent {
	return MessageEvent{Kind: EventKindGroup, GroupID: outcomeTestGroupID, UserID: outcomeTestUserID, MessageID: "12345"}
}

func writeOutcomeTestImage(t *testing.T, content string) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "generated.png")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := md5.Sum([]byte(content))
	return path, strings.ToUpper(hex.EncodeToString(sum[:]))
}

// napCatMessageSentFrame 是 NapCat / SnowLuma 推回机器人自己发出的消息的原样帧：
// post_type 是 message_sent，user_id 等于 self_id；私聊的对方在 target_id 里。
// 群里带引用的生成图，回推是 reply 段 + 文本 + 图片段，图片的 file 是内容 MD5
// 加扩展名，url 是 QQ 的媒体地址。imageMD5 为空时只有文本段。
func napCatMessageSentFrame(messageID int, messageType, peerID, text, imageMD5 string) []byte {
	segments := []any{
		map[string]any{"type": "reply", "data": map[string]any{"id": "12345"}},
		map[string]any{"type": "text", "data": map[string]any{"text": text}},
	}
	raw := "[CQ:reply,id=12345]" + text
	if imageMD5 != "" {
		segments = append(segments, map[string]any{"type": "image", "data": map[string]any{
			"summary": "", "file": imageMD5 + ".png", "sub_type": 0,
			"url":       "https://multimedia.nt.qq.com.cn/download?appid=1407&fileid=outcome",
			"file_size": "18",
		}})
		raw += "[CQ:image,file=" + imageMD5 + ".png]"
	}
	frame := map[string]any{
		"self_id": 10000, "user_id": 10000, "time": time.Now().Unix(),
		"message_id": messageID, "message_seq": messageID, "real_id": messageID,
		"message_type": messageType, "sub_type": "normal", "post_type": "message_sent", "font": 14,
		"sender":         map[string]any{"user_id": 10000, "nickname": "Diana", "card": "", "role": "member"},
		"raw_message":    raw,
		"message_format": "array",
		"message":        segments,
	}
	if messageType == "group" {
		frame["group_id"] = mustAtoi(peerID)
	} else {
		frame["sub_type"] = "friend"
		frame["target_id"] = mustAtoi(peerID)
	}
	encoded, _ := json.Marshal(frame)
	return encoded
}

func mustAtoi(value string) int {
	parsed, _ := strconv.Atoi(value)
	return parsed
}

// newEchoFeed 是一条反向 WebSocket 连接的收帧入口，事件交给 runtime.HandleEvent，
// 和线上接入端推帧走同一条路。
func newEchoFeed(runtime *Runtime) *OneBotReverseServer {
	server := NewOneBotReverseServer(OneBotConfig{Endpoint: "ws://127.0.0.1:18080/onebot/v11/ws"})
	server.mu.Lock()
	server.handler = runtime.HandleEvent
	server.ctx = context.Background()
	server.mu.Unlock()
	return server
}

func feedFrame(t *testing.T, server *OneBotReverseServer, frame []byte) {
	t.Helper()
	if err := server.handleFrame(frame); err != nil {
		t.Errorf("handleFrame() error = %v", err)
	}
}

func selfHistoryItem(messageID int, when time.Time, text string) map[string]any {
	return map[string]any{
		"self_id":      outcomeTestSelfID,
		"user_id":      outcomeTestSelfID,
		"time":         when.Unix(),
		"message_id":   messageID,
		"message_type": "group",
		"group_id":     outcomeTestGroupID,
		"sender":       map[string]any{"user_id": outcomeTestSelfID, "nickname": "Diana"},
		"message":      []any{map[string]any{"type": "text", "data": map[string]any{"text": text}}},
	}
}

// 那次重复发图的原样：生成图带引用发出去，接入端 30 秒没回执，其实已经发出去了。
// 回推到了就认它，不再重发，message_id 用回推里的。
func TestAmbiguousSendConfirmedByEchoIsNotResent(t *testing.T) {
	logs := captureStdLog(t)
	imagePath, imageMD5 := writeOutcomeTestImage(t, "generated image one")
	channel := &ambiguousOutboundChannel{outcomes: []error{ambiguousSendError("send_group_msg")}}
	runtime := newAmbiguousOutcomeRuntime(t, channel)
	runtime.outboundEchoes.echoWait = 5 * time.Second
	feed := newEchoFeed(runtime)
	// 回推在超时之前就推过来了：先一条同样正文、别的图的（另一次发送），再是真正那条。
	channel.onSend = func(attempt int) {
		if attempt != 1 {
			return
		}
		feedFrame(t, feed, napCatMessageSentFrame(54320, "group", outcomeTestGroupID, "图片生成完成。", strings.Repeat("A", 32)))
		feedFrame(t, feed, napCatMessageSentFrame(54321, "group", outcomeTestGroupID, "图片生成完成。", imageMD5))
		// 收帧是异步交给 HandleEvent 的；等两条都记下，再让发送超时返回。
		waitForObservedEchoes(runtime, 2)
	}

	ctx := withOutboundDeliveryPolicy(context.Background(), recoveringOutboundDeliveryPolicy())
	result, err := runtime.sendOutgoingWithResult(ctx, groupOutcomeEvent(), OutgoingMessage{
		Text:           "图片生成完成。",
		ImageURLs:      []string{imagePath},
		ReplyMessageID: "12345",
	})
	if err != nil {
		t.Fatalf("sendOutgoingWithResult() error = %v", err)
	}
	if got := channel.sentCount(); got != 1 {
		t.Fatalf("send attempts = %d, want 1 (no resend after echo)", got)
	}
	if got := apiMessageID(result); got != "54321" {
		t.Fatalf("outbound message id = %q, want the echoed 54321", got)
	}
	if got := channel.historyCallCount(); got != 0 {
		t.Fatalf("history lookups = %d, want 0 when the echo already confirmed delivery", got)
	}
	output := logs.String()
	for _, want := range []string{"发送结果不明（超时），等待回执确认", "已确认送达（回执）"} {
		if !strings.Contains(output, want) {
			t.Fatalf("stdout log missing %q:\n%s", want, output)
		}
	}
}

// 回推晚于开始等待才到，也要被叫醒认领；这里走正向 WebSocket 的收帧入口。
func TestAmbiguousSendWaitsForLateEchoThroughForwardFrame(t *testing.T) {
	channel := &ambiguousOutboundChannel{outcomes: []error{ambiguousSendError("send_group_msg")}}
	runtime := newAmbiguousOutcomeRuntime(t, channel)
	runtime.outboundEchoes.echoWait = 5 * time.Second
	forward := NewOneBotChannel(OneBotConfig{Endpoint: "ws://127.0.0.1:3001"})
	channel.onSend = func(attempt int) {
		if attempt != 1 {
			return
		}
		go func() {
			waitUntilEchoAwaited(runtime)
			frame := napCatMessageSentFrame(54330, "group", outcomeTestGroupID, "好的，马上 就来", "")
			if err := forward.handleFrame(context.Background(), runtime.HandleEvent, frame); err != nil {
				t.Errorf("handleFrame() error = %v", err)
			}
		}()
	}

	started := time.Now()
	result, err := runtime.sendOutgoingWithResult(context.Background(), groupOutcomeEvent(), OutgoingMessage{Text: "好的，马上就来"})
	if err != nil {
		t.Fatalf("sendOutgoingWithResult() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("confirmation waited %s instead of waking on the echo", elapsed)
	}
	if got := channel.sentCount(); got != 1 || apiMessageID(result) != "54330" {
		t.Fatalf("attempts=%d id=%q", got, apiMessageID(result))
	}
}

func TestAmbiguousSendConfirmedByHistoryIsNotResent(t *testing.T) {
	logs := captureStdLog(t)
	now := time.Now()
	channel := &ambiguousOutboundChannel{
		outcomes: []error{ambiguousSendError("send_group_msg")},
		history: []map[string]any{
			// 很早以前一模一样的一句不算：时间早于这次发送。
			selfHistoryItem(54300, now.Add(-10*time.Minute), "收到"),
			{"self_id": outcomeTestSelfID, "user_id": outcomeTestUserID, "time": now.Unix(), "message_id": 54301,
				"message_type": "group", "group_id": outcomeTestGroupID,
				"message": []any{map[string]any{"type": "text", "data": map[string]any{"text": "收到"}}}},
			selfHistoryItem(54302, now, "收到"),
		},
	}
	runtime := newAmbiguousOutcomeRuntime(t, channel)
	appLogs := &failingOutcomeAppLogs{}
	runtime.SetAppLogWriter(appLogs)

	result, err := runtime.sendOutgoingWithResult(context.Background(), groupOutcomeEvent(), OutgoingMessage{Text: "收到"})
	if err != nil {
		t.Fatalf("sendOutgoingWithResult() error = %v", err)
	}
	if got := channel.sentCount(); got != 1 {
		t.Fatalf("send attempts = %d, want 1", got)
	}
	if got := apiMessageID(result); got != "54302" {
		t.Fatalf("outbound message id = %q, want 54302 from history", got)
	}
	if got := channel.historyCallCount(); got != 1 {
		t.Fatalf("history lookups = %d, want 1", got)
	}
	// 应用日志写库失败时，标准输出仍然有记录，写库失败本身也留了一笔。
	output := logs.String()
	for _, want := range []string{"发送结果不明（超时），等待回执确认", "已确认送达（历史）", "not persisted: database is locked"} {
		if !strings.Contains(output, want) {
			t.Fatalf("stdout log missing %q:\n%s", want, output)
		}
	}
	appLogs.mu.Lock()
	defer appLogs.mu.Unlock()
	if strings.Join(appLogs.actions, ",") != "outbound_outcome_unknown,outbound_outcome_confirmed" {
		t.Fatalf("app log actions = %v", appLogs.actions)
	}
}

func TestAmbiguousSendAbsentFromEchoAndHistoryResendsOnce(t *testing.T) {
	logs := captureStdLog(t)
	channel := &ambiguousOutboundChannel{
		outcomes: []error{ambiguousSendError("send_group_msg"), nil},
		history:  []map[string]any{selfHistoryItem(54310, time.Now(), "另一句话")},
	}
	runtime := newAmbiguousOutcomeRuntime(t, channel)
	ctx := withOutboundDeliveryPolicy(context.Background(), recoveringOutboundDeliveryPolicy())

	result, err := runtime.sendOutgoingWithResult(ctx, groupOutcomeEvent(), OutgoingMessage{Text: "这一句没发出去"})
	if err != nil {
		t.Fatalf("sendOutgoingWithResult() error = %v", err)
	}
	if got := channel.sentCount(); got != 2 {
		t.Fatalf("send attempts = %d, want exactly one resend", got)
	}
	if got := apiMessageID(result); got != "60002" {
		t.Fatalf("outbound message id = %q, want the resend's id", got)
	}
	if !strings.Contains(logs.String(), "确认未送达，重发一次") {
		t.Fatalf("stdout log missing resend notice:\n%s", logs.String())
	}
}

// 重发那一次又是结果不明、确认后仍然没有，就此放下，不再进指数退避链。
func TestAmbiguousResendStillAbsentIsDroppedWithoutBackoff(t *testing.T) {
	logs := captureStdLog(t)
	channel := &ambiguousOutboundChannel{
		outcomes: []error{ambiguousSendError("send_group_msg"), ambiguousSendError("send_group_msg"), nil},
	}
	runtime := newAmbiguousOutcomeRuntime(t, channel)
	ctx := withOutboundDeliveryPolicy(context.Background(), recoveringOutboundDeliveryPolicy())

	_, err := runtime.sendOutgoingWithResult(ctx, groupOutcomeEvent(), OutgoingMessage{Text: "两次都不明"})
	if !errors.Is(err, errOutboundOutcomeUnconfirmed) || !errors.Is(err, errOutboundDeliveryDropped) {
		t.Fatalf("error = %v, want unconfirmed + dropped", err)
	}
	if got := channel.sentCount(); got != 2 {
		t.Fatalf("send attempts = %d, want the original plus exactly one resend", got)
	}
	if got := channel.historyCallCount(); got != 2 {
		t.Fatalf("history lookups = %d, want one per ambiguous outcome", got)
	}
	if !strings.Contains(logs.String(), "重发后仍未确认送达，不再重发") {
		t.Fatalf("stdout log missing give-up notice:\n%s", logs.String())
	}
}

// deliveringSlowChannel 复现那次三连发：接入端每次都把消息发到了 QQ，只是上传
// 超过 30 秒，Diana 这边每次都等到超时。platform 记的是群里真正出现的消息。
type deliveringSlowChannel struct {
	ambiguousOutboundChannel
	platform  []map[string]any
	onDeliver func(messageID int, msg OutgoingMessage)
}

func (c *deliveringSlowChannel) SendWithResult(_ context.Context, msg OutgoingMessage) (map[string]any, error) {
	c.mu.Lock()
	c.sent = append(c.sent, msg)
	messageID := 54400 + len(c.sent)
	item := selfHistoryItem(messageID, time.Now(), msg.Text)
	c.platform = append(c.platform, item)
	c.history = append(c.history, item)
	hook := c.onDeliver
	c.mu.Unlock()
	if hook != nil {
		hook(messageID, msg)
	}
	return nil, ambiguousSendError("send_group_msg")
}

func (c *deliveringSlowChannel) Send(ctx context.Context, msg OutgoingMessage) error {
	_, err := c.SendWithResult(ctx, msg)
	return err
}

func (c *deliveringSlowChannel) platformCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.platform)
}

func waitForObservedEchoes(runtime *Runtime, count int) {
	for {
		runtime.outboundEchoes.mu.Lock()
		observed := len(runtime.outboundEchoes.echoes)
		runtime.outboundEchoes.mu.Unlock()
		if observed >= count {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

// waitUntilEchoAwaited 等到确认流程已经开始等回推，用来模拟「回推晚到」。
func waitUntilEchoAwaited(runtime *Runtime) {
	for {
		runtime.outboundEchoes.mu.Lock()
		waiting := runtime.outboundEchoes.changed != nil
		runtime.outboundEchoes.mu.Unlock()
		if waiting {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

// 20:40:51、20:42:29、20:45:21 三次拉取同一个分享链接：30 秒超时 + 60~72 秒退避，
// 再 30 秒超时 + 120~144 秒退避。每一次其实都发出去了。修复后无论回推晚到还是
// 根本不来（只能查历史），群里都只有一条。
func TestRepeatedAmbiguousTimeoutsThatAllDeliverProduceOneMessage(t *testing.T) {
	for _, tc := range []struct {
		name       string
		echoes     bool
		wantSource string
	}{
		{name: "late echo", echoes: true, wantSource: "已确认送达（回执）"},
		{name: "no echo, history", echoes: false, wantSource: "已确认送达（历史）"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureStdLog(t)
			channel := &deliveringSlowChannel{}
			runtime := newAmbiguousOutcomeRuntime(t, channel)
			base := time.Now()
			var offset atomic.Int64
			runtime.outboundEchoes.now = func() time.Time { return base.Add(time.Duration(offset.Load())) }
			if tc.echoes {
				runtime.outboundEchoes.echoWait = 5 * time.Second
				feed := newEchoFeed(runtime)
				channel.onDeliver = func(messageID int, msg OutgoingMessage) {
					go func() {
						// 回推在确认开始等待之后才到，按接入端时间已经过去 45 秒。
						waitUntilEchoAwaited(runtime)
						offset.Store(int64(45 * time.Second))
						feedFrame(t, feed, napCatMessageSentFrame(messageID, "group", outcomeTestGroupID, msg.Text, ""))
					}()
				}
			}
			// 退避调成毫秒级：一旦又掉回退避链，这里会立刻多发几条而不是卡住。
			ctx := withOutboundDeliveryPolicy(context.Background(), recoveringOutboundDeliveryPolicy())

			result, err := runtime.sendOutgoingWithResult(ctx, groupOutcomeEvent(), OutgoingMessage{Text: "图片生成完成。"})
			if err != nil {
				t.Fatalf("sendOutgoingWithResult() error = %v", err)
			}
			if got := channel.platformCount(); got != 1 {
				t.Fatalf("messages on the platform = %d, want exactly 1", got)
			}
			if got := apiMessageID(result); got != "54401" {
				t.Fatalf("outbound message id = %q, want 54401", got)
			}
			if !strings.Contains(logs.String(), tc.wantSource) {
				t.Fatalf("stdout log missing %q:\n%s", tc.wantSource, logs.String())
			}
		})
	}
}

// 回推和历史都拿不到结论时不重发，交给上层当作丢弃，不会把整条回复重新生成一遍。
func TestAmbiguousSendWithoutEvidenceIsNotResent(t *testing.T) {
	channel := &ambiguousOutboundChannel{
		outcomes:   []error{ambiguousSendError("send_group_msg")},
		historyErr: errors.New("get_group_msg_history timeout"),
	}
	runtime := newAmbiguousOutcomeRuntime(t, channel)
	ctx := withOutboundDeliveryPolicy(context.Background(), recoveringOutboundDeliveryPolicy())

	_, err := runtime.sendOutgoingWithResult(ctx, groupOutcomeEvent(), OutgoingMessage{Text: "查不清"})
	if !errors.Is(err, errOutboundOutcomeUnconfirmed) || !errors.Is(err, errOutboundDeliveryDropped) {
		t.Fatalf("error = %v, want unconfirmed + dropped", err)
	}
	if got := channel.sentCount(); got != 1 {
		t.Fatalf("send attempts = %d, want 1", got)
	}
	// 这个群后面的消息照常发，不因为这一条进冷却。
	if _, err := runtime.sendOutgoingWithResult(ctx, groupOutcomeEvent(), OutgoingMessage{Text: "下一条"}); err != nil {
		t.Fatalf("next send error = %v", err)
	}
}

// 私聊不走群退避门，但以前会在 sendChannelPayloadWithRetry 里原地重试超时的
// 发送。结果不明时同样先确认。
func TestAmbiguousPrivateSendConfirmedByFriendHistory(t *testing.T) {
	channel := &ambiguousOutboundChannel{
		outcomes: []error{ambiguousSendError("send_private_msg")},
		history: []map[string]any{{
			"self_id": outcomeTestSelfID, "user_id": outcomeTestSelfID, "target_id": outcomeTestUserID,
			"time": time.Now().Unix(), "message_id": 54340, "message_type": "private",
			"message": []any{map[string]any{"type": "text", "data": map[string]any{"text": "私聊回复"}}},
		}},
	}
	runtime := newAmbiguousOutcomeRuntime(t, channel)
	event := MessageEvent{Kind: EventKindPrivate, UserID: outcomeTestUserID, MessageID: "12346"}

	result, err := runtime.sendOutgoingWithResult(context.Background(), event, OutgoingMessage{Text: "私聊回复"})
	if err != nil {
		t.Fatalf("sendOutgoingWithResult() error = %v", err)
	}
	if got := channel.sentCount(); got != 1 || apiMessageID(result) != "54340" {
		t.Fatalf("attempts=%d id=%q", got, apiMessageID(result))
	}
	channel.mu.Lock()
	defer channel.mu.Unlock()
	if len(channel.historyCalls) != 1 || channel.historyCalls[0] != "get_friend_msg_history" {
		t.Fatalf("history calls = %v", channel.historyCalls)
	}
}

// 确定失败（接入端明确报错）维持原来的退避重发，不去确认。
func TestDefiniteSendFailureKeepsBackoffWithoutConfirmation(t *testing.T) {
	logs := captureStdLog(t)
	channel := &ambiguousOutboundChannel{outcomes: []error{errors.New("onebot api failed: status=failed retcode=1200"), nil}}
	runtime := newAmbiguousOutcomeRuntime(t, channel)
	ctx := withOutboundDeliveryPolicy(context.Background(), recoveringOutboundDeliveryPolicy())

	if _, err := runtime.sendOutgoingWithResult(ctx, groupOutcomeEvent(), OutgoingMessage{Text: "退避后重发"}); err != nil {
		t.Fatalf("sendOutgoingWithResult() error = %v", err)
	}
	if got := channel.sentCount(); got != 2 {
		t.Fatalf("send attempts = %d, want 2", got)
	}
	if got := channel.historyCallCount(); got != 0 {
		t.Fatalf("history lookups = %d, want 0 for a definite failure", got)
	}
	output := logs.String()
	if strings.Contains(output, "发送结果不明") {
		t.Fatalf("definite failure went through confirmation:\n%s", output)
	}
	// 退避日志现在也进标准输出，数据库忙时不会整条消失。
	if !strings.Contains(output, "群消息发送失败，已按指数退避等待下次尝试") {
		t.Fatalf("backoff not logged to stdout:\n%s", output)
	}
}

func TestOneBotCallTimeoutSelectsMediaTimeout(t *testing.T) {
	text := map[string]any{"message": buildOutgoingSegments(OutgoingMessage{Text: "hi"})}
	image := map[string]any{"message": buildOutgoingSegments(OutgoingMessage{Text: "图片生成完成。", ImageURLs: []string{"http://127.0.0.1:18080/media/token.png"}})}
	record := map[string]any{"message": []any{map[string]any{"type": "record", "data": map[string]any{"file": "a.silk"}}}}
	forward := map[string]any{"messages": []map[string]any{{"type": "node", "data": map[string]any{
		"content": []map[string]any{{"type": "image", "data": map[string]string{"file": "base64://AA=="}}},
	}}}}
	for _, tc := range []struct {
		name   string
		action string
		params map[string]any
		want   time.Duration
	}{
		{"text", "send_group_msg", text, 30 * time.Second},
		{"image", "send_group_msg", image, 90 * time.Second},
		{"record", "send_private_msg", record, 90 * time.Second},
		{"forward with image", "send_group_forward_msg", forward, 90 * time.Second},
		{"upload", "upload_group_file", map[string]any{"file": "/tmp/a.zip"}, 90 * time.Second},
		{"query", "get_group_msg_history", image, 30 * time.Second},
	} {
		if got := oneBotCallTimeout(tc.action, tc.params, oneBotTextActionTimeout); got != tc.want {
			t.Errorf("%s: timeout = %s, want %s", tc.name, got, tc.want)
		}
	}
}

// 请求写出去后才超时是「结果不明」；没连上是确定失败。
func TestReverseCallAPIClassifiesTimeoutAfterWriteAsAmbiguous(t *testing.T) {
	original := oneBotReverseCallTimeout
	oneBotReverseCallTimeout = 20 * time.Millisecond
	t.Cleanup(func() { oneBotReverseCallTimeout = original })

	notConnected := NewOneBotReverseServer(OneBotConfig{Endpoint: "ws://127.0.0.1:18080/onebot/v11/ws"})
	if _, err := notConnected.CallAPI(context.Background(), "send_group_msg", nil); err == nil || errors.Is(err, ErrOutboundOutcomeUnknown) {
		t.Fatalf("not connected error = %v, want definite failure", err)
	}

	release := make(chan struct{})
	upgrader := websocket.Upgrader{}
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		go func() {
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}()
		<-release
	}))
	defer bridge.Close()
	defer close(release)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(bridge.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	server := NewOneBotReverseServer(OneBotConfig{Endpoint: "ws://127.0.0.1:18080/onebot/v11/ws"})
	server.connMu.Lock()
	server.conn = conn
	server.connMu.Unlock()

	_, err = server.CallAPI(context.Background(), "send_group_msg", map[string]any{
		"group_id": 20005, "message": buildOutgoingSegments(OutgoingMessage{Text: "hi"}),
	})
	if !errors.Is(err, ErrOutboundOutcomeUnknown) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout after write error = %v, want outcome unknown wrapping deadline", err)
	}
}

func TestOutboundFingerprintAcceptsSplitStandaloneMedia(t *testing.T) {
	fingerprint := outboundFingerprintFromSegments(buildOutgoingSegments(OutgoingMessage{
		Text:      "视频来了",
		VideoURLs: []string{"http://127.0.0.1:18080/media/video.mp4"},
	}))
	target := groupOutcomeEvent()
	// 接入端把视频拆成单独一条发出：任何一部分出现都说明已经执行过。
	videoOnly := newObservedOutboundMessage(MessageEvent{
		Kind: EventKindGroup, GroupID: outcomeTestGroupID, MessageID: "54350",
		Segments: []MessageSegment{{Type: "video", Data: map[string]string{"file": "abc.mp4"}}},
	}, time.Now())
	if !videoOnly.matches(target, fingerprint, time.Now()) {
		t.Fatal("split video piece did not match")
	}
	otherGroup := videoOnly
	otherGroup.event.GroupID = "20006"
	if otherGroup.matches(target, fingerprint, time.Now()) {
		t.Fatal("echo from another group matched")
	}
	unrelated := newObservedOutboundMessage(MessageEvent{
		Kind: EventKindGroup, GroupID: outcomeTestGroupID, MessageID: "54351",
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "视频"}}},
	}, time.Now())
	if unrelated.matches(target, fingerprint, time.Now()) {
		t.Fatal("partial text matched")
	}
}

func TestOutboundFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err       error
		permanent bool
		ambiguous bool
	}{
		{"napcat invalid params retcode", &oneBotActionError{retCode: 1400, message: "参数错误"}, true, false},
		{"numeric segment field", &oneBotActionError{retCode: 200, message: `numeric message segment field must contain only an integer, received "im_current_user_1"`}, true, false},
		{"video must be alone", errors.New(`message element "video" must be the only segment in a message`), true, false},
		{"local invalid group id", fmt.Errorf("diana: send failed after 1 attempts: %w", errors.New(`diana: invalid group id "abc"`)), true, false},
		{"qq risk control", &oneBotActionError{retCode: 200, message: "发送消息失败 result=120"}, false, false},
		{"generic retcode", &oneBotActionError{retCode: 200, message: "onebot api failed: status=failed retcode=200"}, false, false},
		{"not connected", newChannelNotConnectedError("diana: onebot reverse websocket is not connected"), false, false},
		{"timeout after write", ambiguousSendError("send_group_msg"), false, true},
		{"plain deadline", context.DeadlineExceeded, false, false},
	} {
		if got := isPermanentOutboundRejection(tc.err); got != tc.permanent {
			t.Errorf("%s: permanent = %v, want %v", tc.name, got, tc.permanent)
		}
		if got := errors.Is(tc.err, ErrOutboundOutcomeUnknown); got != tc.ambiguous {
			t.Errorf("%s: ambiguous = %v, want %v", tc.name, got, tc.ambiguous)
		}
	}
}

// 消息本身无效的失败以前要退避四次、十来分钟才丢；现在第一次就放下，记一笔，
// 并让入站队列按「发送被拒」落终态。
func TestPermanentRejectionIsDroppedWithoutBackoff(t *testing.T) {
	logs := captureStdLog(t)
	channel := &ambiguousOutboundChannel{outcomes: []error{
		&oneBotActionError{retCode: 200, message: `message element "video" must be the only segment in a message`},
		nil,
	}}
	runtime := newAmbiguousOutcomeRuntime(t, channel)
	ctx := withOutboundDeliveryPolicy(context.Background(), recoveringOutboundDeliveryPolicy())

	_, err := runtime.sendOutgoingWithResult(ctx, groupOutcomeEvent(), OutgoingMessage{Text: "视频来了"})
	if !errors.Is(err, errOutboundPermanentRejection) || !isPermanentSendRejection(err) {
		t.Fatalf("error = %v, want permanent send rejection", err)
	}
	if got := channel.sentCount(); got != 1 {
		t.Fatalf("send attempts = %d, want 1", got)
	}
	if !strings.Contains(logs.String(), "消息被判定无效，不再重试") {
		t.Fatalf("stdout log missing permanent rejection:\n%s", logs.String())
	}
	// 同群后面的消息不受影响，没有进冷却。
	if _, err := runtime.sendOutgoingWithResult(ctx, groupOutcomeEvent(), OutgoingMessage{Text: "下一条"}); err != nil {
		t.Fatalf("next send error = %v", err)
	}
}

// QQ 侧的拒收（result=120 之类，可能是禁言或风控）仍然按原来的退避重试。
func TestTransientQQRejectionKeepsBackoff(t *testing.T) {
	channel := &ambiguousOutboundChannel{outcomes: []error{&oneBotActionError{retCode: 200, message: "发送消息失败 result=120"}, nil}}
	runtime := newAmbiguousOutcomeRuntime(t, channel)
	ctx := withOutboundDeliveryPolicy(context.Background(), recoveringOutboundDeliveryPolicy())

	if _, err := runtime.sendOutgoingWithResult(ctx, groupOutcomeEvent(), OutgoingMessage{Text: "稍后重试"}); err != nil {
		t.Fatalf("sendOutgoingWithResult() error = %v", err)
	}
	if got := channel.sentCount(); got != 2 {
		t.Fatalf("send attempts = %d, want backoff retry", got)
	}
}

// 私聊不走群退避门：以前会在 sendChannelPayloadWithRetry 里原地重试无效消息。
func TestPermanentRejectionPrivateIsNotRetriedInline(t *testing.T) {
	channel := &ambiguousOutboundChannel{outcomes: []error{
		&oneBotActionError{retCode: oneBotInvalidParamsRetCode, message: "参数错误"}, nil, nil,
	}}
	runtime := NewRuntime(BotConfig{BotAccount: outcomeTestSelfID, SendRetryAttempts: 3}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindPrivate, UserID: outcomeTestUserID, MessageID: "12347"}

	_, err := runtime.sendOutgoingWithResult(context.Background(), event, OutgoingMessage{Text: "私聊"})
	if !isPermanentSendRejection(err) {
		t.Fatalf("error = %v, want permanent send rejection", err)
	}
	if got := channel.sentCount(); got != 1 {
		t.Fatalf("send attempts = %d, want 1", got)
	}
}

// message_sent 帧三条连接都要收下来，但它是机器人自己发的：只记回推，不回复、
// 不再记一遍聊天记录。
func TestMessageSentFrameIsObservedButNotHandledAsInbound(t *testing.T) {
	channel := &ambiguousOutboundChannel{}
	runtime := newAmbiguousOutcomeRuntime(t, channel)

	// 反向 WebSocket。
	feedFrame(t, newEchoFeed(runtime), napCatMessageSentFrame(54500, "group", outcomeTestGroupID, "帮助", ""))
	waitForObservedEchoes(runtime, 1)

	// 正向 WebSocket。
	forward := NewOneBotChannel(OneBotConfig{Endpoint: "ws://127.0.0.1:3001"})
	if err := forward.handleFrame(context.Background(), runtime.HandleEvent, napCatMessageSentFrame(54501, "group", outcomeTestGroupID, "帮助", "")); err != nil {
		t.Fatal(err)
	}
	waitForObservedEchoes(runtime, 2)

	// HTTP 上报。私聊的对方在 target_id 里。
	httpChannel := NewOneBotHTTPChannel(OneBotConfig{Endpoint: "http://127.0.0.1:3000", HTTPSecret: "event-secret"})
	httpChannel.mu.Lock()
	httpChannel.handler = runtime.HandleEvent
	httpChannel.ctx = context.Background()
	httpChannel.mu.Unlock()
	body := napCatMessageSentFrame(54502, "private", outcomeTestUserID, "帮助", "")
	mac := hmac.New(sha1.New, []byte("event-secret"))
	_, _ = mac.Write(body)
	req := httptest.NewRequest(http.MethodPost, "/onebot/v11/http", bytes.NewReader(body))
	req.Header.Set("X-Signature", "sha1="+hex.EncodeToString(mac.Sum(nil)))
	recorder := httptest.NewRecorder()
	httpChannel.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("HTTP event status = %d", recorder.Code)
	}
	waitForObservedEchoes(runtime, 3)

	var private MessageEvent
	runtime.outboundEchoes.mu.Lock()
	for _, echo := range runtime.outboundEchoes.echoes {
		if echo.event.MessageID == "54502" {
			private = echo.event
		}
	}
	runtime.outboundEchoes.mu.Unlock()
	if private.Kind != EventKindPrivate || private.TargetID != outcomeTestUserID {
		t.Fatalf("private echo = %+v", private)
	}
	// 「帮助」是个会触发回复的词；自己发的不能被当成有人来问。
	if got := channel.sentCount(); got != 0 {
		t.Fatalf("self-sent frames triggered %d replies", got)
	}
	runtime.mu.RLock()
	historyRows := len(runtime.history[sessionKey(groupOutcomeEvent())]) + len(runtime.history[sessionKey(private)])
	runtime.mu.RUnlock()
	if historyRows != 0 {
		t.Fatalf("self-sent frames were remembered again: %d rows", historyRows)
	}
}

// 私聊结果不明，message_sent 回推带着 target_id，按它对上会话。
func TestAmbiguousPrivateSendConfirmedByMessageSentEcho(t *testing.T) {
	channel := &ambiguousOutboundChannel{outcomes: []error{ambiguousSendError("send_private_msg")}}
	runtime := newAmbiguousOutcomeRuntime(t, channel)
	runtime.outboundEchoes.echoWait = 5 * time.Second
	feed := newEchoFeed(runtime)
	channel.onSend = func(attempt int) {
		if attempt != 1 {
			return
		}
		// 发给另一个人的同样一句不算。
		feedFrame(t, feed, napCatMessageSentFrame(54510, "private", "10002", "私聊回复", ""))
		feedFrame(t, feed, napCatMessageSentFrame(54511, "private", outcomeTestUserID, "私聊回复", ""))
		waitForObservedEchoes(runtime, 2)
	}
	event := MessageEvent{Kind: EventKindPrivate, UserID: outcomeTestUserID, SelfID: outcomeTestSelfID, MessageID: "12348"}

	result, err := runtime.sendOutgoingWithResult(context.Background(), event, OutgoingMessage{Text: "私聊回复"})
	if err != nil {
		t.Fatalf("sendOutgoingWithResult() error = %v", err)
	}
	if got := channel.sentCount(); got != 1 || apiMessageID(result) != "54511" {
		t.Fatalf("attempts=%d id=%q", got, apiMessageID(result))
	}
}

// 没有 target_id 的私聊回推分不出发给了谁：只凭正文不认，还得同一个机器人账号、
// 就在请求在途的那段时间里发的。
func TestPrivateEchoWithoutTargetNeedsSameAccountAndTime(t *testing.T) {
	since := time.Now()
	fingerprint := outboundFingerprintFromSegments(buildOutgoingSegments(OutgoingMessage{Text: "私聊回复"}))
	target := MessageEvent{Kind: EventKindPrivate, UserID: outcomeTestUserID, SelfID: outcomeTestSelfID}
	echo := func(selfID string, sent time.Time) observedOutboundMessage {
		return newObservedOutboundMessage(MessageEvent{
			Kind: EventKindPrivate, SelfID: selfID, UserID: selfID, MessageID: "54520", Time: sent.Unix(),
			Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "私聊回复"}}},
		}, sent)
	}
	if !echo(outcomeTestSelfID, since.Add(40*time.Second)).matches(target, fingerprint, since) {
		t.Fatal("same account within the in-flight window did not match")
	}
	if echo("10009", since.Add(40*time.Second)).matches(target, fingerprint, since) {
		t.Fatal("another bot account matched")
	}
	if echo(outcomeTestSelfID, since.Add(-10*time.Minute)).matches(target, fingerprint, since) {
		t.Fatal("an old message matched on text alone")
	}
	unknownSelf := target
	unknownSelf.SelfID = ""
	if echo(outcomeTestSelfID, since).matches(unknownSelf, fingerprint, since) {
		t.Fatal("matched without knowing which account sent the request")
	}
}

// 接入端自己等 QQ 超时、正向连接断线，都可能已经发出去了：按结果不明处理。
// 带 result= 的是 QQ 给的拒收码，仍是确定失败。
func TestBridgeSideTimeoutsAndDisconnectsAreAmbiguous(t *testing.T) {
	for _, tc := range []struct {
		name      string
		action    string
		err       error
		ambiguous bool
	}{
		{"napcat ntevent timeout", "send_group_msg", &oneBotActionError{retCode: 200, message: "Timeout: NTEvent serviceAndMethod:NodeIKernelMsgService/sendMsg ListenerName:NodeIKernelMsgListener/onMsgInfoListUpdate EventRet: {}"}, true},
		{"napcat bare send failure", "send_group_msg", &oneBotActionError{retCode: 200, message: "发送消息失败"}, true},
		{"qq rejection code", "send_group_msg", &oneBotActionError{retCode: 200, message: "发送消息失败 result=120 errMsg="}, false},
		{"timeout on a query", "get_group_msg_history", &oneBotActionError{retCode: 200, message: "Timeout: NTEvent getMsgHistory"}, false},
		{"disconnect after write", "get_status", errOneBotDisconnectedAwaitingResponse, true},
	} {
		resultCh := make(chan callResult, 1)
		resultCh <- callResult{err: tc.err}
		_, err := oneBotAwaitResponse(context.Background(), tc.action, resultCh)
		if got := errors.Is(err, ErrOutboundOutcomeUnknown); got != tc.ambiguous {
			t.Errorf("%s: ambiguous = %v, want %v (err=%v)", tc.name, got, tc.ambiguous, err)
		}
		if isPermanentOutboundRejection(err) {
			t.Errorf("%s: classified as permanent", tc.name)
		}
	}
}

// NapCat 的 HTTP 服务端参数校验不过回 HTTP 400；正文说是参数问题的算永久失败。
func TestHTTPBadRequestWithInvalidParamsIsPermanent(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		switch r.URL.Path {
		case "/send_group_msg":
			_, _ = w.Write([]byte(`{"status":"failed","retcode":400,"message":"参数错误: group_id 必须是数字"}`))
		default:
			_, _ = w.Write([]byte(`{"status":"failed","retcode":400,"message":"bot busy"}`))
		}
	}))
	defer api.Close()
	channel := NewOneBotHTTPChannel(OneBotConfig{Endpoint: api.URL})

	_, err := channel.CallAPI(context.Background(), "send_group_msg", map[string]any{"group_id": "x"})
	if !isPermanentOutboundRejection(err) {
		t.Fatalf("invalid params error = %v, want permanent", err)
	}
	_, err = channel.CallAPI(context.Background(), "send_private_msg", map[string]any{"user_id": 1})
	if err == nil || isPermanentOutboundRejection(err) {
		t.Fatalf("other 400 error = %v, want retryable", err)
	}
}

// 确认加一次重发最坏要好几分钟，入站租约会按这个上限往后推，免得被别的 worker
// 领走重新生成一遍。
func TestAmbiguousConfirmationExtendsInboundLease(t *testing.T) {
	channel := &ambiguousOutboundChannel{
		outcomes: []error{ambiguousSendError("send_group_msg")},
		history:  []map[string]any{selfHistoryItem(54530, time.Now(), "续租")},
	}
	runtime := newAmbiguousOutcomeRuntime(t, channel)
	var extendedTo []time.Time
	ctx := context.WithValue(context.Background(), inboundLeaseExtensionContextKey{}, func(until time.Time) error {
		extendedTo = append(extendedTo, until)
		return nil
	})

	if _, err := runtime.sendOutgoingWithResult(ctx, groupOutcomeEvent(), OutgoingMessage{Text: "续租"}); err != nil {
		t.Fatalf("sendOutgoingWithResult() error = %v", err)
	}
	// 一次在发送开始时，一次在进入确认时。
	if len(extendedTo) != 2 || time.Until(extendedTo[1]) < oneBotMediaActionTimeout {
		t.Fatalf("lease extensions = %v", extendedTo)
	}
}

type selfEchoAuditStore struct {
	*memoryInboundEventStore
	mu     sync.Mutex
	echoes []string
}

func (s *selfEchoAuditStore) RecordInboundEventDelivery(context.Context, MessageEvent, OutboundDeliveryStage, string, string) error {
	return nil
}

func (s *selfEchoAuditStore) RecordInboundEventSelfEcho(_ context.Context, echo MessageEvent, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.echoes = append(s.echoes, echo.MessageID)
	return nil
}

// 回推比发送回执先到时，那一刻还没有 outbound_message_id 可关联；回执记下后补一次，
// self_echo_at 才不会一直空着。
func TestSelfEchoBeforeAckIsLinkedAfterAck(t *testing.T) {
	channel := &ambiguousOutboundChannel{}
	runtime := newAmbiguousOutcomeRuntime(t, channel)
	store := &selfEchoAuditStore{memoryInboundEventStore: newMemoryInboundEventStore()}
	runtime.SetInboundEventStore(store)
	feed := newEchoFeed(runtime)
	channel.onSend = func(int) {
		feedFrame(t, feed, napCatMessageSentFrame(60001, "group", outcomeTestGroupID, "先到的回推", ""))
		waitForObservedEchoes(runtime, 1)
	}

	if _, err := runtime.sendOutgoingWithResult(context.Background(), groupOutcomeEvent(), OutgoingMessage{Text: "先到的回推"}); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.echoes) != 2 || store.echoes[1] != "60001" {
		t.Fatalf("self echo links = %v, want one on arrival and one after the ack", store.echoes)
	}
}

// 结果确认不了时通道恰好也掉线了：以前按「离线」交回队列，恢复后重新生成再发一遍，
// 正是这次要防的重复。现在无论在不在线都直接落终态。
func TestUnconfirmedOutcomeIsTerminalEvenWhenChannelGoesOffline(t *testing.T) {
	channel := &ambiguousOutboundChannel{
		outcomes:   []error{ambiguousSendError("send_group_msg")},
		historyErr: errors.New("get_group_msg_history timeout"),
	}
	channel.onSend = func(int) { channel.offline.Store(true) }
	runtime := newAmbiguousOutcomeRuntime(t, channel)
	event := MessageEvent{Kind: EventKindGroup, GroupID: outcomeTestGroupID, UserID: outcomeTestUserID, MessageID: "12349", Time: time.Now().Unix()}

	outcome, err := runtime.replyAndRecord(context.Background(), event, "帮助", "replied")
	if err != nil || outcome != inboundOutcomeDroppedOutboundUnconfirmed {
		t.Fatalf("outcome=%q err=%v, want terminal %q", outcome, err, inboundOutcomeDroppedOutboundUnconfirmed)
	}
	if got := channel.sentCount(); got != 1 {
		t.Fatalf("send attempts = %d, want 1", got)
	}
}

// 接入端说清了发不出去的原因（不是好友），或者卡在发消息之前的富媒体上传，
// 都是确定没发，不去确认。
func TestExplicitPreSendFailuresAreNotAmbiguous(t *testing.T) {
	for _, message := range []string{
		"发送消息失败：请先添加对方为好友",
		"Timeout: NTEvent serviceAndMethod:NodeIKernelRichMediaService/uploadRMFileWithoutMsg ListenerName: EventRet: {}",
	} {
		resultCh := make(chan callResult, 1)
		resultCh <- callResult{err: &oneBotActionError{retCode: 200, message: message}}
		if _, err := oneBotAwaitResponse(context.Background(), "send_private_msg", resultCh); errors.Is(err, ErrOutboundOutcomeUnknown) {
			t.Errorf("%q classified as ambiguous", message)
		}
	}
}

// 这个账号最近连续发了好几条都没有回推（接入端没开自身消息上报），结果不明时
// 不再干等两分钟，直接查历史。
func TestSilentEchoAccountSkipsEchoWait(t *testing.T) {
	channel := &ambiguousOutboundChannel{}
	runtime := newAmbiguousOutcomeRuntime(t, channel)
	runtime.outboundEchoes.echoWait = time.Minute
	event := groupOutcomeEvent()
	event.SelfID = outcomeTestSelfID
	for index := 0; index < outboundEchoSilentAfter; index++ {
		if _, err := runtime.sendOutgoingWithResult(context.Background(), event, OutgoingMessage{Text: fmt.Sprintf("第 %d 条", index)}); err != nil {
			t.Fatal(err)
		}
	}
	channel.mu.Lock()
	channel.outcomes = []error{ambiguousSendError("send_group_msg")}
	channel.history = []map[string]any{selfHistoryItem(54600, time.Now(), "直接查历史")}
	channel.mu.Unlock()

	started := time.Now()
	result, err := runtime.sendOutgoingWithResult(context.Background(), event, OutgoingMessage{Text: "直接查历史"})
	if err != nil {
		t.Fatalf("sendOutgoingWithResult() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("waited %s for an echo that never comes", elapsed)
	}
	if apiMessageID(result) != "54600" {
		t.Fatalf("outbound message id = %q", apiMessageID(result))
	}

	// 回推一来就恢复等待。
	runtime.observeOutboundEcho(MessageEvent{Kind: EventKindGroup, GroupID: outcomeTestGroupID, SelfID: outcomeTestSelfID, MessageID: "54601"})
	if runtime.outboundEchoes.echoesSilent(event) {
		t.Fatal("echo did not reset the silent counter")
	}
}

// 认不出账号的发送（提醒之类，没有 profile 也没有 self_id）不累积「没有回推」的
// 计数，免得一个空账号一直涨下去，把谁都判成不上报。
func TestAccountlessSendsDoNotMarkEchoesSilent(t *testing.T) {
	var tracker outboundEchoTracker
	accountless := groupOutcomeEvent()
	for index := 0; index < 3*outboundEchoSilentAfter; index++ {
		tracker.claim(accountless, strconv.Itoa(60000+index))
	}
	if tracker.echoesSilent(accountless) || len(tracker.sendsSinceEcho) != 0 {
		t.Fatalf("account-less sends accumulated: %v", tracker.sendsSinceEcho)
	}
}

// HTTP 已经回了 2xx，正文却读不全或解析不了：接入端已经处理了这个 action，只是
// 不知道结果，不能当成没发。
func TestHTTPSuccessStatusWithUnreadableBodyIsAmbiguous(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	defer api.Close()
	channel := NewOneBotHTTPChannel(OneBotConfig{Endpoint: api.URL})
	if _, err := channel.CallAPI(context.Background(), "send_group_msg", map[string]any{"group_id": 20005}); !errors.Is(err, ErrOutboundOutcomeUnknown) {
		t.Fatalf("error = %v, want outcome unknown", err)
	}
}

type recordingLeaseStore struct {
	*memoryInboundEventStore
	mu      sync.Mutex
	extends []time.Time
}

func (s *recordingLeaseStore) ExtendInboundLease(_ context.Context, _ string, _ string, until time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.extends = append(s.extends, until)
	return nil
}

// 发送开始时续租，但租约还够用就不写库；同一轮里连续几条分片只写一次。
func TestInboundLeaseExtensionOnlyWritesWhenNeeded(t *testing.T) {
	store := &recordingLeaseStore{memoryInboundEventStore: newMemoryInboundEventStore()}
	ctx := withInboundLeaseExtension(context.Background(), store, "event-1", "worker-1", time.Now().Add(10*time.Minute))

	extendInboundLease(ctx, 3*time.Minute)
	if len(store.extends) != 0 {
		t.Fatalf("lease still had 10 minutes but was extended: %v", store.extends)
	}
	extendInboundLease(ctx, 15*time.Minute)
	extendInboundLease(ctx, 15*time.Minute+30*time.Second)
	if len(store.extends) != 1 || time.Until(store.extends[0]) < 15*time.Minute {
		t.Fatalf("extensions = %v, want exactly one past 15 minutes", store.extends)
	}
}
