// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package voicecall

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder 是假的下行通道，按顺序记下每一帧。二进制帧记成 Type "binary"。
type recorder struct {
	mu     sync.Mutex
	frames []ServerMessage
	notify chan struct{}
}

func newRecorder() *recorder {
	return &recorder{notify: make(chan struct{}, 1024)}
}

func (r *recorder) WriteJSON(msg ServerMessage) error {
	r.mu.Lock()
	r.frames = append(r.frames, msg)
	r.mu.Unlock()
	r.notify <- struct{}{}
	return nil
}

func (r *recorder) WriteBinary(data []byte) error {
	r.mu.Lock()
	r.frames = append(r.frames, ServerMessage{Type: "binary", Text: string(data)})
	r.mu.Unlock()
	r.notify <- struct{}{}
	return nil
}

func (r *recorder) snapshot() []ServerMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ServerMessage(nil), r.frames...)
}

// waitFor 等到出现满足条件的帧。
func (r *recorder) waitFor(t *testing.T, desc string, match func(ServerMessage) bool) ServerMessage {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		for _, frame := range r.snapshot() {
			if match(frame) {
				return frame
			}
		}
		select {
		case <-r.notify:
		case <-deadline:
			t.Fatalf("等不到 %s，已收到：%s", desc, describe(r.snapshot()))
		}
	}
}

func describe(frames []ServerMessage) string {
	parts := make([]string, 0, len(frames))
	for _, frame := range frames {
		label := frame.Type
		switch frame.Type {
		case ServerState:
			label += ":" + frame.State
		case ServerTurnEnd:
			label += fmt.Sprintf(":%d:%s", frame.Turn, frame.Reason)
		case ServerReply, ServerAudioStart, ServerAudioEnd:
			label += fmt.Sprintf(":%d.%d", frame.Turn, frame.Seq)
		case ServerSpeech:
			label += ":" + frame.Event
		case "binary", ServerTranscript:
			label += ":" + frame.Text
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, " ")
}

func isType(kind string) func(ServerMessage) bool {
	return func(msg ServerMessage) bool { return msg.Type == kind }
}

func turnEnd(turn int, reason string) func(ServerMessage) bool {
	return func(msg ServerMessage) bool {
		return msg.Type == ServerTurnEnd && msg.Turn == turn && msg.Reason == reason
	}
}

// fakeBackend 的三个环节都能按需卡住，用来制造「正在合成时被打断」这类时序。
type fakeBackend struct {
	mu          sync.Mutex
	transcribed []string
	replied     []string

	// transcribe 为空时把音频原样当成文字；WAV 去掉头。
	transcribe func(ctx context.Context, audio []byte, format string) (string, error)
	// reply 为空时把 replyText 一次说完。
	reply     func(ctx context.Context, text string, emit func(string)) error
	replyText string
	// synthesize 为空时每句交出一片 "<句子>"。
	synthesize func(ctx context.Context, text string, chunk func(string, []byte) error) error
}

func (b *fakeBackend) Transcribe(ctx context.Context, audio []byte, format string) (string, error) {
	if b.transcribe != nil {
		return b.transcribe(ctx, audio, format)
	}
	if format == FormatWAV {
		audio = audio[44:]
	}
	b.mu.Lock()
	b.transcribed = append(b.transcribed, format)
	b.mu.Unlock()
	return string(audio), nil
}

func (b *fakeBackend) Reply(ctx context.Context, text string, emit func(string)) error {
	b.mu.Lock()
	b.replied = append(b.replied, text)
	b.mu.Unlock()
	if b.reply != nil {
		return b.reply(ctx, text, emit)
	}
	emit(b.replyText)
	return nil
}

func (b *fakeBackend) Synthesize(ctx context.Context, text string, chunk func(string, []byte) error) error {
	if b.synthesize != nil {
		return b.synthesize(ctx, text, chunk)
	}
	return chunk("audio/mpeg", []byte("<"+text+">"))
}

func (b *fakeBackend) repliedTexts() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.replied...)
}

