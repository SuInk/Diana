// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Package voicecall 是和机器人实时语音通话的会话逻辑：收音频、按静音分句、转写、
// 交给回复流程、把回复逐句合成后下发，以及播报中被打断。它不关心传输层和机器人
// 内部，网页测试页和手机 App 走的是同一套协议（docs/voice-call.md）。
package voicecall

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/SuInk/diana/internal/safego"
)

// Backend 是通话用到的三件事。实现方负责把它们接到语音识别插槽、回复流程和语音
// 合成插槽上。
type Backend interface {
	// Transcribe 转写一句话。format 是 FormatWAV 或客户端声明的封装格式。
	Transcribe(ctx context.Context, audio []byte, format string) (string, error)
	// Reply 用这句话走一轮回复。回复每发出一条就调一次 emit；返回时这一轮结束。
	// ctx 取消时应尽快返回。
	Reply(ctx context.Context, text string, emit func(string)) error
	// Synthesize 把一句话合成为音频，合成出多少就交出多少。chunk 返回错误时停止。
	Synthesize(ctx context.Context, text string, chunk func(mime string, data []byte) error) error
}

// HangupBackend 是能让机器人自己挂电话的 Backend：回复流程里决定挂断时 TakeHangup
// 返回 true，会话把这一轮念完后下发 hangup 并关闭。
type HangupBackend interface {
	TakeHangup() bool
}

// Speaker 是群聊通话里说话的那一方。
type Speaker struct {
	ID   string
	Name string
}

// SpeakerBackend 是群聊通话的 Backend：一句话会有好几个成员接，每条回复带着是谁说
// 的，合成时用那个成员的声音。实现了它，会话就不再调 Backend.Reply 和
// Backend.Synthesize。
type SpeakerBackend interface {
	ReplyBySpeaker(ctx context.Context, text string, emit func(Speaker, string)) error
	SynthesizeAs(ctx context.Context, speaker Speaker, text string, chunk func(mime string, data []byte) error) error
}

// Transport 是下行通道。实现方保证不会被并发调用以外的方式打乱顺序；会话自己
// 串行调用它。
type Transport interface {
	WriteJSON(ServerMessage) error
	WriteBinary([]byte) error
}

// Options 是一通电话的固定信息。
type Options struct {
	CallID  string
	BotID   string
	BotName string
	// MaxUtteranceBytes 是一句话的音频上限，默认 8 MiB。
	MaxUtteranceBytes int
	// VAD 为空时用 DefaultVADConfig。
	VAD func(sampleRate int) VADConfig
}

const (
	defaultMaxUtteranceBytes = 8 << 20
	defaultSampleRate        = 16000
	// TurnMerged 是被后一句接上的那一轮的结束原因：这句话还没进回复流程，后一句
	// 会带着它的文字一起说。
	TurnMerged = "merged"
)

// ErrHangup 表示客户端挂断，传输层收到后关闭连接。
var ErrHangup = errors.New("voicecall: hangup")

var errTurnGone = errors.New("voicecall: turn is no longer active")

// recoverGoroutinePanic 挡住会话协程里的 panic：一通电话出错不能带垮整个进程。
func recoverGoroutinePanic(component string) { safego.Report("voicecall."+component, recover()) }

// Session 是一通电话。HandleControl 和 HandleAudio 由读循环串行调用。
type Session struct {
	backend Backend
	out     Transport
	opts    Options

	ctx    context.Context
	cancel context.CancelFunc

	mu           sync.Mutex
	started      bool
	closed       bool
	format       string
	sampleRate   int
	segmentation string
	bargeIn      bool
	vad          *VAD
	buffer       []byte
	turnSeq      int
	// active 是正在说（或正在想、还没被顶替）的那一轮，下行以它为准。
	active *turn
	// next 是用户在 active 回着的时候又说的一句：先转写、先想好回复，第一句合成出
	// 声音时才顶替 active（见 promoteLocked）。一时只留一个候补，更新的一句来了，
	// 还没出声的候补直接作废。
	next *turn
	// lastDone 是最近一轮的结束信号，挂断时等它收尾。
	lastDone chan struct{}
	// lastReply 是最近一轮的回复流程结束信号：回复流程一次只跑一轮，后一轮要等前一轮
	// 的回复流程收完尾才进。只等回复流程，不等它念完：前一轮念着的时候后一轮就能想。
	lastReply chan struct{}
	// pendingCarry 是开口打断时还没进回复流程的那一轮，下一句接上它的文字。
	pendingCarry *turn

	botHungUp bool

	// done 在会话结束时关闭，见 Done。
	done      chan struct{}
	doneOnce  sync.Once
	doneClose sync.Once
}

