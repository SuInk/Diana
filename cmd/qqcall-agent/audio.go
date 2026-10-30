// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/gorilla/websocket"

	"github.com/SuInk/diana/model/qqcall"
)

// 两条虚拟线：
//
//	QQ 扬声器 → qq_speaker（null sink）→ qq_speaker.monitor → parec → Diana（对方的声音）
//	Diana → pacat → bot_voice（null sink）→ bot_voice.monitor → bot_mic → QQ 麦克风
//
// QQ 的音频引擎跟着 PulseAudio 的默认设备走，所以把这两个设成默认就行。bot_mic 用
// remap-source 包一层：有的程序不把 .monitor 当麦克风列出来。
const (
	sinkSpeaker = "qq_speaker"
	sinkVoice   = "bot_voice"
	sourceMic   = "bot_mic"
)

var pcmArgs = []string{"--raw", "--format=s16le", "--rate=" + strconv.Itoa(qqcall.SampleRate), "--channels=1"}

// setupAudio 保证 PulseAudio 在跑、三个设备都在并设为默认。可以重复调用。
func setupAudio(ctx context.Context) error {
	if exec.CommandContext(ctx, "pactl", "info").Run() != nil {
		if out, err := exec.CommandContext(ctx, "pulseaudio", "--daemonize=yes", "--exit-idle-time=-1").CombinedOutput(); err != nil {
			return fmt.Errorf("启动 PulseAudio 失败: %v: %s", err, strings.TrimSpace(string(out)))
		}
	}
	sinks, _ := exec.CommandContext(ctx, "pactl", "list", "short", "sinks").Output()
	for _, sink := range []string{sinkSpeaker, sinkVoice} {
		if !strings.Contains(string(sinks), sink) {
			if err := pactl(ctx, "load-module", "module-null-sink", "sink_name="+sink); err != nil {
				return err
			}
		}
	}
	sources, _ := exec.CommandContext(ctx, "pactl", "list", "short", "sources").Output()
	if !strings.Contains(string(sources), sourceMic+"\t") {
		if err := pactl(ctx, "load-module", "module-remap-source", "master="+sinkVoice+".monitor", "source_name="+sourceMic); err != nil {
			return err
		}
	}
	if err := pactl(ctx, "set-default-sink", sinkSpeaker); err != nil {
		return err
	}
	return pactl(ctx, "set-default-source", sourceMic)
}

func pactl(ctx context.Context, args ...string) error {
	if out, err := exec.CommandContext(ctx, "pactl", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("pactl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// captureActive 判断 QQ 是否在采集麦克风，也就是电话接通了。响铃时 QQ 只放铃声，
// 不开麦克风。
func captureActive(ctx context.Context) bool {
	out, err := exec.CommandContext(ctx, "pactl", "list", "source-outputs").Output()
	return err == nil && strings.Contains(string(out), `application.name = "TRAE"`)
}

// audioLink 是一通电话的两条音频管道。
type audioLink struct {
	ctx    context.Context
	cancel context.CancelFunc
	rec    *exec.Cmd

	// queue 让 WebSocket 读循环不被 pacat 的实时消费卡住：Diana 一次发来一整句。
	queue chan []byte

	mu       sync.Mutex
	play     *exec.Cmd
	playPipe io.WriteCloser
}

func (a *agent) startAudio(ctx context.Context) error {
	a.stopAudio()
	linkCtx, cancel := context.WithCancel(ctx)
	link := &audioLink{ctx: linkCtx, cancel: cancel, queue: make(chan []byte, 1024)}
	rec := exec.CommandContext(linkCtx, "parec", append([]string{"--device=" + sinkSpeaker + ".monitor", "--latency-msec=40"}, pcmArgs...)...)
	stdout, err := rec.StdoutPipe()
	if err != nil {
		cancel()
		return err
	}
	if err := rec.Start(); err != nil {
		cancel()
		return fmt.Errorf("parec: %w", err)
	}
	link.rec = rec
	if err := link.startPlayer(); err != nil {
		cancel()
		_ = rec.Wait()
		return err
	}
	a.mu.Lock()
	a.audio = link
	a.mu.Unlock()
	go a.uplink(link, stdout)
	go link.drain()
	return nil
}

// uplink 把对方的声音按 20ms 一帧发给 Diana。
func (a *agent) uplink(link *audioLink, r io.Reader) {
	defer func() { _ = link.rec.Wait() }()
	buf := make([]byte, qqcall.FrameBytes)
	for {
		if _, err := io.ReadFull(r, buf); err != nil {
			return
		}
		a.mu.Lock()
		c := a.client
		a.mu.Unlock()
		if c != nil {
			c.write(websocket.BinaryMessage, append([]byte(nil), buf...))
		}
	}
}

func (a *agent) stopAudio() {
	a.mu.Lock()
	link := a.audio
	a.audio = nil
	a.mu.Unlock()
	if link == nil {
		return
	}
	link.cancel()
	link.mu.Lock()
	link.stopPlayerLocked()
	link.mu.Unlock()
}

// playback 把 Diana 发来的声音排进播放队列。没接通时丢掉。
func (a *agent) playback(pcm []byte) {
	a.mu.Lock()
	link := a.audio
	a.mu.Unlock()
	if link == nil {
		return
	}
	select {
	case link.queue <- pcm:
	default:
		log.Printf("播放队列满了，丢掉 %d 字节", len(pcm))
	}
}

// drain 按顺序把队列写进 pacat，pacat 按实时速率消费。
func (l *audioLink) drain() {
	for {
		select {
		case <-l.ctx.Done():
			return
		case pcm := <-l.queue:
			l.mu.Lock()
			w := l.playPipe
			l.mu.Unlock()
			if w == nil {
				continue
			}
			if _, err := w.Write(pcm); err != nil && !errors.Is(err, io.ErrClosedPipe) && !errors.Is(err, os.ErrClosed) {
				log.Printf("写入虚拟麦克风失败: %v", err)
			}
		}
	}
}

func (l *audioLink) startPlayer() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	play := exec.CommandContext(l.ctx, "pacat", append([]string{"--playback", "--device=" + sinkVoice, "--latency-msec=60"}, pcmArgs...)...)
	stdin, err := play.StdinPipe()
	if err != nil {
		return err
	}
	if err := play.Start(); err != nil {
		return fmt.Errorf("pacat: %w", err)
	}
	l.play, l.playPipe = play, stdin
	return nil
}

func (l *audioLink) stopPlayerLocked() {
	if l.play == nil {
		return
	}
	_ = l.playPipe.Close()
	if l.play.Process != nil {
		_ = l.play.Process.Kill()
	}
	_ = l.play.Wait()
	l.play, l.playPipe = nil, nil
}

// flush 丢掉 PulseAudio 里还没播的声音：打断时机器人要立刻闭嘴，不能把缓冲念完。
func (l *audioLink) flush() {
drained:
	for {
		select {
		case <-l.queue:
		default:
			break drained
		}
	}
	l.mu.Lock()
	l.stopPlayerLocked()
	l.mu.Unlock()
	if l.ctx.Err() != nil {
		return
	}
	if err := l.startPlayer(); err != nil {
		log.Printf("重开播放失败: %v", err)
	}
}
