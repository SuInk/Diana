// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Package vrchat 是 Diana 和 VRChat 客户端之间的 OSC 桥：往聊天框发字、按映射表
// 驱动 Avatar 参数、短时操控移动，并监听 VRChat 发回来的 Avatar 状态。
//
// 桥是进程级的单例：一台机器上只有一个 VRChat 客户端，OSC 端口也只能绑一次，
// 所以它不按机器人或群拆分。
package vrchat

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/internal/safego"
	"github.com/SuInk/diana/model/osc"
)

const (
	DefaultHost       = "127.0.0.1"
	DefaultSendPort   = 9000
	DefaultListenPort = 9001

	// MinChatboxInterval 是两段聊天框消息之间的最短间隔。VRChat 对聊天框有限速，
	// 发太快的会被客户端丢掉，而且看的人也来不及读完一段。
	MinChatboxInterval     = 1500 * time.Millisecond
	DefaultChatboxInterval = 3 * time.Second
	// maxChatboxPending 限制排队的段数：一段按 3 秒算，十几段已经要念一分钟了，
	// 再多只会让聊天框落后于实际对话。
	maxChatboxPending = 12

	// MaxInputHold 是移动类输入按住的绝对上限，设置里再怎么调也越不过它：
	// 按住不放的输入会让 Avatar 一直走，出了事故就是撞墙穿模、掉出世界。
	MaxInputHold     = 10 * time.Second
	DefaultInputHold = 3 * time.Second
	jumpPress        = 150 * time.Millisecond

	// explicitExpressionGuard 内心情不覆盖 Agent 刚明确设的表情。
	explicitExpressionGuard = 2 * time.Minute

	maxTrackedParams = 256
)

// Config 是桥的运行配置，由插件设置换算而来。
type Config struct {
	SendAddress     string
	ListenAddress   string // 空串表示不监听
	ChatboxInterval time.Duration
	ChatboxSound    bool
	InputMaxHold    time.Duration
	Expressions     ExpressionMap
}

// Sender 抽象出 OSC 发送端，测试里换成记录器。
type Sender interface {
	Send(osc.Message) error
}

type dialFunc func(address string) (Sender, func() error, error)

func dialUDP(address string) (Sender, func() error, error) {
	client, err := osc.Dial(address)
	if err != nil {
		return nil, nil, err
	}
	return client, client.Close, nil
}

type paramValue struct {
	value     any
	updatedAt time.Time
}

type chatItem struct {
	text  string
	first bool
}

// Bridge 是 OSC 桥本体。零值不可用，用 NewBridge 构造。
type Bridge struct {
	// lifecycle 串行化 Apply 和 Close：启停中途要临时放开 mu 等协程退出，
	// 两次启停交错会把套接字开关成一团。
	lifecycle sync.Mutex
	mu        sync.Mutex
	dial      dialFunc
	listen    func(string, osc.Handler) (*osc.Server, error)
	now       func() time.Time
	enabled   bool
	cfg       Config

	sender      Sender
	closeSender func() error
	server      *osc.Server
	listenErr   string
	lastErr     string

	// 聊天框队列由一个后台协程按间隔发出。
	pending   []chatItem
	wake      chan struct{}
	stop      chan struct{}
	done      chan struct{}
	lastChat  string
	lastChatT time.Time

	inputs map[string]*time.Timer

	expression       string
	expressionAt     time.Time
	expressionByMood bool
	revert           *time.Timer

	avatarID     string
	avatarAt     time.Time
	params       map[string]paramValue
	lastPacketAt time.Time
	lastPeer     string
}

// NewBridge 返回一个未启用的桥。
func NewBridge() *Bridge {
	return &Bridge{dial: dialUDP, listen: osc.Listen, now: time.Now}
}