type turn struct {
	id     int
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	// genCtx 只管回复流程：后一句有了文字，这一轮就不用再往下想了，但已经排好的
	// 句子还接着念，直到后一句的回复出声。取消 ctx 才是连念也停。
	genCtx    context.Context
	genCancel context.CancelFunc
	// replyDone 在回复流程结束（或这一轮根本没走到回复流程）时关闭。
	replyDone chan struct{}
	replyOnce sync.Once

	audio  []byte
	format string
	text   string
	carry  *turn

	// transcribed 关闭后 transcript/transcribeErr 可读。转写不随这一轮取消：被并进
	// 下一轮时，下一轮还要用它的文字。
	transcribed   chan struct{}
	transcript    string
	transcribeErr error

	// 下面几项由 Session.mu 保护。
	replyStarted bool
	speaking     bool
	ended        bool
}

func NewSession(parent context.Context, backend Backend, out Transport, opts Options) *Session {
	if opts.MaxUtteranceBytes <= 0 {
		opts.MaxUtteranceBytes = defaultMaxUtteranceBytes
	}
	if opts.VAD == nil {
		opts.VAD = DefaultVADConfig
	}
	ctx, cancel := context.WithCancel(parent)
	return &Session{backend: backend, out: out, opts: opts, ctx: ctx, cancel: cancel}
}

// HandleControl 处理一条控制消息。返回 ErrHangup 时调用方应关闭连接。
func (s *Session) HandleControl(msg ClientMessage) error {
	if msg.Type != ClientStart && !s.isStarted() {
		s.sendError("not_started", "先发 start 再发其他消息", false)
		return nil
	}
	switch msg.Type {
	case ClientStart:
		return s.start(msg)
	case ClientCommit:
		s.commit()
	case ClientInterrupt:
		s.mu.Lock()
		s.pendingCarry = nil
		s.interruptLocked()
		s.mu.Unlock()
	case ClientText:
		text := strings.TrimSpace(msg.Text)
		if text == "" {
			return nil
		}
		s.mu.Lock()
		s.startTurnLocked(&turn{text: text})
		s.mu.Unlock()
	case ClientHangup:
		s.Close()
		return ErrHangup
	default:
		s.sendError("unknown_type", fmt.Sprintf("不认识的消息类型 %q", msg.Type), false)
	}
	return nil
}

// HandleAudio 处理一帧上行音频。
func (s *Session) HandleAudio(frame []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started || s.closed {
		return
	}
	if s.segmentation == SegmentByCmd {
		if len(s.buffer) == 0 && s.active != nil {
			// 客户端分句时只在用户说话时才上行，所以新一句的第一帧就是开口。
			if !s.bargeIn {
				return
			}
			s.mergeUnstartedLocked()
		}
		if len(s.buffer)+len(frame) > s.opts.MaxUtteranceBytes {
			s.buffer = nil
			s.writeLocked(ServerMessage{Type: ServerError, Code: "utterance_too_long", Text: "这句话太长了，已丢弃"})
			return
		}
		s.buffer = append(s.buffer, frame...)
		return
	}
	if s.active != nil && !s.bargeIn {
		// 不许打断时，机器人说话期间的声音不当输入，免得把它自己的声音录进去。
		return
	}
	s.handleVADLocked(s.vad.Push(frame))
}

