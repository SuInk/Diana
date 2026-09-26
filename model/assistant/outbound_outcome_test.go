// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"crypto/md5"
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
	return ChannelStatus{Connected: true, SelfID: outcomeTestSelfID}
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

// napCatGroupEcho 是 NapCat 回推机器人自己群消息的原样结构：引用段在前，图片的
// file 是内容 MD5 加扩展名，url 是 QQ 的媒体地址。
func napCatGroupEcho(t *testing.T, messageID int, text, imageMD5 string) MessageEvent {
	t.Helper()
	payload := fmt.Sprintf(`{
		"self_id": %[1]s, "user_id": %[1]s, "time": %[2]d, "message_id": %[3]d, "message_seq": %[3]d, "real_id": %[3]d,
		"message_type": "group", "sub_type": "normal", "post_type": "message", "group_id": %[4]s, "font": 14,
		"sender": {"user_id": %[1]s, "nickname": "Diana", "card": "", "role": "member"},
		"raw_message": "[CQ:reply,id=12345]%[5]s[CQ:image,file=%[6]s.png]",
		"message_format": "array",
		"message": [
			{"type": "reply", "data": {"id": "12345"}},
			{"type": "text", "data": {"text": %[7]q}},
			{"type": "image", "data": {"summary": "", "file": "%[6]s.png", "sub_type": 0,
				"url": "https://multimedia.nt.qq.com.cn/download?appid=1407&fileid=outcome", "file_size": "18"}}
		]
	}`, outcomeTestSelfID, time.Now().Unix(), messageID, outcomeTestGroupID, text, imageMD5, text)
	var envelope oneBotEnvelope
	if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
		t.Fatal(err)
	}
	return messageEventFromEnvelope(envelope)
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
	// 回推在超时之前就进来了：先一条同样正文、别的图的（另一次发送），再是真正那条。
	channel.onSend = func(attempt int) {
		if attempt != 1 {
			return
		}
		runtime.observeOutboundEcho(napCatGroupEcho(t, 54320, "图片生成完成。", strings.Repeat("A", 32)))
		runtime.observeOutboundEcho(napCatGroupEcho(t, 54321, "图片生成完成。", imageMD5))
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

// 回推晚于开始等待才到，也要被叫醒认领；走的是完整的入站路径。
func TestAmbiguousSendWaitsForLateEchoThroughHandleEvent(t *testing.T) {
	channel := &ambiguousOutboundChannel{outcomes: []error{ambiguousSendError("send_group_msg")}}
	runtime := newAmbiguousOutcomeRuntime(t, channel)
	runtime.outboundEchoes.echoWait = 5 * time.Second
	channel.onSend = func(attempt int) {
		if attempt != 1 {
			return
		}
		go func() {
			time.Sleep(3 * time.Millisecond)
			echo := MessageEvent{
				Kind: EventKindGroup, GroupID: outcomeTestGroupID, UserID: outcomeTestSelfID, SelfID: outcomeTestSelfID,
				MessageID: "54330", Time: time.Now().Unix(),
				Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "好的，马上 就来"}}},
			}
			_ = runtime.HandleEvent(context.Background(), echo)
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
				channel.onDeliver = func(messageID int, msg OutgoingMessage) {
					go func() {
						// 回推在确认开始等待之后才到，按接入端时间已经过去 45 秒。
						waitUntilEchoAwaited(runtime)
						offset.Store(int64(45 * time.Second))
						runtime.observeOutboundEcho(MessageEvent{
							Kind: EventKindGroup, GroupID: outcomeTestGroupID, UserID: outcomeTestSelfID, SelfID: outcomeTestSelfID,
							MessageID: strconv.Itoa(messageID),
							Segments:  []MessageSegment{{Type: "text", Data: map[string]string{"text": msg.Text}}},
						})
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
	if !videoOnly.matches(target, fingerprint) {
		t.Fatal("split video piece did not match")
	}
	otherGroup := videoOnly
	otherGroup.event.GroupID = "20006"
	if otherGroup.matches(target, fingerprint) {
		t.Fatal("echo from another group matched")
	}
	unrelated := newObservedOutboundMessage(MessageEvent{
		Kind: EventKindGroup, GroupID: outcomeTestGroupID, MessageID: "54351",
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "视频"}}},
	}, time.Now())
	if unrelated.matches(target, fingerprint) {
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
