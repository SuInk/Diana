// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/qqcall"
)

// 来电的判断全靠外部可见的东西，因为 QQ 不给接口：
//   - 有电话：X 上出现标题为「语音通话」的窗口，关掉就是通话结束；
//   - 是打进来的：窗口里昵称下面写着「邀请你语音通话」（OCR）；
//   - 接通了：QQ 的音频引擎 TRAE 开始从虚拟麦克风采集；
//   - 对方是谁：接通时 SnowLuma 日志里那行 529/11 推送的 from。
const (
	pollInterval = 300 * time.Millisecond
	peerWait     = 6 * time.Second
)

type agent struct {
	cfg config

	mu     sync.Mutex
	client *client
	call   *call
	audio  *audioLink
}

type call struct {
	id       string
	window   string
	state    string
	incoming bool
	name     string
	peer     string
	// logPath/logOffset 记下响铃时 SnowLuma 日志写到哪了，只在这之后找接通推送。
	logPath   string
	logOffset int64
}

func newAgent(cfg config) *agent {
	return &agent{cfg: cfg}
}

func (a *agent) attach(c *client) {
	a.mu.Lock()
	old := a.client
	a.client = c
	hello := qqcall.Message{Type: qqcall.TypeHello, Version: version, State: qqcall.StateIdle}
	if a.call != nil && a.call.incoming {
		hello.State, hello.Call, hello.Name, hello.PeerUIN = a.call.state, a.call.id, a.call.name, a.call.peer
	}
	a.mu.Unlock()
	if old != nil {
		// 只服务一个 Diana：新连接顶掉旧的。
		_ = old.conn.Close()
	}
	c.send(hello)
}

func (a *agent) detach(c *client) {
	a.mu.Lock()
	if a.client == c {
		a.client = nil
	}
	a.mu.Unlock()
	_ = c.conn.Close()
}

func (a *agent) emit(msg qqcall.Message) {
	a.mu.Lock()
	c := a.client
	a.mu.Unlock()
	if c != nil {
		c.send(msg)
	}
}

func (a *agent) watch(ctx context.Context) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			a.stopAudio()
			return
		case <-ticker.C:
			a.tick(ctx)
		}
	}
}

func (a *agent) tick(ctx context.Context) {
	windows := findWindows(ctx, a.cfg.WindowTitle)
	a.mu.Lock()
	current := a.call
	a.mu.Unlock()

	if current == nil {
		if len(windows) == 0 {
			return
		}
		a.begin(ctx, windows[0])
		return
	}
	if !contains(windows, current.window) {
		a.end(current)
		return
	}
	if current.state != qqcall.StateConnected && captureActive(ctx) {
		a.connect(ctx, current)
	}
}

func (a *agent) begin(ctx context.Context, window string) {
	c := &call{id: newCallID(), window: window, state: qqcall.StateRinging}
	c.logPath, c.logOffset = snowlumaLogPosition(a.cfg.LogDir)
	text, err := ocrWindow(ctx, window, a.cfg.NameArea)
	if err != nil {
		log.Printf("识别来电窗口失败: %v", err)
	}
	c.name, c.incoming = qqcall.ParseCallerText(text)
	a.mu.Lock()
	a.call = c
	a.mu.Unlock()
	if !c.incoming {
		// 自己拨出去的（有人在 noVNC 里点的），或者识别失败。都不报给 Diana：
		// 它只管接电话，报了反而可能被当成来电去点「接听」。
		log.Printf("call %s: 不是来电（识别到 %q），不处理", c.id, text)
		return
	}
	log.Printf("call %s: 来电 %q", c.id, c.name)
	a.emit(qqcall.Message{Type: qqcall.TypeRinging, Call: c.id, Name: c.name})
}

func (a *agent) connect(ctx context.Context, c *call) {
	a.mu.Lock()
	c.state = qqcall.StateConnected
	a.mu.Unlock()
	if !c.incoming {
		return
	}
	if err := a.startAudio(ctx); err != nil {
		log.Printf("call %s: 音频没接上: %v", c.id, err)
		a.emit(qqcall.Message{Type: qqcall.TypeError, Call: c.id, Error: "音频没接上: " + err.Error()})
	}
	go func() {
		defer recoverGoroutinePanic("peer")
		peer := waitPeer(ctx, c.logPath, c.logOffset, peerWait)
		a.mu.Lock()
		c.peer = peer
		a.mu.Unlock()
		log.Printf("call %s: 接通，对方 QQ 号%s", c.id, map[bool]string{true: "已读到", false: "没读到"}[peer != ""])
		a.emit(qqcall.Message{Type: qqcall.TypeConnected, Call: c.id, Name: c.name, PeerUIN: peer})
	}()
}