func (s *Session) handleVADLocked(events []VADEvent) {
	for _, event := range events {
		switch event.Kind {
		case VADSpeechStart:
			// 开口不打断正在回的那一轮：可能只是咳嗽、底噪或外放的回声。等这句转写出字、
			// 新回复出声再打断（见 runTurn、promoteLocked）。
			s.writeLocked(ServerMessage{Type: ServerSpeech, Event: "start"})
			s.mergeUnstartedLocked()
		case VADSpeechEnd:
			s.writeLocked(ServerMessage{Type: ServerSpeech, Event: "end"})
			s.startTurnLocked(&turn{audio: PCMToWAV(event.Audio, s.sampleRate), format: FormatWAV})
		case VADSpeechDiscarded:
			s.writeLocked(ServerMessage{Type: ServerSpeech, Event: "discarded"})
			// 一声咳嗽打断了还没开始回的那一轮：那句话不能就这么丢了，单独接着回。
			if s.pendingCarry != nil {
				s.startTurnLocked(&turn{})
			}
		}
	}
}

func (s *Session) start(msg ClientMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		s.writeLocked(ServerMessage{Type: ServerError, Code: "already_started", Text: "通话已经开始"})
		return nil
	}
	format := strings.ToLower(strings.TrimSpace(msg.Format))
	if format == "" {
		format = FormatPCM16
	}
	switch format {
	case FormatPCM16, FormatWebM, FormatOgg, FormatWAV, FormatMP4:
	default:
		s.writeLocked(ServerMessage{Type: ServerError, Code: "unsupported_format", Text: fmt.Sprintf("不支持的音频格式 %q", format), Fatal: true})
		return ErrHangup
	}
	segmentation := strings.TrimSpace(msg.Segmentation)
	if format != FormatPCM16 || segmentation == SegmentByCmd {
		// 只有裸 PCM 能在服务端听静音。
		segmentation = SegmentByCmd
	} else {
		segmentation = SegmentByVAD
	}
	s.format, s.segmentation = format, segmentation
	s.sampleRate = msg.SampleRate
	if s.sampleRate <= 0 {
		s.sampleRate = defaultSampleRate
	}
	if format == FormatPCM16 && (s.sampleRate < 8000 || s.sampleRate > 48000) {
		s.writeLocked(ServerMessage{Type: ServerError, Code: "unsupported_format", Text: "采样率要在 8000 到 48000 之间", Fatal: true})
		return ErrHangup
	}
	s.bargeIn = msg.BargeIn == nil || *msg.BargeIn
	if segmentation == SegmentByVAD {
		s.vad = NewVAD(s.opts.VAD(s.sampleRate))
	}
	s.started = true
	ready := &Ready{CallID: s.opts.CallID, BotID: s.opts.BotID, BotName: s.opts.BotName, Format: format, Segmentation: segmentation, BargeIn: s.bargeIn}
	if format == FormatPCM16 {
		ready.SampleRate = s.sampleRate
	}
	s.writeLocked(ServerMessage{Type: ServerReady, Ready: ready})
	s.writeLocked(ServerMessage{Type: ServerState, State: StateListening})
	return nil
}

func (s *Session) commit() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.segmentation == SegmentByVAD {
		s.handleVADLocked(s.vad.Flush())
		return
	}
	audio := s.buffer
	s.buffer = nil
	if len(audio) == 0 {
		if s.pendingCarry != nil {
			s.startTurnLocked(&turn{})
		}
		return
	}
	format := s.format
	if format == FormatPCM16 {
		audio, format = PCMToWAV(audio, s.sampleRate), FormatWAV
	}
	s.startTurnLocked(&turn{audio: audio, format: format})
}

// mergeUnstartedLocked 是用户开口了：还没进回复流程的那一轮留着，等这句说完带上
// 它的文字一起回。已经在回的不动，见 promoteLocked。
func (s *Session) mergeUnstartedLocked() {
	for _, t := range []*turn{s.next, s.active} {
		if t != nil && !t.ended && !t.replyStarted {
			s.endTurnLocked(t, TurnMerged)
			t.cancel()
			s.pendingCarry = t
			return
		}
	}
}