func startSession(t *testing.T, backend Backend, start ClientMessage) (*Session, *recorder) {
	t.Helper()
	out := newRecorder()
	session := NewSession(context.Background(), backend, out, Options{CallID: "c1", BotID: "bot"})
	t.Cleanup(session.Close)
	start.Type = ClientStart
	if err := session.HandleControl(start); err != nil {
		t.Fatalf("start error = %v", err)
	}
	out.waitFor(t, "ready", isType(ServerReady))
	return session, out
}

func TestSessionSpeaksReplySentenceBySentenceWhileReplying(t *testing.T) {
	firstAudio := make(chan struct{})
	backend := &fakeBackend{}
	backend.reply = func(ctx context.Context, text string, emit func(string)) error {
		emit("第一句话已经说完了。第二句话还在")
		// 第二句还没写完，第一句就该已经念出来了：这是「边合成边发」。
		select {
		case <-firstAudio:
		case <-ctx.Done():
			return ctx.Err()
		}
		emit("后面接着说呢！")
		return nil
	}
	var audioOnce sync.Once
	backend.synthesize = func(ctx context.Context, text string, chunk func(string, []byte) error) error {
		if err := chunk("audio/mpeg", []byte("<"+text+">")); err != nil {
			return err
		}
		// 直接从音频写入完成处通知，避免两个 waitFor 消费同一个 recorder.notify。
		audioOnce.Do(func() { close(firstAudio) })
		return nil
	}
	session, out := startSession(t, backend, ClientMessage{Segmentation: SegmentByCmd})
	if err := session.HandleControl(ClientMessage{Type: ClientText, Text: "你好"}); err != nil {
		t.Fatal(err)
	}
	out.waitFor(t, "turn_end done and listening", func(msg ServerMessage) bool {
		if !turnEnd(1, TurnDone)(msg) {
			return false
		}
		frames := out.snapshot()
		last := frames[len(frames)-1]
		return last.Type == ServerState && last.State == StateListening
	})
	got := describe(out.snapshot())
	want := "ready state:listening state:thinking transcript:你好 " +
		"reply:1.1 state:speaking audio_start:1.1 binary:<第一句话已经说完了。> audio_end:1.1 " +
		// 回复流程每发一条消息就是一处断句，消息之间不会再拼成一句。
		"reply:1.2 audio_start:1.2 binary:<第二句话还在> audio_end:1.2 " +
		"reply:1.3 audio_start:1.3 binary:<后面接着说呢！> audio_end:1.3 " +
		"turn_end:1:done state:listening"
	if got != want {
		t.Fatalf("下行顺序不对\n got: %s\nwant: %s", got, want)
	}
}

func TestSessionInterruptStopsSynthesisAndDownlink(t *testing.T) {
	synthCancelled := make(chan struct{})
	backend := &fakeBackend{replyText: "第一句先念出来吧。第二句会被打断的！第三句根本轮不到。"}
	backend.synthesize = func(ctx context.Context, text string, chunk func(string, []byte) error) error {
		if strings.HasPrefix(text, "第一句") {
			return chunk("audio/mpeg", []byte("<1>"))
		}
		if err := chunk("audio/mpeg", []byte("<2a>")); err != nil {
			return err
		}
		<-ctx.Done()
		close(synthCancelled)
		// 合成器在取消后还想交出数据，也不能下发。
		if err := chunk("audio/mpeg", []byte("<2b>")); err == nil {
			t.Error("打断后的音频片没有被拒绝")
		}
		return ctx.Err()
	}
	session, out := startSession(t, backend, ClientMessage{Segmentation: SegmentByCmd})
	session.HandleControl(ClientMessage{Type: ClientText, Text: "讲个故事"})
	out.waitFor(t, "第二句第一片", func(msg ServerMessage) bool { return msg.Type == "binary" && msg.Text == "<2a>" })
	session.HandleControl(ClientMessage{Type: ClientInterrupt})
	out.waitFor(t, "turn_end interrupted", turnEnd(1, TurnInterrupted))
	select {
	case <-synthCancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("打断后合成没有被取消")
	}
	// 等这一轮的协程彻底收尾，再确认打断之后没有任何这一轮的下行。
	session.mu.Lock()
	done := session.lastDone
	session.mu.Unlock()
	<-done
	frames := out.snapshot()
	interruptedAt := -1
	for i, frame := range frames {
		if frame.Type == ServerInterrupted {
			interruptedAt = i
		}
	}
	for _, frame := range frames[interruptedAt+1:] {
		switch frame.Type {
		case "binary", ServerReply, ServerAudioStart, ServerAudioEnd:
			t.Fatalf("打断后还有下行：%s", describe(frames))
		}
	}
	if strings.Contains(describe(frames), "第三句") {
		t.Fatalf("第三句不该下发：%s", describe(frames))
	}
}

