// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package vrchat

import (
	"errors"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/osc"
)

type sentMessage struct {
	osc.Message
	at time.Time
}

type recordingSender struct {
	mu   sync.Mutex
	sent []sentMessage
	fail error
}

func (s *recordingSender) Send(message osc.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	s.sent = append(s.sent, sentMessage{Message: message, at: time.Now()})
	return nil
}

func (s *recordingSender) messages(prefix string) []sentMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []sentMessage
	for _, message := range s.sent {
		if strings.HasPrefix(message.Address, prefix) {
			out = append(out, message)
		}
	}
	return out
}

func (s *recordingSender) waitFor(t *testing.T, prefix string, count int) []sentMessage {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got := s.messages(prefix); len(got) >= count {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d messages on %s, got %d", count, prefix, len(s.messages(prefix)))
	return nil
}

func newTestBridge(t *testing.T, cfg Config) (*Bridge, *recordingSender) {
	t.Helper()
	floor := chatboxIntervalFloor
	chatboxIntervalFloor = 0
	t.Cleanup(func() { chatboxIntervalFloor = floor })
	sender := &recordingSender{}
	bridge := NewBridge()
	bridge.dial = func(string) (Sender, func() error, error) { return sender, func() error { return nil }, nil }
	if cfg.Expressions.index == nil {
		cfg.Expressions, _ = ParseExpressionMap(DefaultExpressionMap)
	}
	if err := bridge.Apply(true, cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bridge.Close)
	return bridge, sender
}

func TestSplitChatboxRespectsLimitsAndBreaks(t *testing.T) {
	if got := SplitChatbox("  你好  "); !reflect.DeepEqual(got, []string{"你好"}) {
		t.Fatalf("short text = %#v", got)
	}
	if got := SplitChatbox(" \n\t "); got != nil {
		t.Fatalf("blank text = %#v", got)
	}

	sentence := strings.Repeat("这是一句测试用的话", 5) + "。" // 46 字
	long := strings.Repeat(sentence, 7)
	segments := SplitChatbox(long)
	if len(segments) < 3 {
		t.Fatalf("expected several segments, got %d", len(segments))
	}
	for i, segment := range segments {
		if n := len([]rune(segment)); n > ChatboxMaxRunes {
			t.Fatalf("segment %d has %d runes", i, n)
		}
		if i < len(segments)-1 && !strings.HasSuffix(segment, "。") {
			t.Fatalf("segment %d should end at a sentence break: %q", i, segment)
		}
	}
	if strings.Join(segments, "") != long {
		t.Fatal("segments must reassemble to the original text")
	}

	// 没有任何标点的长串只能硬切，但也不能超长。
	hard := SplitChatbox(strings.Repeat("a", 300))
	if len(hard) != 3 || len(hard[0]) != ChatboxMaxRunes || len(hard[2]) != 300-2*ChatboxMaxRunes {
		t.Fatalf("hard split lengths wrong: %d segments", len(hard))
	}

	// 行数上限：12 行短句要在第 9 行后断开，空行被压掉，控制字符被去掉。
	lines := make([]string, 12)
	for i := range lines {
		lines[i] = "第" + string(rune('A'+i)) + "行\x07"
	}
	byLines := SplitChatbox(strings.Join(lines, "\n\n\n"))
	if len(byLines) != 2 {
		t.Fatalf("line split = %#v", byLines)
	}
	if strings.Count(byLines[0], "\n")+1 != ChatboxMaxLines {
		t.Fatalf("first segment should hold %d lines: %q", ChatboxMaxLines, byLines[0])
	}
	if strings.ContainsRune(byLines[0], '\x07') || strings.Contains(byLines[0], "\n\n") {
		t.Fatalf("text not normalized: %q", byLines[0])
	}
}

func TestChatboxQueueIsRateLimited(t *testing.T) {
	interval := 60 * time.Millisecond
	bridge, sender := newTestBridge(t, Config{ChatboxInterval: interval, ChatboxSound: true})

	text := strings.Repeat("一二三四五六七八九十", 30) // 300 字，三段
	result, err := bridge.Chatbox(text, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Segments != 3 || result.Dropped != 0 {
		t.Fatalf("result = %+v", result)
	}
	sent := sender.waitFor(t, "/chatbox/input", 3)
	for i, message := range sent {
		if len(message.Args) != 3 || message.Args[1] != true {
			t.Fatalf("message %d args = %#v", i, message.Args)
		}
		// 提示音只在第一段响。
		if want := i == 0; message.Args[2] != want {
			t.Fatalf("message %d sound = %v", i, message.Args[2])
		}
		if i > 0 {
			if gap := message.at.Sub(sent[i-1].at); gap < interval-5*time.Millisecond {
				t.Fatalf("gap between %d and %d is %v, want >= %v", i-1, i, gap, interval)
			}
		}
	}
	if status := bridge.Status(0); status.ChatboxPending != 0 || status.LastChatbox == "" {
		t.Fatalf("status = %+v", status)
	}
}

func TestChatboxQueueCapAndReplace(t *testing.T) {
	bridge, sender := newTestBridge(t, Config{ChatboxInterval: time.Hour})
	// 第一段立即发出，后面的都卡在一小时的间隔上。
	if _, err := bridge.Chatbox("开场", false); err != nil {
		t.Fatal(err)
	}
	sender.waitFor(t, "/chatbox/input", 1)

	huge := strings.Repeat("字", ChatboxMaxRunes*(maxChatboxPending+3))
	result, err := bridge.Chatbox(huge, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Segments != maxChatboxPending || result.Dropped != 3 || result.Pending != maxChatboxPending {
		t.Fatalf("result = %+v", result)
	}
	replaced, err := bridge.Chatbox("改口", true)
	if err != nil {
		t.Fatal(err)
	}
	if replaced.Pending != 1 {
		t.Fatalf("replace should drop queued segments: %+v", replaced)
	}
	if _, err := bridge.Chatbox("   ", false); err == nil {
		t.Fatal("blank chatbox text should fail")
	}
	if err := bridge.ClearChatbox(); err != nil {
		t.Fatal(err)
	}
	if bridge.Status(0).ChatboxPending != 0 {
		t.Fatal("clear should empty the queue")
	}
	if err := bridge.SetTyping(true); err != nil {
		t.Fatal(err)
	}
	typing := sender.messages("/chatbox/typing")
	if len(typing) != 1 || typing[0].Args[0] != true {
		t.Fatalf("typing = %#v", typing)
	}
}

func TestParseExpressionMap(t *testing.T) {
	mapping, problems := ParseExpressionMap(DefaultExpressionMap)
	if len(problems) != 0 {
		t.Fatalf("default template problems: %v", problems)
	}
	for _, name := range []string{"平静", "开心", "HAPPY", "疑惑", "屑", "趴桌", "低落", "slump"} {
		if _, ok := mapping.Lookup(name); !ok {
			t.Fatalf("default template missing %q", name)
		}
	}
	neutral, _ := mapping.Lookup("neutral")
	want := []Param{{Name: "Expression", Value: int32(0)}, {Name: "Slump", Value: false}}
	if !reflect.DeepEqual(neutral.Params, want) {
		t.Fatalf("neutral params = %#v", neutral.Params)
	}

	custom, problems := ParseExpressionMap(`
# 注释
微笑｜smile ＝ Smile：0.5，Blush:true
坏行
越界 = Face:300
浮点越界 = Face:1.5
非法名 = Fa ce:1
斜杠 = a/b:1
空参数 =
微笑 = Other:1
`)
	if len(problems) != 7 {
		t.Fatalf("problems = %v", problems)
	}
	smile, ok := custom.Lookup("SMILE")
	if !ok || smile.Name() != "微笑" {
		t.Fatalf("full-width separators not accepted: %#v", smile)
	}
	if !reflect.DeepEqual(smile.Params, []Param{{Name: "Smile", Value: float32(0.5)}, {Name: "Blush", Value: true}}) {
		t.Fatalf("smile params = %#v", smile.Params)
	}
	if got := custom.Names(); !reflect.DeepEqual(got, []string{"微笑", "微笑"}) {
		// 重名的后一行仍然保留在列表里，但查找沿用前一个定义。
		t.Fatalf("names = %#v", got)
	}
}

func TestSetExpressionSendsParamsAndReverts(t *testing.T) {
	bridge, sender := newTestBridge(t, Config{})
	expression, err := bridge.SetExpression("趴桌", 40*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if expression.Name() != "趴桌" {
		t.Fatalf("expression = %#v", expression)
	}
	first := sender.messages("/avatar/parameters/")
	if len(first) != 1 || first[0].Address != "/avatar/parameters/Slump" || first[0].Args[0] != true {
		t.Fatalf("sent = %#v", first)
	}
	// 到点回到「平静」：Expression=0、Slump=false。
	all := sender.waitFor(t, "/avatar/parameters/", 3)
	if all[1].Address != "/avatar/parameters/Expression" || all[2].Args[0] != false {
		t.Fatalf("revert sent = %#v", all)
	}
	if status := bridge.Status(0); status.Expression != "平静" {
		t.Fatalf("expression after revert = %q", status.Expression)
	}
	if _, err := bridge.SetExpression("不存在", 0); err == nil || !strings.Contains(err.Error(), "开心") {
		t.Fatalf("unknown expression error should list options: %v", err)
	}
}

func TestApplyMoodYieldsToExplicitExpression(t *testing.T) {
	bridge, sender := newTestBridge(t, Config{})
	now := time.Unix(1_800_000_000, 0)
	bridge.mu.Lock()
	bridge.now = func() time.Time { return now }
	bridge.mu.Unlock()

	if !bridge.ApplyMood(ExpressionHappy) {
		t.Fatal("first mood should apply")
	}
	if bridge.ApplyMood(ExpressionHappy) {
		t.Fatal("unchanged mood must not resend")
	}
	if _, err := bridge.SetExpression("屑", 0); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if bridge.ApplyMood(ExpressionLow) {
		t.Fatal("mood must not override a fresh explicit expression")
	}
	now = now.Add(explicitExpressionGuard)
	if !bridge.ApplyMood(ExpressionLow) {
		t.Fatal("mood should apply once the explicit expression is stale")
	}
	if !bridge.ApplyMood(ExpressionNeutral) {
		t.Fatal("mood-driven expressions can be replaced by the next mood right away")
	}
	if bridge.ApplyMood("没写进映射表的心情") {
		t.Fatal("unmapped mood must be a no-op")
	}
	if got := len(sender.messages("/avatar/parameters/")); got != 1+1+1+2 {
		t.Fatalf("sent %d parameter messages", got)
	}
}

func TestInputIsClampedAndAutoReleased(t *testing.T) {
	bridge, sender := newTestBridge(t, Config{InputMaxHold: 50 * time.Millisecond})
	hold, err := bridge.Input("forward", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if hold != 50*time.Millisecond {
		t.Fatalf("hold = %v, want clamp to 50ms", hold)
	}
	if active := bridge.Status(0).ActiveInputs; !reflect.DeepEqual(active, []string{"MoveForward"}) {
		t.Fatalf("active = %#v", active)
	}
	sent := sender.waitFor(t, "/input/MoveForward", 2)
	if sent[0].Args[0] != int32(1) || sent[1].Args[0] != int32(0) {
		t.Fatalf("press/release = %#v", sent)
	}
	if len(bridge.Status(0).ActiveInputs) != 0 {
		t.Fatal("input should be released after hold")
	}

	if _, err := bridge.Input("voice", time.Second); err == nil {
		t.Fatal("unlisted inputs must be rejected")
	}
	if hold, _ := bridge.Input("jump", 5*time.Second); hold != jumpPress {
		t.Fatalf("jump hold = %v", hold)
	}
	if _, err := bridge.Input("turn_left", 40*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.Input("stop", 0); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"/input/Jump", "/input/LookLeft"} {
		got := sender.messages(input)
		if len(got) != 2 || got[1].Args[0] != int32(0) {
			t.Fatalf("%s should be released by stop: %#v", input, got)
		}
	}
	// stop 之后原来的定时器不能再补发一次松开。
	time.Sleep(80 * time.Millisecond)
	if got := sender.messages("/input/LookLeft"); len(got) != 2 {
		t.Fatalf("stale timer fired: %#v", got)
	}
}

func TestDisableReleasesInputsAndRejectsCalls(t *testing.T) {
	bridge, sender := newTestBridge(t, Config{InputMaxHold: time.Minute})
	if _, err := bridge.Input("forward", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := bridge.Apply(false, Config{}); err != nil {
		t.Fatal(err)
	}
	got := sender.messages("/input/MoveForward")
	if len(got) != 2 || got[1].Args[0] != int32(0) {
		t.Fatalf("disable must release held inputs: %#v", got)
	}
	if _, err := bridge.Chatbox("hi", false); !errors.Is(err, ErrDisabled) {
		t.Fatalf("chatbox after disable err = %v", err)
	}
	if _, err := bridge.Input("forward", time.Second); !errors.Is(err, ErrDisabled) {
		t.Fatalf("input after disable err = %v", err)
	}
	if bridge.ApplyMood(ExpressionHappy) {
		t.Fatal("mood after disable must be a no-op")
	}
}

func TestListenerTracksAvatarState(t *testing.T) {
	bridge, _ := newTestBridge(t, Config{ListenAddress: "127.0.0.1:0"})
	status := bridge.Status(0)
	if !status.Listening || status.ListenError != "" {
		t.Fatalf("listener not running: %+v", status)
	}
	client, err := osc.Dial(status.ListenAddress)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	send := func(message osc.Message) {
		t.Helper()
		if err := client.Send(message); err != nil {
			t.Fatal(err)
		}
	}
	send(osc.Message{Address: "/avatar/parameters/Old", Args: []any{int32(1)}})
	send(osc.Message{Address: "/avatar/change", Args: []any{"avtr_diana"}})
	send(osc.Message{Address: "/avatar/parameters/Expression", Args: []any{int32(2)}})
	send(osc.Message{Address: "/avatar/parameters/AFK", Args: []any{true}})
	send(osc.Message{Address: "/avatar/parameters/VelocityX", Args: []any{float32(0.3)}})
	_ = client.SendBundle(osc.Bundle{Timetag: osc.TimetagImmediately, Elements: []osc.Message{
		{Address: "/avatar/parameters/Slump", Args: []any{true}},
	}})

	deadline := time.Now().Add(3 * time.Second)
	for {
		status = bridge.Status(0)
		if len(status.Params) == 2 && status.Builtin["AFK"] == true {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status never caught up: %+v", status)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if status.AvatarID != "avtr_diana" || status.LastPacketAt == nil {
		t.Fatalf("avatar = %q", status.AvatarID)
	}
	names := map[string]any{}
	for _, param := range status.Params {
		names[param.Name] = param.Value
	}
	// 换 Avatar 前的参数作废，高频的内置参数不进摘要。
	if _, ok := names["Old"]; ok {
		t.Fatal("params from the previous avatar must be cleared")
	}
	if names["Expression"] != int32(2) || names["Slump"] != true {
		t.Fatalf("params = %#v", names)
	}
	if limited := bridge.Status(1); len(limited.Params) != 1 {
		t.Fatalf("param limit ignored: %d", len(limited.Params))
	}
}

func TestListenPortConflictKeepsSending(t *testing.T) {
	taken, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	floor := chatboxIntervalFloor
	chatboxIntervalFloor = 0
	defer func() { chatboxIntervalFloor = floor }()

	sender := &recordingSender{}
	bridge := NewBridge()
	bridge.dial = func(string) (Sender, func() error, error) { return sender, nil, nil }
	defer bridge.Close()
	mapping, _ := ParseExpressionMap(DefaultExpressionMap)
	err = bridge.Apply(true, Config{ListenAddress: taken.LocalAddr().String(), Expressions: mapping})
	if err == nil {
		t.Fatal("expected listen error")
	}
	status := bridge.Status(0)
	if status.Listening || status.ListenError == "" || !status.Enabled {
		t.Fatalf("status = %+v", status)
	}
	if _, err := bridge.SetExpression("开心", 0); err != nil {
		t.Fatalf("sending should still work: %v", err)
	}
	if status.SendAddress != "127.0.0.1:9000" {
		t.Fatalf("default send address = %q", status.SendAddress)
	}
}

func TestUDPEndToEnd(t *testing.T) {
	received := make(chan osc.Message, 8)
	vrchat, err := osc.Listen("127.0.0.1:0", func(message osc.Message, _ *net.UDPAddr) { received <- message })
	if err != nil {
		t.Fatal(err)
	}
	defer vrchat.Close()
	floor := chatboxIntervalFloor
	chatboxIntervalFloor = 0
	defer func() { chatboxIntervalFloor = floor }()

	bridge := NewBridge()
	defer bridge.Close()
	mapping, _ := ParseExpressionMap(DefaultExpressionMap)
	if err := bridge.Apply(true, Config{SendAddress: vrchat.LocalAddr().String(), Expressions: mapping}); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.Chatbox("你好 VRChat", false); err != nil {
		t.Fatal(err)
	}
	select {
	case message := <-received:
		if !reflect.DeepEqual(message, osc.Message{Address: "/chatbox/input", Args: []any{"你好 VRChat", true, false}}) {
			t.Fatalf("received %#v", message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("chatbox message never arrived")
	}
}