// promoteLocked 让候补的一轮接过话头：它的第一句已经合成出声音了，这时才打断还在
// 说的那一轮。先生成再打断，用户听不到「旧的停了、新的还没好」那段空白。
func (s *Session) promoteLocked(t *turn) {
	if s.next != t {
		return
	}
	if old := s.active; old != nil && !old.ended {
		old.cancel()
		s.writeLocked(ServerMessage{Type: ServerInterrupted, Turn: old.id})
		s.endTurnLocked(old, TurnInterrupted)
	}
	if s.next == t {
		s.active, s.next = t, nil
	}
}

// interruptLocked 立即停掉候补和正在说的那一轮，用户主动点打断时用。
func (s *Session) interruptLocked() {
	for _, t := range []*turn{s.next, s.active} {
		if t == nil || t.ended {
			continue
		}
		t.cancel()
		s.writeLocked(ServerMessage{Type: ServerInterrupted, Turn: t.id})
		s.endTurnLocked(t, TurnInterrupted)
	}
}

// endTurnLocked 结束一轮并告诉客户端。之后这一轮的任何下行都会被丢掉。正在说的
// 那一轮结束时，候补直接接上。
func (s *Session) endTurnLocked(t *turn, reason string) {
	if t.ended {
		return
	}
	t.ended = true
	promoted := false
	switch {
	case s.active == t:
		s.active, s.next = s.next, nil
		promoted = s.active != nil
	case s.next == t:
		s.next = nil
	}
	s.writeLocked(ServerMessage{Type: ServerTurnEnd, Turn: t.id, Reason: reason})
	switch {
	case s.closed:
	case s.active == nil:
		s.writeLocked(ServerMessage{Type: ServerState, State: StateListening})
	case promoted && !s.active.speaking:
		s.writeLocked(ServerMessage{Type: ServerState, State: StateThinking})
	}
}

func (s *Session) startTurnLocked(t *turn) {
	if s.closed {
		return
	}
	s.mergeUnstartedLocked()
	// 还没出声的候补被更新的一句顶掉：它的话已经进了回复流程，作废就行。
	if old := s.next; old != nil {
		old.cancel()
		s.writeLocked(ServerMessage{Type: ServerInterrupted, Turn: old.id})
		s.endTurnLocked(old, TurnInterrupted)
	}
	s.turnSeq++
	t.id = s.turnSeq
	t.ctx, t.cancel = context.WithCancel(s.ctx)
	t.genCtx, t.genCancel = context.WithCancel(t.ctx)
	t.done = make(chan struct{})
	t.transcribed = make(chan struct{})
	t.replyDone = make(chan struct{})
	t.carry, s.pendingCarry = s.pendingCarry, nil
	previous := s.lastReply
	s.lastDone, s.lastReply = t.done, t.replyDone
	if s.active != nil {
		s.next = t
	} else {
		s.active = t
		s.writeLocked(ServerMessage{Type: ServerState, State: StateThinking})
	}
	go func() {
		defer recoverGoroutinePanic("transcribe")
		s.transcribe(t)
	}()
	go func() {
		defer recoverGoroutinePanic("turn")
		s.runTurn(t, previous)
	}()
}

func (s *Session) transcribe(t *turn) {
	defer close(t.transcribed)
	if len(t.audio) == 0 {
		t.transcript = t.text
		return
	}
	text, err := s.backend.Transcribe(s.ctx, t.audio, t.format)
	t.transcript, t.transcribeErr = strings.TrimSpace(text), err
}