// bargeInBackend 第一轮一直说下去（每句交出一片后卡住，直到被打断）；后面几轮
// 按 transcribe 给的文字回一句短的。
func bargeInBackend(transcript string) *fakeBackend {
	backend := &fakeBackend{}
	backend.transcribe = func(ctx context.Context, audio []byte, format string) (string, error) {
		return transcript, nil
	}
	backend.reply = func(ctx context.Context, text string, emit func(string)) error {
		if text == "说点什么" {
			emit("我会一直说下去，直到你打断我为止。")
			return nil
		}
		emit("好，换个话题。")
		return nil
	}
	backend.synthesize = func(ctx context.Context, text string, chunk func(string, []byte) error) error {
		if err := chunk("audio/mpeg", []byte("<"+text+">")); err != nil {
			return err
		}
		if strings.Contains(text, "一直说") {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	return backend
}

func speakUp(session *Session) {
	pcm := append(tone(400, 9000), silence(900)...)
	for i := 0; i < len(pcm); i += 640 {
		session.HandleAudio(pcm[i:min(i+640, len(pcm))])
	}
}

func frameIndex(frames []ServerMessage, match func(ServerMessage) bool) int {
	for i, frame := range frames {
		if match(frame) {
			return i
		}
	}
	return -1
}

// 先生成再打断：开口不停，这句转写出字、新回复的第一句合成出声音时才打断正在说的
// 那一轮，中间没有空白。
func TestSessionServerVADBargeInInterruptsOnceNewReplyHasAudio(t *testing.T) {
	session, out := startSession(t, bargeInBackend("换个话题"), ClientMessage{Format: FormatPCM16, SampleRate: testRate})
	session.HandleControl(ClientMessage{Type: ClientText, Text: "说点什么"})
	out.waitFor(t, "开始播报", func(msg ServerMessage) bool { return msg.Type == "binary" })
	speakUp(session)
	out.waitFor(t, "第二轮说完", turnEnd(2, TurnDone))
	frames := out.snapshot()
	speechStart := frameIndex(frames, func(msg ServerMessage) bool { return msg.Type == ServerSpeech && msg.Event == "start" })
	transcript := frameIndex(frames, func(msg ServerMessage) bool { return msg.Type == ServerTranscript && msg.Turn == 2 })
	interrupted := frameIndex(frames, func(msg ServerMessage) bool { return msg.Type == ServerInterrupted && msg.Turn == 1 })
	newAudio := frameIndex(frames, func(msg ServerMessage) bool { return msg.Type == ServerAudioStart && msg.Turn == 2 })
	if speechStart < 0 || transcript < 0 || interrupted < 0 || newAudio < 0 {
		t.Fatalf("缺帧：%s", describe(frames))
	}
	if !(speechStart < transcript && transcript < interrupted && interrupted < newAudio) {
		t.Fatalf("应开口→转写→新回复出声时才打断：%s", describe(frames))
	}
	if frameIndex(frames, turnEnd(1, TurnInterrupted)) < 0 {
		t.Fatalf("第一轮应以 interrupted 结束：%s", describe(frames))
	}
}

// 开口了但转写不出字（咳嗽、底噪、外放的回声）：正在说的那一轮照常说完。
func TestSessionNoiseDoesNotInterruptSpeech(t *testing.T) {
	backend := bargeInBackend("")
	release := make(chan struct{})
	backend.synthesize = func(ctx context.Context, text string, chunk func(string, []byte) error) error {
		if err := chunk("audio/mpeg", []byte("<"+text+">")); err != nil {
			return err
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	session, out := startSession(t, backend, ClientMessage{Format: FormatPCM16, SampleRate: testRate})
	session.HandleControl(ClientMessage{Type: ClientText, Text: "说点什么"})
	out.waitFor(t, "开始播报", func(msg ServerMessage) bool { return msg.Type == "binary" })
	speakUp(session)
	out.waitFor(t, "第二轮没听出字", turnEnd(2, TurnNoSpeech))
	close(release)
	out.waitFor(t, "第一轮照常说完", turnEnd(1, TurnDone))
	if frameIndex(out.snapshot(), isType(ServerInterrupted)) >= 0 {
		t.Fatalf("杂音不该打断：%s", describe(out.snapshot()))
	}
}

// 新的一句有字以后，前一轮不再往下想：回复流程里后面才发出的消息不再念。
func TestSessionNewUtteranceStopsPreviousReplyGeneration(t *testing.T) {
	backend := bargeInBackend("等一下")
	more := make(chan struct{})
	cancelled := make(chan struct{})
	backend.reply = func(ctx context.Context, text string, emit func(string)) error {
		if text != "说点什么" {
			emit("好，你说。")
			return nil
		}
		emit("我会一直说下去，直到你打断我为止。")
		select {
		case <-ctx.Done():
			close(cancelled)
		case <-more:
		}
		emit("这句不该念出来。")
		return ctx.Err()
	}
	session, out := startSession(t, backend, ClientMessage{Format: FormatPCM16, SampleRate: testRate})
	session.HandleControl(ClientMessage{Type: ClientText, Text: "说点什么"})
	out.waitFor(t, "开始播报", func(msg ServerMessage) bool { return msg.Type == "binary" })
	speakUp(session)
	out.waitFor(t, "第二轮说完", turnEnd(2, TurnDone))
	select {
	case <-cancelled:
	default:
		t.Fatal("前一轮的回复流程应被取消")
	}
	if strings.Contains(describe(out.snapshot()), "不该念") {
		t.Fatalf("被顶替的一轮后面的消息不该再念：%s", describe(out.snapshot()))
	}
}

func TestSessionServerVADSegmentsAndTranscribesUtterance(t *testing.T) {
	backend := &fakeBackend{replyText: "收到你的语音了。"}
	backend.transcribe = func(ctx context.Context, audio []byte, format string) (string, error) {
		if format != FormatWAV || string(audio[:4]) != "RIFF" {
			return "", fmt.Errorf("应收到 WAV，format=%s", format)
		}
		return "一句语音", nil
	}
	session, out := startSession(t, backend, ClientMessage{Format: FormatPCM16, SampleRate: testRate})
	pcm := append(append(silence(300), tone(800, 9000)...), silence(900)...)
	for i := 0; i < len(pcm); i += 640 {
		session.HandleAudio(pcm[i:min(i+640, len(pcm))])
	}
	out.waitFor(t, "speech end", func(msg ServerMessage) bool { return msg.Type == ServerSpeech && msg.Event == "end" })
	out.waitFor(t, "transcript", func(msg ServerMessage) bool { return msg.Type == ServerTranscript && msg.Text == "一句语音" })
	out.waitFor(t, "turn_end done", turnEnd(1, TurnDone))
}

func TestSessionMergesUtteranceThatHadNotReachedReply(t *testing.T) {
	release := make(chan struct{})
	backend := &fakeBackend{replyText: "好的，明天晴天。"}
	backend.transcribe = func(ctx context.Context, audio []byte, format string) (string, error) {
		if string(audio) == "我想问一下" {
			// 第一句转写得慢，用户接着说了第二句。
			<-release
		}
		return string(audio), nil
	}
	session, out := startSession(t, backend, ClientMessage{Format: FormatWebM})
	session.HandleAudio([]byte("我想问一下"))
	session.HandleControl(ClientMessage{Type: ClientCommit})
	out.waitFor(t, "第一轮开始", func(msg ServerMessage) bool { return msg.Type == ServerState && msg.State == StateThinking })
	session.HandleAudio([]byte("明天天气怎么样"))
	out.waitFor(t, "第一轮被并入", turnEnd(1, TurnMerged))
	session.HandleControl(ClientMessage{Type: ClientCommit})
	close(release)
	out.waitFor(t, "第二轮完成", turnEnd(2, TurnDone))
	if got := backend.repliedTexts(); len(got) != 1 || got[0] != "我想问一下 明天天气怎么样" {
		t.Fatalf("两句应并成一轮回复：%q", got)
	}
}

func TestSessionWithoutBargeInIgnoresAudioWhileReplying(t *testing.T) {
	release := make(chan struct{})
	backend := &fakeBackend{replyText: "等我说完这一句。"}
	backend.synthesize = func(ctx context.Context, text string, chunk func(string, []byte) error) error {
		if err := chunk("audio/mpeg", []byte("<a>")); err != nil {
			return err
		}
		<-release
		return nil
	}
	off := false
	session, out := startSession(t, backend, ClientMessage{Format: FormatPCM16, SampleRate: testRate, BargeIn: &off})
	session.HandleControl(ClientMessage{Type: ClientText, Text: "你好"})
	out.waitFor(t, "播报中", func(msg ServerMessage) bool { return msg.Type == "binary" })
	session.HandleAudio(tone(600, 9000))
	session.HandleAudio(silence(900))
	close(release)
	out.waitFor(t, "turn_end done", turnEnd(1, TurnDone))
	for _, frame := range out.snapshot() {
		if frame.Type == ServerSpeech || frame.Type == ServerInterrupted {
			t.Fatalf("关闭打断时播报中的声音不该被处理：%s", describe(out.snapshot()))
		}
	}
}

func TestSessionRunsRepliesOneAtATime(t *testing.T) {
	var mu sync.Mutex
	running, maxRunning := 0, 0
	backend := &fakeBackend{}
	backend.reply = func(ctx context.Context, text string, emit func(string)) error {
		mu.Lock()
		running++
		maxRunning = max(maxRunning, running)
		mu.Unlock()
		// 被打断后回复流程还要花点时间收尾，后一轮不能趁这时候插进来。
		<-ctx.Done()
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		running--
		mu.Unlock()
		return ctx.Err()
	}
	session, out := startSession(t, backend, ClientMessage{Segmentation: SegmentByCmd})
	session.HandleControl(ClientMessage{Type: ClientText, Text: "第一句"})
	out.waitFor(t, "第一轮", func(msg ServerMessage) bool { return msg.Type == ServerTranscript && msg.Turn == 1 })
	time.Sleep(20 * time.Millisecond)
	session.HandleControl(ClientMessage{Type: ClientText, Text: "第二句"})
	out.waitFor(t, "第二轮转写", func(msg ServerMessage) bool { return msg.Type == ServerTranscript && msg.Turn == 2 })
	time.Sleep(100 * time.Millisecond)
	session.HandleControl(ClientMessage{Type: ClientInterrupt})
	session.Close()
	mu.Lock()
	defer mu.Unlock()
	if maxRunning != 1 {
		t.Fatalf("同时在跑的回复 = %d，应一次一轮", maxRunning)
	}
}

func TestSessionRejectsBadStartAndUnstartedMessages(t *testing.T) {
	out := newRecorder()
	session := NewSession(context.Background(), &fakeBackend{}, out, Options{})
	defer session.Close()
	session.HandleControl(ClientMessage{Type: ClientText, Text: "hi"})
	out.waitFor(t, "not_started", func(msg ServerMessage) bool { return msg.Code == "not_started" })
	if err := session.HandleControl(ClientMessage{Type: ClientStart, Format: "flac"}); err != ErrHangup {
		t.Fatalf("不支持的格式应挂断，err = %v", err)
	}
	out.waitFor(t, "unsupported_format", func(msg ServerMessage) bool { return msg.Code == "unsupported_format" && msg.Fatal })
}

func TestSessionCompressedFormatForcesClientSegmentation(t *testing.T) {
	_, out := startSession(t, &fakeBackend{}, ClientMessage{Format: FormatWebM, Segmentation: SegmentByVAD})
	ready := out.waitFor(t, "ready", isType(ServerReady))
	if ready.Ready.Segmentation != SegmentByCmd {
		t.Fatalf("webm 只能客户端分句，got %q", ready.Ready.Segmentation)
	}
}
