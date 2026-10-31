package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type qqStreamFakeAPI struct {
	mu       sync.Mutex
	frames   []map[string]any
	messages []map[string]any
	deletes  []string
	// rateLimit 让前 N 帧回 429。
	rateLimit int
	// reject 让所有帧被拒收，模拟账号没开流式能力。
	reject bool
	server *httptest.Server
}

func newQQStreamFakeAPI(t *testing.T) *qqStreamFakeAPI {
	t.Helper()
	api := &qqStreamFakeAPI{}
	api.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		api.mu.Lock()
		defer api.mu.Unlock()
		switch {
		case r.Method == http.MethodDelete:
			api.deletes = append(api.deletes, r.URL.Path)
		case strings.HasSuffix(r.URL.Path, "/stream_messages"):
			api.frames = append(api.frames, body)
			if api.reject {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":304099,"message":"no permission"}`))
				return
			}
			if api.rateLimit > 0 {
				api.rateLimit--
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"code":50002,"message":"rate limit"}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"s1","ext_info":{"ref_idx":"REFIDX_s1"}}`))
		case strings.HasSuffix(r.URL.Path, "/messages"):
			api.messages = append(api.messages, body)
			_, _ = w.Write([]byte(`{"id":"m1"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.server.Close)
	return api
}

func (a *qqStreamFakeAPI) channel() *QQOfficialChannel {
	channel := NewQQOfficialChannel(QQOfficialConfig{AppID: "app", AppSecret: "secret"})
	channel.apiBaseOverride = a.server.URL
	channel.tokens = &platformTokenCache{fetch: func(context.Context) (string, time.Duration, error) {
		return "token", time.Hour, nil
	}}
	return channel
}

func TestQQOfficialStreamFramesThenFinalReplacesPreview(t *testing.T) {
	api := newQQStreamFakeAPI(t)
	channel := api.channel()
	ctx := context.Background()
	draft := OutgoingMessage{UserID: "U2345", PassiveReplyMessageID: "in1"}
	for _, text := range []string{"你好", "你好呀，今天"} {
		draft.Text = text
		if err := channel.SendTextDraft(ctx, draft, 1); err != nil {
			t.Fatal(err)
		}
	}
	result, err := channel.SendWithResult(ctx, OutgoingMessage{UserID: "U2345", PassiveReplyMessageID: "in1", Text: "你好呀，今天过得怎么样"})
	if err != nil {
		t.Fatal(err)
	}
	if len(api.messages) != 0 {
		t.Fatalf("final reply was also posted as a normal message: %#v", api.messages)
	}
	if result["message_id"] != "s1" || len(api.frames) != 3 {
		t.Fatalf("result = %#v, frames = %#v", result, api.frames)
	}
	for i, frame := range api.frames {
		if frame["index"] != float64(i) || frame["msg_seq"] != float64(1) || frame["msg_id"] != "in1" || frame["event_id"] != "in1" || frame["input_mode"] != "replace" {
			t.Fatalf("frame %d = %#v", i, frame)
		}
		if _, ok := frame["stream_msg_id"]; ok != (i > 0) {
			t.Fatalf("frame %d stream_msg_id presence wrong: %#v", i, frame)
		}
	}
	last := api.frames[2]
	if last["input_state"] != float64(qqStreamStateDone) || last["content_raw"] != "你好呀，今天过得怎么样" {
		t.Fatalf("done frame = %#v", last)
	}

	// 流收尾之后，同一个 msg_id 下的下一条走普通发送，msg_seq 接着往后排。
	if _, err := channel.SendWithResult(ctx, OutgoingMessage{UserID: "U2345", PassiveReplyMessageID: "in1", Text: "第二段"}); err != nil {
		t.Fatal(err)
	}
	if len(api.messages) != 1 || api.messages[0]["msg_seq"] != float64(2) {
		t.Fatalf("follow-up = %#v", api.messages)
	}
}

func TestQQOfficialStreamRejectsGroups(t *testing.T) {
	api := newQQStreamFakeAPI(t)
	err := api.channel().SendTextDraft(context.Background(), OutgoingMessage{GroupID: "G2345", UserID: "U2345", PassiveReplyMessageID: "in1", Text: "草稿"}, 1)
	if err == nil || len(api.frames) != 0 {
		t.Fatalf("err = %v, frames = %d", err, len(api.frames))
	}
}

func TestQQOfficialStreamFirstFrameRejectedFallsBackToMessage(t *testing.T) {
	api := newQQStreamFakeAPI(t)
	api.reject = true
	channel := api.channel()
	ctx := context.Background()
	if err := channel.SendTextDraft(ctx, OutgoingMessage{UserID: "U2345", PassiveReplyMessageID: "in1", Text: "草稿"}, 1); err == nil {
		t.Fatal("rejected frame should report an error")
	}
	if _, err := channel.SendWithResult(ctx, OutgoingMessage{UserID: "U2345", PassiveReplyMessageID: "in1", Text: "定稿"}); err != nil {
		t.Fatal(err)
	}
	if len(api.messages) != 1 || len(api.frames) != 1 {
		t.Fatalf("messages = %#v, frames = %#v", api.messages, api.frames)
	}
}

func TestQQOfficialStreamRetriesRateLimitWithFreshIndex(t *testing.T) {
	previous := qqStreamRetryBase
	qqStreamRetryBase = time.Millisecond
	t.Cleanup(func() { qqStreamRetryBase = previous })
	api := newQQStreamFakeAPI(t)
	api.rateLimit = 2
	if err := api.channel().SendTextDraft(context.Background(), OutgoingMessage{UserID: "U2345", PassiveReplyMessageID: "in1", Text: "草稿"}, 1); err != nil {
		t.Fatal(err)
	}
	if len(api.frames) != 3 {
		t.Fatalf("frames = %#v", api.frames)
	}
	for i, frame := range api.frames {
		if frame["index"] != float64(i) {
			t.Fatalf("retry %d reused index: %#v", i, frame)
		}
	}
}