// fullText 是这一轮连同被它接上的几轮一起的文字。
func (t *turn) fullText(ctx context.Context) (string, error) {
	var parts []string
	for current := t; current != nil; current = current.carry {
		select {
		case <-current.transcribed:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		if current.transcribeErr != nil && current == t {
			return "", current.transcribeErr
		}
		if current.transcript != "" {
			parts = append([]string{current.transcript}, parts...)
		}
	}
	return strings.Join(parts, " "), nil
}

func (s *Session) runTurn(t *turn, previous chan struct{}) {
	defer close(t.done)
	// 没走到回复流程就退出时，也要等前一轮的回复流程收完尾再放行后一轮：被顶掉的
	// 候补早早退出，不能让更后面的一轮趁前面还在想的时候插进来。
	defer func() {
		if previous != nil {
			<-previous
		}
		t.finishReply()
	}()
	text, err := t.fullText(t.ctx)
	if err != nil {
		if t.ctx.Err() == nil {
			s.turnWrite(t, ServerMessage{Type: ServerError, Turn: t.id, Code: "stt_failed", Text: "语音识别失败：" + err.Error()})
			s.finishTurn(t, TurnFailed)
		}
		return
	}
	if text == "" {
		s.finishTurn(t, TurnNoSpeech)
		return
	}
	if s.turnWrite(t, ServerMessage{Type: ServerTranscript, Turn: t.id, Text: text}) != nil {
		return
	}
	// 这句有字了，是真开口：正在回的那一轮不用再往下想，已经排好的句子接着念，等这句
	// 的回复出声再打断。
	s.mu.Lock()
	if s.next == t && s.active != nil {
		s.active.genCancel()
	}
	s.mu.Unlock()
	if previous != nil {
		select {
		case <-previous:
		case <-t.ctx.Done():
			return
		}
	}
	s.mu.Lock()
	if t.ended {
		s.mu.Unlock()
		return
	}
	t.replyStarted = true
	s.mu.Unlock()

	sentences := make(chan spokenSentence, 64)
	spoken := make(chan struct{})
	go func() {
		defer recoverGoroutinePanic("speak")
		defer close(spoken)
		seq := 0
		for sentence := range sentences {
			if t.ctx.Err() != nil {
				continue
			}
			seq++
			s.speak(t, seq, sentence.speaker, sentence.text)
		}
	}()
	// emit 可能在回复流程的别的 goroutine 上调用，切句和计数要加锁。
	var (
		queueMu  sync.Mutex
		splitter SentenceSplitter
		replied  bool
		finished bool
	)
	queue := func(speaker Speaker, items []string) {
		for _, sentence := range items {
			replied = true
			select {
			case sentences <- spokenSentence{speaker: speaker, text: sentence}:
			case <-t.ctx.Done():
				return
			}
		}
	}
	// emit 在回复流程的发送路径上被调用；每条消息之间本来就是断句处。
	var replyErr error
	if group, ok := s.backend.(SpeakerBackend); ok {
		// 每条发言单独切句：上一个人没说完的半句不能并到下一个人嘴里。
		replyErr = group.ReplyBySpeaker(t.genCtx, text, func(speaker Speaker, message string) {
			queueMu.Lock()
			defer queueMu.Unlock()
			if finished || t.genCtx.Err() != nil {
				return
			}
			var own SentenceSplitter
			queue(speaker, append(own.Push(message+"\n"), own.Flush()...))
		})
	} else {
		replyErr = s.backend.Reply(t.genCtx, text, func(message string) {
			queueMu.Lock()
			defer queueMu.Unlock()
			if finished || t.genCtx.Err() != nil {
				return
			}
			queue(Speaker{}, splitter.Push(message+"\n"))
		})
	}
	queueMu.Lock()
	queue(Speaker{}, splitter.Flush())
	finished = true
	close(sentences)
	queueMu.Unlock()
	t.finishReply()
	<-spoken
	switch {
	case t.ctx.Err() != nil:
	case t.genCtx.Err() != nil:
		// 被后一句顶替：排好的句子念完了，后一句接着说。
		s.finishTurn(t, TurnInterrupted)
	case replyErr != nil:
		s.turnWrite(t, ServerMessage{Type: ServerError, Turn: t.id, Code: "reply_failed", Text: "回复失败：" + replyErr.Error()})
		s.finishTurn(t, TurnFailed)
	case !replied:
		s.finishTurn(t, TurnNoReply)
	default:
		s.finishTurn(t, TurnDone)
	}
	if t.ctx.Err() == nil {
		if hanger, ok := s.backend.(HangupBackend); ok && hanger.TakeHangup() {
			// 告别那句念完了：下发 hangup 再关。Close 要等这一轮收尾，不能在这里同步调。
			go func() {
				defer recoverGoroutinePanic("hangup")
				s.hangup()
			}()
		}
	}
}

// hangup 是机器人这边挂断：告诉客户端，然后关闭会话。
func (s *Session) hangup() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.writeLocked(ServerMessage{Type: ServerHangup})
	s.botHungUp = true
	s.mu.Unlock()
	s.Close()
}