// Apply 按开关和配置启停桥。重复用同一份配置调用是无操作，插件每次设置变化
// 或开关变化都可以放心地整份喂进来。
func (b *Bridge) Apply(enabled bool, cfg Config) error {
	cfg = normalizeConfig(cfg)
	b.lifecycle.Lock()
	defer b.lifecycle.Unlock()
	b.mu.Lock()
	if !enabled {
		b.shutdownLocked()
		b.mu.Unlock()
		return nil
	}
	var errs []error
	if b.sender == nil || cfg.SendAddress != b.cfg.SendAddress {
		b.closeSenderLocked()
		sender, closer, err := b.dial(cfg.SendAddress)
		if err != nil {
			errs = append(errs, err)
			b.lastErr = err.Error()
		} else {
			b.sender, b.closeSender = sender, closer
		}
	}
	if b.server == nil || cfg.ListenAddress != b.cfg.ListenAddress || !b.enabled {
		b.closeServerLocked()
		b.listenErr = ""
		if cfg.ListenAddress != "" {
			server, err := b.listen(cfg.ListenAddress, b.receive)
			if err != nil {
				// 端口被占（常见是别的 OSC 工具已经绑了 9001）不影响发送，照常能用。
				b.listenErr = err.Error()
				errs = append(errs, err)
			} else {
				b.server = server
			}
		}
	}
	b.cfg = cfg
	if !b.enabled {
		b.enabled = true
		b.wake = make(chan struct{}, 1)
		b.stop = make(chan struct{})
		b.done = make(chan struct{})
		wake, stop, done := b.wake, b.stop, b.done
		go func() {
			defer recoverPanic("vrchat.chatbox")
			b.chatboxLoop(wake, stop, done)
		}()
	}
	b.mu.Unlock()
	return errors.Join(errs...)
}

// Close 停用桥并归零所有按住的输入。
func (b *Bridge) Close() {
	b.lifecycle.Lock()
	defer b.lifecycle.Unlock()
	b.mu.Lock()
	b.shutdownLocked()
	b.mu.Unlock()
}

func normalizeConfig(cfg Config) Config {
	cfg.SendAddress = strings.TrimSpace(cfg.SendAddress)
	if cfg.SendAddress == "" {
		cfg.SendAddress = net.JoinHostPort(DefaultHost, strconv.Itoa(DefaultSendPort))
	}
	cfg.ListenAddress = strings.TrimSpace(cfg.ListenAddress)
	if cfg.ChatboxInterval < chatboxIntervalFloor {
		cfg.ChatboxInterval = chatboxIntervalFloor
	}
	if cfg.InputMaxHold <= 0 {
		cfg.InputMaxHold = DefaultInputHold
	}
	if cfg.InputMaxHold > MaxInputHold {
		cfg.InputMaxHold = MaxInputHold
	}
	return cfg
}

func (b *Bridge) shutdownLocked() {
	b.releaseInputsLocked()
	if b.revert != nil {
		b.revert.Stop()
		b.revert = nil
	}
	if b.enabled {
		close(b.stop)
		done := b.done
		// 等协程退出时要先放锁：它发最后一段时也要拿锁。
		b.mu.Unlock()
		<-done
		b.mu.Lock()
	}
	b.enabled = false
	b.pending = nil
	b.closeSenderLocked()
	b.closeServerLocked()
}

func (b *Bridge) closeSenderLocked() {
	if b.closeSender != nil {
		_ = b.closeSender()
	}
	b.sender, b.closeSender = nil, nil
}

func (b *Bridge) closeServerLocked() {
	if b.server == nil {
		return
	}
	server := b.server
	b.server = nil
	// Close 会等收包协程退出，而 receive 要拿锁，不放锁就会互等。
	b.mu.Unlock()
	_ = server.Close()
	b.mu.Lock()
}

// chatboxIntervalFloor 只在测试里调低，免得限速用例一跑就是好几秒。
var chatboxIntervalFloor = MinChatboxInterval

var ErrDisabled = errors.New("VRChat 联动没有启用")

func (b *Bridge) sendLocked(message osc.Message) error {
	if !b.enabled {
		return ErrDisabled
	}
	if b.sender == nil {
		return fmt.Errorf("OSC 发送端没有就绪：%s", b.lastErr)
	}
	if err := b.sender.Send(message); err != nil {
		b.lastErr = err.Error()
		return err
	}
	return nil
}

// ---- 聊天框 ----

// ChatboxResult 说明一次聊天框请求的排队情况。
type ChatboxResult struct {
	Segments int `json:"segments"`
	Dropped  int `json:"dropped,omitempty"`
	Pending  int `json:"pending"`
}

// Chatbox 把文字切段后排进聊天框队列。replace 为真时先清掉还没发的旧段，
// 用于「改口」：新话出来了，旧话再念就是在打岔。
func (b *Bridge) Chatbox(text string, replace bool) (ChatboxResult, error) {
	segments := SplitChatbox(text)
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.enabled {
		return ChatboxResult{}, ErrDisabled
	}
	if len(segments) == 0 {
		return ChatboxResult{Pending: len(b.pending)}, errors.New("聊天框内容为空")
	}
	if replace {
		b.pending = nil
	}
	result := ChatboxResult{}
	for i, segment := range segments {
		if len(b.pending) >= maxChatboxPending {
			result.Dropped = len(segments) - i
			break
		}
		b.pending = append(b.pending, chatItem{text: segment, first: i == 0})
		result.Segments++
	}
	result.Pending = len(b.pending)
	select {
	case b.wake <- struct{}{}:
	default:
	}
	return result, nil
}