func (a *agent) end(c *call) {
	a.stopAudio()
	a.mu.Lock()
	if a.call == c {
		a.call = nil
	}
	a.mu.Unlock()
	if c.incoming {
		log.Printf("call %s: 结束", c.id)
		a.emit(qqcall.Message{Type: qqcall.TypeEnded, Call: c.id})
	}
}

// command 处理 Diana 的指令。指令带的 call 必须是当前这通，避免点到下一通电话上。
func (a *agent) command(msg qqcall.Message) error {
	if msg.Type == qqcall.TypeFlush {
		a.mu.Lock()
		link := a.audio
		a.mu.Unlock()
		if link != nil {
			link.flush()
		}
		return nil
	}
	a.mu.Lock()
	c := a.call
	a.mu.Unlock()
	if c == nil || !c.incoming || c.id != msg.Call {
		return errors.New("没有这通电话")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	switch msg.Type {
	case qqcall.TypeAnswer:
		if c.state != qqcall.StateRinging {
			return errors.New("这通电话不在响铃")
		}
		return clickWindow(ctx, c.window, a.cfg.AnswerPos)
	case qqcall.TypeHangup:
		if c.state == qqcall.StateConnected {
			return clickWindow(ctx, c.window, a.cfg.HangupPos)
		}
		return clickWindow(ctx, c.window, a.cfg.RejectPos)
	}
	return fmt.Errorf("不认识的指令 %q", msg.Type)
}

func findWindows(ctx context.Context, title string) []string {
	out, err := exec.CommandContext(ctx, "xdotool", "search", "--onlyvisible", "--name", "^"+title+"$").Output()
	if err != nil {
		// 没有匹配的窗口时 xdotool 也是非零退出。
		return nil
	}
	return strings.Fields(string(out))
}

func clickWindow(ctx context.Context, window string, at point) error {
	if out, err := exec.CommandContext(ctx, "xdotool", "windowactivate", "--sync", window).CombinedOutput(); err != nil {
		return fmt.Errorf("激活通话窗口失败: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.CommandContext(ctx, "xdotool", "mousemove", "--window", window, strconv.Itoa(at.X), strconv.Itoa(at.Y), "click", "1").CombinedOutput(); err != nil {
		return fmt.Errorf("点击失败: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ocrWindow 截通话窗口里昵称那一块，放大后交给 tesseract。字很小，不放大认不出来。
func ocrWindow(ctx context.Context, window string, area rect) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	crop := fmt.Sprintf("%dx%d+%d+%d", area.W, area.H, area.X, area.Y)
	shot := exec.CommandContext(ctx, "import", "-window", window, "-crop", crop, "+repage", "-resize", "300%", "png:-")
	img, err := shot.Output()
	if err != nil {
		return "", fmt.Errorf("截图失败: %w", err)
	}
	ocr := exec.CommandContext(ctx, "tesseract", "stdin", "stdout", "-l", "chi_sim+eng", "--psm", "6")
	ocr.Stdin = bytes.NewReader(img)
	text, err := ocr.Output()
	if err != nil {
		return "", fmt.Errorf("OCR 失败: %w", err)
	}
	return string(text), nil
}

// snowlumaLogPosition 返回今天的 SnowLuma 日志和它现在的长度。
func snowlumaLogPosition(dir string) (string, int64) {
	path := filepath.Join(dir, "snowluma-"+time.Now().Format("2006-01-02")+".log")
	info, err := os.Stat(path)
	if err != nil {
		return path, 0
	}
	return path, info.Size()
}

// waitPeer 从 offset 往后读日志，等接通推送那一行。
func waitPeer(ctx context.Context, path string, offset int64, wait time.Duration) string {
	deadline := time.Now().Add(wait)
	for {
		if peer := scanPeer(path, offset); peer != "" {
			return peer
		}
		if time.Now().After(deadline) {
			return ""
		}
		select {
		case <-ctx.Done():
			return ""
		case <-time.After(pollInterval):
		}
	}
}

func scanPeer(path string, offset int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return ""
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		if peer := qqcall.PeerFromLogLine(scanner.Text()); peer != "" {
			return peer
		}
	}
	return ""
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func newCallID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