// BotHungUp 报告这通电话是不是机器人自己挂的。传输层据此区分「机器人挂断」和
// 「连接意外断了」：后者可以等客户端重连。
func (s *Session) BotHungUp() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.botHungUp
}

// Done 在会话结束（任何一方挂断）后关闭，传输层据此收尾，比如关掉连接。
func (s *Session) Done() <-chan struct{} {
	s.doneOnce.Do(func() { s.done = make(chan struct{}) })
	return s.done
}

func (s *Session) markDone() {
	s.Done()
	s.doneClose.Do(func() { close(s.done) })
}

// speak 下发一句回复的文字，再把它合成后的音频逐片下发。合成失败只报错，文字已经
// 到了，通话继续。
func (s *Session) speak(t *turn, seq int, speaker Speaker, sentence string) {
	if s.turnWrite(t, ServerMessage{Type: ServerReply, Turn: t.id, Seq: seq, Text: sentence, Speaker: speaker.ID, SpeakerName: speaker.Name}) != nil {
		return
	}
	speakable := SpeakableText(sentence)
	if speakable == "" {
		return
	}
	started := false
	synthesize := s.backend.Synthesize
	if group, ok := s.backend.(SpeakerBackend); ok {
		synthesize = func(ctx context.Context, text string, chunk func(string, []byte) error) error {
			return group.SynthesizeAs(ctx, speaker, text, chunk)
		}
	}
	err := synthesize(t.ctx, speakable, func(mime string, data []byte) error {
		if len(data) == 0 {
			return nil
		}
		return s.turnDo(t, func() error {
			if !started {
				started = true
				s.promoteLocked(t)
				if !t.speaking {
					t.speaking = true
					s.writeLocked(ServerMessage{Type: ServerState, State: StateSpeaking})
				}
				if err := s.writeLocked(ServerMessage{Type: ServerAudioStart, Turn: t.id, Seq: seq, MIME: mime, Speaker: speaker.ID, SpeakerName: speaker.Name}); err != nil {
					return err
				}
			}
			return s.out.WriteBinary(data)
		})
	})
	if started {
		s.turnWrite(t, ServerMessage{Type: ServerAudioEnd, Turn: t.id, Seq: seq})
	}
	if err != nil && !errors.Is(err, errTurnGone) && t.ctx.Err() == nil {
		s.turnWrite(t, ServerMessage{Type: ServerError, Turn: t.id, Seq: seq, Code: "tts_failed", Text: "语音合成失败：" + err.Error()})
	}
}

// spokenSentence 是排队等念的一句，群聊通话里带着是谁说的。
type spokenSentence struct {
	speaker Speaker
	text    string
}

func (s *Session) finishTurn(t *turn, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.endTurnLocked(t, reason)
}

func (t *turn) finishReply() {
	t.replyOnce.Do(func() { close(t.replyDone) })
}

// turnDo 在这一轮还算数时执行 fn。持锁执行，保证打断之后不会再有这一轮的下行。
func (s *Session) turnDo(t *turn, fn func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.ended || s.closed || t.ctx.Err() != nil {
		return errTurnGone
	}
	return fn()
}

func (s *Session) turnWrite(t *turn, msg ServerMessage) error {
	return s.turnDo(t, func() error { return s.writeLocked(msg) })
}

func (s *Session) writeLocked(msg ServerMessage) error {
	return s.out.WriteJSON(msg)
}

func (s *Session) sendError(code, text string, fatal bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writeLocked(ServerMessage{Type: ServerError, Code: code, Text: text, Fatal: fatal})
}

func (s *Session) isStarted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started
}

// Close 挂断：停掉进行中的一轮，之后不再下发任何东西。
func (s *Session) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	var waits []chan struct{}
	for _, t := range []*turn{s.active, s.next} {
		if t != nil {
			t.cancel()
			waits = append(waits, t.done)
		}
	}
	s.closed = true
	if s.lastDone != nil {
		waits = append(waits, s.lastDone)
	}
	s.mu.Unlock()
	s.cancel()
	for _, done := range waits {
		<-done
	}
	s.markDone()
}