// ClearChatbox 丢掉排队的段并清空聊天框。
func (b *Bridge) ClearChatbox() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pending = nil
	return b.sendLocked(osc.Message{Address: "/chatbox/input", Args: []any{"", true, false}})
}

// SetTyping 切换聊天框上方的「正在输入」指示。
func (b *Bridge) SetTyping(typing bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sendLocked(osc.Message{Address: "/chatbox/typing", Args: []any{typing}})
}

func (b *Bridge) chatboxLoop(wake <-chan struct{}, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	for {
		b.mu.Lock()
		if len(b.pending) == 0 {
			b.mu.Unlock()
			select {
			case <-wake:
				continue
			case <-stop:
				return
			}
		}
		wait := b.cfg.ChatboxInterval - b.now().Sub(b.lastChatT)
		b.mu.Unlock()
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-timer.C:
			case <-stop:
				timer.Stop()
				return
			}
		}
		b.mu.Lock()
		if len(b.pending) == 0 {
			b.mu.Unlock()
			continue
		}
		item := b.pending[0]
		b.pending = b.pending[1:]
		// immediate=true 跳过 VRChat 的键盘确认；提示音只在一条话的第一段响一次。
		sound := b.cfg.ChatboxSound && item.first
		_ = b.sendLocked(osc.Message{Address: "/chatbox/input", Args: []any{item.text, true, sound}})
		b.lastChat = item.text
		b.lastChatT = b.now()
		b.mu.Unlock()
	}
}

// ---- 表情 ----

// SetExpression 按映射表设置一个表情。hold 大于 0 时到点自动回到「平静」。
func (b *Bridge) SetExpression(name string, hold time.Duration) (Expression, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	expression, ok := b.cfg.Expressions.Lookup(name)
	if !ok {
		return Expression{}, fmt.Errorf("映射表里没有表情「%s」，可用：%s", name, strings.Join(b.cfg.Expressions.Names(), "、"))
	}
	if err := b.applyExpressionLocked(expression, false); err != nil {
		return Expression{}, err
	}
	if b.revert != nil {
		b.revert.Stop()
		b.revert = nil
	}
	if hold > 0 {
		neutral, ok := b.cfg.Expressions.Lookup(ExpressionNeutral)
		if ok && neutral.Name() != expression.Name() {
			var timer *time.Timer
			timer = time.AfterFunc(hold, func() {
				b.mu.Lock()
				defer b.mu.Unlock()
				if b.revert != timer {
					return
				}
				b.revert = nil
				_ = b.applyExpressionLocked(neutral, false)
			})
			b.revert = timer
		}
	}
	return expression, nil
}

// ApplyMood 让心情档位驱动表情。映射表没写对应条目、心情没变、或 Agent 刚明确
// 指定过表情时都不动——心情几小时才变一档，不该每条回复都把表情重置一遍。
func (b *Bridge) ApplyMood(label string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.enabled {
		return false
	}
	expression, ok := b.cfg.Expressions.Lookup(label)
	if !ok {
		return false
	}
	if b.expression == expression.Name() {
		return false
	}
	if !b.expressionByMood && !b.expressionAt.IsZero() && b.now().Sub(b.expressionAt) < explicitExpressionGuard {
		return false
	}
	return b.applyExpressionLocked(expression, true) == nil
}

func (b *Bridge) applyExpressionLocked(expression Expression, byMood bool) error {
	for _, param := range expression.Params {
		if err := b.sendLocked(osc.Message{Address: ParameterAddress(param.Name), Args: []any{param.Value}}); err != nil {
			return err
		}
	}
	b.expression = expression.Name()
	b.expressionAt = b.now()
	b.expressionByMood = byMood
	return nil
}

// ---- 移动与视角 ----

// actions 是开放给 Agent 的输入动作，只映射到 VRChat 的按键型 /input 地址。
// 语音开关、菜单这些不开放：模型不该能替人开麦。
var actions = map[string]string{
	"forward":    "MoveForward",
	"backward":   "MoveBackward",
	"left":       "MoveLeft",
	"right":      "MoveRight",
	"turn_left":  "LookLeft",
	"turn_right": "LookRight",
	"run":        "Run",
	"jump":       "Jump",
}