func TestQQReplyDraftOnlyForUnauditedPrivateChats(t *testing.T) {
	runtime := &Runtime{channel: newQQStreamFakeAPI(t).channel()}
	event := MessageEvent{Platform: PlatformQQOfficial, Kind: EventKindPrivate, UserID: "U2345", MessageID: "in1"}
	cfg := BotConfig{ReplySafetyPrivateEnabled: boolPointer(false), BotReplyLoopDetectionEnabled: boolPointer(false)}
	if runtime.telegramReplyDraft(event, cfg) == nil {
		t.Fatal("private QQ chat without audit should stream")
	}
	audited := cfg
	audited.ReplySafetyPrivateEnabled = boolPointer(true)
	if runtime.telegramReplyDraft(event, audited) != nil {
		t.Fatal("audited private reply must not stream before the audit")
	}
	group := event
	group.Kind = EventKindGroup
	group.GroupID = "G2345"
	if runtime.telegramReplyDraft(group, cfg) != nil {
		t.Fatal("QQ groups have no stream API")
	}
	off := cfg
	off.LLMStreamingEnabled = boolPointer(false)
	if runtime.telegramReplyDraft(event, off) != nil {
		t.Fatal("streaming switch off must disable QQ stream")
	}
}

func TestQQOfficialStreamAbandonClosesAndRecallsHalfFrame(t *testing.T) {
	api := newQQStreamFakeAPI(t)
	channel := api.channel()
	ctx := context.Background()
	msg := OutgoingMessage{UserID: "U2345", PassiveReplyMessageID: "in1"}
	draft := msg
	draft.Text = "蓝发的那个是星街"
	if err := channel.SendTextDraft(ctx, draft, 1); err != nil {
		t.Fatal(err)
	}
	if err := channel.AbandonTextDraft(ctx, msg); err != nil {
		t.Fatal(err)
	}
	if len(api.frames) != 2 || len(api.messages) != 0 {
		t.Fatalf("frames = %#v, messages = %#v", api.frames, api.messages)
	}
	done := api.frames[1]
	if done["input_state"] != float64(qqStreamStateDone) || done["content_raw"] != "蓝发的那个是星街" || done["stream_msg_id"] != "s1" {
		t.Fatalf("done frame = %#v", done)
	}
	if len(api.deletes) != 1 || api.deletes[0] != "/v2/users/U2345/messages/s1" {
		t.Fatalf("deletes = %#v", api.deletes)
	}
	// 收过一次就不在了，再收是空操作；之后同一条入站消息的回复走普通发送。
	if err := channel.AbandonTextDraft(ctx, msg); err != nil || len(api.frames) != 2 || len(api.deletes) != 1 {
		t.Fatalf("second abandon: err = %v, frames = %d, deletes = %d", err, len(api.frames), len(api.deletes))
	}
}

func TestQQOfficialStreamAbandonLeavesFinishedStreamAlone(t *testing.T) {
	api := newQQStreamFakeAPI(t)
	channel := api.channel()
	ctx := context.Background()
	if err := channel.SendTextDraft(ctx, OutgoingMessage{UserID: "U2345", PassiveReplyMessageID: "in1", Text: "你好呀，今天过得"}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := channel.SendWithResult(ctx, OutgoingMessage{UserID: "U2345", PassiveReplyMessageID: "in1", Text: "你好呀，今天过得怎么样"}); err != nil {
		t.Fatal(err)
	}
	if err := channel.AbandonTextDraft(ctx, OutgoingMessage{UserID: "U2345", PassiveReplyMessageID: "in1"}); err != nil {
		t.Fatal(err)
	}
	if len(api.frames) != 2 || len(api.deletes) != 0 {
		t.Fatalf("frames = %d, deletes = %#v", len(api.frames), api.deletes)
	}
}

func TestQQReplyDraftHoldsShortFirstFrameAndTurnEndRecallsIt(t *testing.T) {
	api := newQQStreamFakeAPI(t)
	runtime := &Runtime{channel: api.channel()}
	event := MessageEvent{Platform: PlatformQQOfficial, Kind: EventKindPrivate, UserID: "U2345", MessageID: "in1"}
	cfg := BotConfig{ReplySafetyPrivateEnabled: boolPointer(false), BotReplyLoopDetectionEnabled: boolPointer(false)}
	draft := runtime.telegramReplyDraft(event, cfg)
	if draft == nil {
		t.Fatal("private QQ chat without audit should stream")
	}
	ctx := context.Background()
	// 刚出一个字就被打断时什么都不发，避免聊天里只剩一个「蓝」。
	if draft.ObserveTextDelta(ctx, "蓝") || len(api.frames) != 0 {
		t.Fatalf("one-rune first frame was sent: %#v", api.frames)
	}
	runtime.abandonReplyStream(ctx, event)
	if len(api.frames) != 0 || len(api.deletes) != 0 {
		t.Fatalf("nothing streamed, nothing to recall: frames = %d, deletes = %#v", len(api.frames), api.deletes)
	}
	if !draft.ObserveTextDelta(ctx, "蓝发的那个是星街彗星") || len(api.frames) != 1 {
		t.Fatalf("long enough first frame should stream: %#v", api.frames)
	}
	// 这一轮随后静默结束：轮次收尾时关流并撤回，取消了的上下文也不影响收尾。
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	runtime.abandonReplyStream(canceled, event)
	if len(api.frames) != 2 || len(api.deletes) != 1 {
		t.Fatalf("frames = %d, deletes = %#v", len(api.frames), api.deletes)
	}
}