// ActionNames 返回可用动作，顺序稳定，工具 schema 用它生成枚举。
func ActionNames() []string {
	return []string{"forward", "backward", "left", "right", "turn_left", "turn_right", "run", "jump", "stop"}
}

// Input 按住一个动作一段时间后自动松开。时长被夹在配置上限以内；同一个动作
// 再次触发会重新计时而不是叠加。stop 立即松开全部。
func (b *Bridge) Input(action string, hold time.Duration) (time.Duration, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.enabled {
		return 0, ErrDisabled
	}
	if action == "stop" {
		b.releaseInputsLocked()
		return 0, nil
	}
	input, ok := actions[action]
	if !ok {
		return 0, fmt.Errorf("不支持的动作 %q", action)
	}
	switch {
	case action == "jump":
		// 跳是一次按键：按下立刻松开，按住不放只会原地连跳。
		hold = jumpPress
	case hold <= 0:
		hold = min(time.Second, b.cfg.InputMaxHold)
	case hold > b.cfg.InputMaxHold:
		hold = b.cfg.InputMaxHold
	}
	if err := b.sendLocked(osc.Message{Address: "/input/" + input, Args: []any{int32(1)}}); err != nil {
		return 0, err
	}
	if b.inputs == nil {
		b.inputs = map[string]*time.Timer{}
	}
	if previous := b.inputs[input]; previous != nil {
		previous.Stop()
	}
	var timer *time.Timer
	timer = time.AfterFunc(hold, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.inputs[input] != timer {
			return
		}
		delete(b.inputs, input)
		_ = b.sendLocked(osc.Message{Address: "/input/" + input, Args: []any{int32(0)}})
	})
	b.inputs[input] = timer
	return hold, nil
}

func (b *Bridge) releaseInputsLocked() {
	for input, timer := range b.inputs {
		timer.Stop()
		_ = b.sendLocked(osc.Message{Address: "/input/" + input, Args: []any{int32(0)}})
	}
	b.inputs = nil
}

// ---- 监听 ----

// 这些内置参数变化极频繁（速度、口型、音量），记下来只会把真正有意义的自定义
// 参数挤出状态摘要。
var noisyBuiltinParams = map[string]bool{
	"VelocityX": true, "VelocityY": true, "VelocityZ": true, "VelocityMagnitude": true,
	"AngularY": true, "Upright": true, "Viseme": true, "Voice": true,
	"GestureLeftWeight": true, "GestureRightWeight": true,
	"ScaleFactor": true, "ScaleFactorInverse": true, "EyeHeightAsMeters": true, "EyeHeightAsPercent": true,
}

// summaryBuiltinParams 是状态摘要里单列的内置参数：能回答「她在干嘛」。
var summaryBuiltinParams = []string{"AFK", "Seated", "InStation", "MuteSelf", "VRMode", "Grounded", "GestureLeft", "GestureRight"}

var builtinParams = map[string]bool{
	"IsLocal": true, "PreviewMode": true, "TrackingType": true, "IsOnFriendsList": true,
	"AvatarVersion": true, "IsAnimatorEnabled": true, "ScaleModified": true, "Earmuffs": true,
}

func init() {
	for _, name := range summaryBuiltinParams {
		builtinParams[name] = true
	}
	for name := range noisyBuiltinParams {
		builtinParams[name] = true
	}
}

func (b *Bridge) receive(message osc.Message, from *net.UDPAddr) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	b.lastPacketAt = now
	if from != nil {
		b.lastPeer = from.String()
	}
	switch {
	case message.Address == "/avatar/change":
		if len(message.Args) > 0 {
			if id, ok := message.Args[0].(string); ok {
				b.avatarID = id
				b.avatarAt = now
				// 换了 Avatar，旧参数全部作废；刚设的表情也不再成立。
				b.params = nil
				b.expression = ""
			}
		}
	case strings.HasPrefix(message.Address, "/avatar/parameters/"):
		name := strings.TrimPrefix(message.Address, "/avatar/parameters/")
		if name == "" || len(message.Args) != 1 || noisyBuiltinParams[name] {
			return
		}
		if b.params == nil {
			b.params = map[string]paramValue{}
		}
		if _, exists := b.params[name]; !exists && len(b.params) >= maxTrackedParams {
			b.evictOldestParamLocked()
		}
		b.params[name] = paramValue{value: message.Args[0], updatedAt: now}
	}
}

func (b *Bridge) evictOldestParamLocked() {
	oldest := ""
	var oldestAt time.Time
	for name, value := range b.params {
		if oldest == "" || value.updatedAt.Before(oldestAt) {
			oldest, oldestAt = name, value.updatedAt
		}
	}
	delete(b.params, oldest)
}

// ---- 状态 ----

// ParamStatus 是状态摘要里的一个参数。
type ParamStatus struct {
	Name      string    `json:"name"`
	Value     any       `json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Status 是桥的对外快照，WebUI 和 Agent 工具共用。
type Status struct {
	Enabled         bool           `json:"enabled"`
	SendAddress     string         `json:"send_address,omitempty"`
	ListenAddress   string         `json:"listen_address,omitempty"`
	Listening       bool           `json:"listening"`
	ListenError     string         `json:"listen_error,omitempty"`
	LastError       string         `json:"last_error,omitempty"`
	LastPacketAt    *time.Time     `json:"last_packet_at,omitempty"`
	LastPeer        string         `json:"last_peer,omitempty"`
	AvatarID        string         `json:"avatar_id,omitempty"`
	AvatarChangedAt *time.Time     `json:"avatar_changed_at,omitempty"`
	Builtin         map[string]any `json:"builtin,omitempty"`
	Params          []ParamStatus  `json:"params,omitempty"`
	Expression      string         `json:"expression,omitempty"`
	ExpressionAt    *time.Time     `json:"expression_at,omitempty"`
	ExpressionMood  bool           `json:"expression_by_mood,omitempty"`
	Expressions     []string       `json:"expressions,omitempty"`
	ChatboxPending  int            `json:"chatbox_pending"`
	LastChatbox     string         `json:"last_chatbox,omitempty"`
	LastChatboxAt   *time.Time     `json:"last_chatbox_at,omitempty"`
	ActiveInputs    []string       `json:"active_inputs,omitempty"`
}

// Status 返回当前快照。paramLimit 限制自定义参数条数，按最近更新排序。
func (b *Bridge) Status(paramLimit int) Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	status := Status{
		Enabled:         b.enabled,
		ListenError:     b.listenErr,
		LastError:       b.lastErr,
		LastPeer:        b.lastPeer,
		AvatarID:        b.avatarID,
		Expression:      b.expression,
		ExpressionMood:  b.expressionByMood,
		ChatboxPending:  len(b.pending),
		LastChatbox:     b.lastChat,
		LastPacketAt:    timePointer(b.lastPacketAt),
		AvatarChangedAt: timePointer(b.avatarAt),
		ExpressionAt:    timePointer(b.expressionAt),
		LastChatboxAt:   timePointer(b.lastChatT),
	}
	if b.enabled {
		status.SendAddress = b.cfg.SendAddress
		status.ListenAddress = b.cfg.ListenAddress
		status.Expressions = b.cfg.Expressions.Names()
	}
	if b.server != nil {
		status.Listening = true
		if addr := b.server.LocalAddr(); addr != nil {
			status.ListenAddress = addr.String()
		}
	}
	for _, name := range summaryBuiltinParams {
		if value, ok := b.params[name]; ok {
			if status.Builtin == nil {
				status.Builtin = map[string]any{}
			}
			status.Builtin[name] = value.value
		}
	}
	for name, value := range b.params {
		if builtinParams[name] {
			continue
		}
		status.Params = append(status.Params, ParamStatus{Name: name, Value: value.value, UpdatedAt: value.updatedAt})
	}
	sort.Slice(status.Params, func(i, j int) bool {
		if !status.Params[i].UpdatedAt.Equal(status.Params[j].UpdatedAt) {
			return status.Params[i].UpdatedAt.After(status.Params[j].UpdatedAt)
		}
		return status.Params[i].Name < status.Params[j].Name
	})
	if paramLimit > 0 && len(status.Params) > paramLimit {
		status.Params = status.Params[:paramLimit]
	}
	for input := range b.inputs {
		status.ActiveInputs = append(status.ActiveInputs, input)
	}
	sort.Strings(status.ActiveInputs)
	return status
}

func timePointer(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// recoverPanic 必须自己调 recover()：交给 safego.Recover 再转一层就接不住了。
func recoverPanic(component string) {
	safego.Report(component, recover())
}
