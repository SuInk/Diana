// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/SuInk/diana/internal/procgroup"
	"github.com/SuInk/diana/model/qqcall"
	"github.com/SuInk/diana/model/voicecall"
)

// QQ 语音通话：主人用 QQ 给机器人打语音电话，机器人接起来实时对话。
//
// QQ 不开放语音接口，只能让 SnowLuma 容器里真的 Linux QQ 去接，再把声音从虚拟
// 声卡里掏出来。容器里的 qqcall-agent 管窗口和声卡（cmd/qqcall-agent），这个插件
// 连着它：来电时核对身份、决定接不接，接通后把声音交给和 WebUI 电话同一套通话
// 引擎（model/voicecall）。设计和部署见 docs/qq-voice-call.md。
//
// 第一版只接主人。响铃时只看得到窗口上的昵称，QQ 号要接通后才从 SnowLuma 日志里
// 读到，所以核对两次：昵称对上主人的备注或昵称才接，接通后 QQ 号不是主人立刻挂断。
const (
	qqVoiceCallPluginID = "official.qq-voice-call"

	qqVoiceCallSettingAgentURL    = "agent_url"
	qqVoiceCallSettingAgentToken  = "agent_token"
	qqVoiceCallSettingProfile     = "profile"
	qqVoiceCallSettingAnswerDelay = "answer_delay"
	qqVoiceCallSettingMaxMinutes  = "max_minutes"
	qqVoiceCallSettingBargeIn     = "barge_in"

	qqVoiceCallDialTimeout  = 10 * time.Second
	qqVoiceCallRetryMin     = 3 * time.Second
	qqVoiceCallRetryMax     = 30 * time.Second
	qqVoiceCallWriteTimeout = 10 * time.Second
	qqVoiceCallNameTimeout  = 8 * time.Second
)

// QQVoiceCallPlugin 持有到 qqcall-agent 的唯一一条连接。
type QQVoiceCallPlugin struct {
	mu      sync.Mutex
	runtime *Runtime
	change  PluginStateChange
	// key 是当前连接用的地址和 token，变了才重连。
	key    string
	cancel context.CancelFunc
	status QQVoiceCallStatus
}

// QQVoiceCallStatus 是插件此刻的状态，排查用。
type QQVoiceCallStatus struct {
	Connected bool   `json:"connected"`
	LastError string `json:"last_error,omitempty"`
	// LastCall 是最近一通电话的处理结果，给人看的一句话。
	LastCall string `json:"last_call,omitempty"`
}

func NewQQVoiceCallPlugin() *QQVoiceCallPlugin {
	return &QQVoiceCallPlugin{}
}

func (p *QQVoiceCallPlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID:              qqVoiceCallPluginID,
		Name:            "QQ 语音通话",
		Version:         "0.1.0",
		Description:     "主人用 QQ 给机器人打语音电话，机器人自动接听并实时对话，人设、记忆和私聊记录与 QQ 私聊是同一份。需要在 SnowLuma 容器里运行 qqcall-agent，并在模型分配里配好语音识别和语音合成。第一版只接主人的电话。",
		Official:        true,
		BuiltIn:         true,
		DefaultDisabled: true,
		Platforms:       []string{PlatformOneBotV11},
		PlatformNotes: map[string]string{
			PlatformOneBotV11: "只支持 SnowLuma 容器里的 Linux QQ，需要部署 qqcall-agent。",
		},
		Permissions: []string{"network:websocket", "qq:voice_call"},
		Settings: []PluginSettingSpec{
			{
				Key:         qqVoiceCallSettingAgentURL,
				Label:       "qqcall-agent 地址",
				Description: "SnowLuma 容器里 qqcall-agent 的 WebSocket 地址，例如 ws://127.0.0.1:6190/ws；同一个 compose 里写 ws://snowluma:6190/ws。留空不连接。",
				Type:        PluginSettingTypeString,
				Default:     "",
				GlobalOnly:  true,
			},
			{
				Key:         qqVoiceCallSettingAgentToken,
				Label:       "qqcall-agent 令牌",
				Description: "和容器里的 QQCALL_TOKEN 环境变量一致。",
				Type:        PluginSettingTypeString,
				Default:     "",
				Secret:      true,
				GlobalOnly:  true,
			},
			{
				Key:         qqVoiceCallSettingProfile,
				Label:       "接电话的机器人",
				Description: "填机器人 ID，这台机器人的 QQ 号就是容器里登录的那个。留空时用启用了插件的机器人里 ID 排第一的 OneBot 机器人。",
				Type:        PluginSettingTypeString,
				Default:     "",
				GlobalOnly:  true,
			},
			{
				Key:         qqVoiceCallSettingAnswerDelay,
				Label:       "响铃多久后接听",
				Description: "核对完昵称后再等这么久才接，像人一样别秒接。",
				Type:        PluginSettingTypeNumber,
				Default:     2,
				Min:         settingRange(0),
				Max:         settingRange(20),
				Step:        0.5,
				Unit:        "秒",
				GlobalOnly:  true,
			},
			{
				Key:         qqVoiceCallSettingMaxMinutes,
				Label:       "单次通话最长",
				Description: "到点自动挂断，防止忘了挂一直占着语音识别和合成。",
				Type:        PluginSettingTypeNumber,
				Default:     30,
				Min:         settingRange(1),
				Max:         settingRange(180),
				Unit:        "分钟",
				GlobalOnly:  true,
			},
			{
				Key:         qqVoiceCallSettingBargeIn,
				Label:       "允许打断",
				Description: "机器人说话时对方开口就停下。虚拟声卡没有回声消除，对方外放时机器人自己的声音可能被 QQ 回传过来把自己打断；对方戴耳机时再打开。",
				Type:        PluginSettingTypeBool,
				Default:     false,
				GlobalOnly:  true,
			},
		},
	}
}

// Handle 不处理消息：电话从 qqcall-agent 那边来。
func (p *QQVoiceCallPlugin) Handle(context.Context, PluginRequest) (*PluginResponse, error) {
	return nil, nil
}

func (p *QQVoiceCallPlugin) bindRuntime(runtime *Runtime) {
	p.mu.Lock()
	p.runtime = runtime
	p.mu.Unlock()
	p.apply()
}

// PluginStateChanged 跟着开关和设置连上或断开 qqcall-agent。
func (p *QQVoiceCallPlugin) PluginStateChanged(change PluginStateChange) {
	p.mu.Lock()
	p.change = change
	p.mu.Unlock()
	p.apply()
}

// Status 返回连接和最近一通电话的状态。
func (p *QQVoiceCallPlugin) Status() QQVoiceCallStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

func (p *QQVoiceCallPlugin) apply() {
	p.mu.Lock()
	defer p.mu.Unlock()
	url := strings.TrimSpace(p.change.Settings.String(qqVoiceCallSettingAgentURL, ""))
	token := strings.TrimSpace(p.change.Settings.String(qqVoiceCallSettingAgentToken, ""))
	key := ""
	if p.change.Enabled && p.runtime != nil && url != "" {
		key = url + "\x00" + token
	}
	if key == p.key {
		return
	}
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	p.key = key
	p.status.Connected = false
	if key == "" {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	go func() {
		defer recoverGoroutinePanic("qq_voice_call.loop")
		p.loop(ctx, url, token)
	}()
}

// settings 取当前的设置和运行时，每通电话开始时读一次。
func (p *QQVoiceCallPlugin) settings() (*Runtime, PluginStateChange) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.runtime, p.change
}

func (p *QQVoiceCallPlugin) setStatus(fn func(*QQVoiceCallStatus)) {
	p.mu.Lock()
	fn(&p.status)
	p.mu.Unlock()
}

func (p *QQVoiceCallPlugin) noteCall(format string, args ...any) {
	text := fmt.Sprintf(format, args...)
	log.Printf("diana: qq voice call: %s", text)
	p.setStatus(func(s *QQVoiceCallStatus) { s.LastCall = text })
}

// loop 保持到 agent 的连接，断了退避重连。
func (p *QQVoiceCallPlugin) loop(ctx context.Context, url, token string) {
	wait := qqVoiceCallRetryMin
	for ctx.Err() == nil {
		started := time.Now()
		err := p.run(ctx, url, token)
		p.setStatus(func(s *QQVoiceCallStatus) {
			s.Connected = false
			if err != nil {
				s.LastError = err.Error()
			}
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Printf("diana: qq voice call: agent 连接断开: %v", err)
		}
		if time.Since(started) > time.Minute {
			wait = qqVoiceCallRetryMin
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, qqVoiceCallRetryMax)
	}
}

func (p *QQVoiceCallPlugin) run(ctx context.Context, url, token string) error {
	dialer := websocket.Dialer{HandshakeTimeout: qqVoiceCallDialTimeout}
	header := http.Header{}
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	conn, resp, err := dialer.DialContext(ctx, url, header)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return errors.New("令牌不对，和容器里的 QQCALL_TOKEN 核对一下")
		}
		return err
	}
	defer conn.Close()
	link := &qqCallLink{conn: conn}
	p.setStatus(func(s *QQVoiceCallStatus) { s.Connected, s.LastError = true, "" })
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	go link.keepAlive(ctx)

	calls := newQQCallSet(p, link)
	defer calls.closeAll()
	for {
		kind, data, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		switch kind {
		case websocket.BinaryMessage:
			calls.audio(data)
		case websocket.TextMessage:
			var msg qqcall.Message
			if json.Unmarshal(data, &msg) != nil {
				continue
			}
			calls.handle(ctx, msg)
		}
	}
}

// qqCallLink 串行写到 agent 的连接。
type qqCallLink struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

func (l *qqCallLink) send(msg qqcall.Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return l.write(websocket.TextMessage, data)
}

func (l *qqCallLink) write(kind int, data []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = l.conn.SetWriteDeadline(time.Now().Add(qqVoiceCallWriteTimeout))
	return l.conn.WriteMessage(kind, data)
}

func (l *qqCallLink) keepAlive(ctx context.Context) {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.mu.Lock()
			err := l.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
			l.mu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

// qqCallSet 是一条 agent 连接上的电话。agent 同一时间只有一通，但响铃核对和
// 挂断是异步的，按 call ID 记着更稳。
type qqCallSet struct {
	plugin *QQVoiceCallPlugin
	link   *qqCallLink

	mu sync.Mutex
	// answered 是我们决定接的电话：ID → 接电话的机器人和主人。只有它们接通后才开会话，
	// 别人在 noVNC 里手动接的电话不归机器人管。
	answered map[string]qqCallTarget
	active   *qqActiveCall
}

type qqCallTarget struct {
	profileID string
	ownerID   string
}

type qqActiveCall struct {
	id      string
	session *voicecall.Session
	cancel  context.CancelFunc
}

func newQQCallSet(plugin *QQVoiceCallPlugin, link *qqCallLink) *qqCallSet {
	return &qqCallSet{plugin: plugin, link: link, answered: map[string]qqCallTarget{}}
}

func (s *qqCallSet) handle(ctx context.Context, msg qqcall.Message) {
	switch msg.Type {
	case qqcall.TypeHello:
		if msg.State == qqcall.StateRinging && msg.Call != "" {
			go s.screen(ctx, msg)
		}
	case qqcall.TypeRinging:
		go s.screen(ctx, msg)
	case qqcall.TypeConnected:
		s.connected(ctx, msg)
	case qqcall.TypeEnded:
		s.ended(msg.Call)
	case qqcall.TypeError:
		s.plugin.noteCall("agent 报错: %s", msg.Error)
	}
}

// screen 是第一道核对：来电窗口上的昵称得是主人的备注或昵称。
func (s *qqCallSet) screen(ctx context.Context, msg qqcall.Message) {
	defer recoverGoroutinePanic("qq_voice_call.screen")
	runtime, change := s.plugin.settings()
	if runtime == nil {
		return
	}
	profileID, problem := qqVoiceCallProfile(runtime, change)
	if problem != "" {
		s.plugin.noteCall("来电「%s」没接：%s", msg.Name, problem)
		return
	}
	if info := runtime.VoiceCallInfo(ctx, profileID); info.Problem != "" {
		s.plugin.noteCall("来电「%s」没接：%s", msg.Name, info.Problem)
		return
	}
	ownerID := strings.TrimSpace(runtime.profileConfig(profileID).WithDefaults().OwnerID)
	if ownerID == "" {
		s.plugin.noteCall("来电「%s」没接：机器人还没填主人账号", msg.Name)
		return
	}
	nameCtx, cancel := context.WithTimeout(ctx, qqVoiceCallNameTimeout)
	names := runtime.qqOwnerNames(nameCtx, profileID, ownerID)
	cancel()
	if len(names) == 0 {
		s.plugin.noteCall("来电「%s」没接：问不到主人的 QQ 昵称", msg.Name)
		return
	}
	if !qqcall.NameMatches(msg.Name, names...) {
		s.plugin.noteCall("来电「%s」不是主人，没接", msg.Name)
		return
	}
	delay := qqVoiceCallSeconds(change.Settings, qqVoiceCallSettingAnswerDelay, 2*time.Second)
	select {
	case <-ctx.Done():
		return
	case <-time.After(delay):
	}
	s.mu.Lock()
	s.answered[msg.Call] = qqCallTarget{profileID: profileID, ownerID: ownerID}
	s.mu.Unlock()
	if err := s.link.send(qqcall.Message{Type: qqcall.TypeAnswer, Call: msg.Call}); err != nil {
		s.plugin.noteCall("接听主人的来电失败: %v", err)
	}
}

// connected 是第二道核对：接通后读到的 QQ 号必须是主人，读不到也挂。
func (s *qqCallSet) connected(ctx context.Context, msg qqcall.Message) {
	s.mu.Lock()
	target, ours := s.answered[msg.Call]
	s.mu.Unlock()
	if !ours {
		return
	}
	if msg.PeerUIN != target.ownerID {
		reason := "对方 QQ 号不是主人"
		if msg.PeerUIN == "" {
			reason = "接通后读不到对方 QQ 号"
		}
		s.plugin.noteCall("来电「%s」%s，已挂断", msg.Name, reason)
		_ = s.link.send(qqcall.Message{Type: qqcall.TypeHangup, Call: msg.Call})
		return
	}
	runtime, change := s.plugin.settings()
	if runtime == nil {
		return
	}
	call, err := runtime.NewVoiceCall(ctx, target.profileID, voiceCallMessagePrefix+"qq-"+msg.Call)
	if err != nil {
		s.plugin.noteCall("主人的电话接通了，但开不了通话: %v", err)
		_ = s.link.send(qqcall.Message{Type: qqcall.TypeHangup, Call: msg.Call})
		return
	}
	callCtx, cancel := context.WithCancel(ctx)
	out := &qqCallTransport{ctx: callCtx, link: s.link}
	session := voicecall.NewSession(callCtx, call, out, voicecall.Options{CallID: msg.Call, BotID: target.profileID})
	bargeIn := change.Settings.Bool(qqVoiceCallSettingBargeIn, false)
	if err := session.HandleControl(voicecall.ClientMessage{
		Type:         voicecall.ClientStart,
		Format:       voicecall.FormatPCM16,
		SampleRate:   qqcall.SampleRate,
		Segmentation: voicecall.SegmentByVAD,
		BargeIn:      &bargeIn,
	}); err != nil {
		cancel()
		session.Close()
		s.plugin.noteCall("通话开始失败: %v", err)
		_ = s.link.send(qqcall.Message{Type: qqcall.TypeHangup, Call: msg.Call})
		return
	}
	s.mu.Lock()
	if s.active != nil {
		s.active.close()
	}
	s.active = &qqActiveCall{id: msg.Call, session: session, cancel: cancel}
	s.mu.Unlock()
	s.plugin.noteCall("接通了主人的电话")

	maxDuration := time.Duration(change.Settings.Int(qqVoiceCallSettingMaxMinutes, 30)) * time.Minute
	go func() {
		select {
		case <-callCtx.Done():
		case <-time.After(maxDuration):
			s.plugin.noteCall("通话到了 %s 上限，自动挂断", maxDuration)
			_ = s.link.send(qqcall.Message{Type: qqcall.TypeHangup, Call: msg.Call})
		}
	}()
}

func (s *qqCallSet) ended(id string) {
	s.mu.Lock()
	delete(s.answered, id)
	active := s.active
	if active != nil && active.id == id {
		s.active = nil
	} else {
		active = nil
	}
	s.mu.Unlock()
	if active != nil {
		active.close()
		s.plugin.noteCall("主人的电话挂断了")
	}
}

func (s *qqCallSet) audio(frame []byte) {
	s.mu.Lock()
	active := s.active
	s.mu.Unlock()
	if active != nil {
		active.session.HandleAudio(frame)
	}
}

func (s *qqCallSet) closeAll() {
	s.mu.Lock()
	active := s.active
	s.active = nil
	s.mu.Unlock()
	if active != nil {
		active.close()
	}
}

func (c *qqActiveCall) close() {
	c.cancel()
	c.session.Close()
}

// qqCallTransport 把通话引擎的下行交给 agent：一句合成好的音频收齐后转成 16kHz
// 裸 PCM 发过去；打断时让 agent 丢掉还没播的。字幕类消息 QQ 那头用不上，丢掉。
type qqCallTransport struct {
	ctx  context.Context
	link *qqCallLink
	mime string
	buf  bytes.Buffer
}

func (t *qqCallTransport) WriteJSON(msg voicecall.ServerMessage) error {
	switch msg.Type {
	case voicecall.ServerAudioStart:
		t.mime = msg.MIME
		t.buf.Reset()
	case voicecall.ServerAudioEnd:
		audio := append([]byte(nil), t.buf.Bytes()...)
		t.buf.Reset()
		pcm, err := decodeCallSpeechToPCM(t.ctx, audio, t.mime)
		if err != nil {
			log.Printf("diana: qq voice call: 合成的语音转 PCM 失败: %v", err)
			return nil
		}
		return t.link.write(websocket.BinaryMessage, pcm)
	case voicecall.ServerInterrupted:
		t.buf.Reset()
		return t.link.send(qqcall.Message{Type: qqcall.TypeFlush})
	case voicecall.ServerError:
		if msg.Fatal {
			log.Printf("diana: qq voice call: 通话出错: %s", msg.Text)
		}
	}
	return nil
}

func (t *qqCallTransport) WriteBinary(data []byte) error {
	t.buf.Write(data)
	return nil
}

// decodeCallSpeechToPCM 用 ffmpeg 把一句合成的音频（mp3、wav、ogg……）转成 16kHz 单声道裸 PCM。
func decodeCallSpeechToPCM(ctx context.Context, audio []byte, mime string) ([]byte, error) {
	if len(audio) == 0 {
		return nil, errors.New("空音频")
	}
	cmd := procgroup.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-i", "pipe:0",
		"-ac", "1", "-ar", fmt.Sprint(qqcall.SampleRate), "-f", "s16le", "pipe:1")
	cmd.Stdin = bytes.NewReader(audio)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: %w: %s", mime, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// qqVoiceCallProfile 选出接电话的机器人：设置里指定的，否则启用了插件的第一台 OneBot 机器人。
func qqVoiceCallProfile(runtime *Runtime, change PluginStateChange) (string, string) {
	if id := strings.TrimSpace(change.Settings.String(qqVoiceCallSettingProfile, "")); id != "" {
		if !IsOneBotPlatform(runtime.profileConfig(id).WithDefaults().Platform) {
			return "", "指定的机器人「" + id + "」不是 OneBot 机器人"
		}
		return id, ""
	}
	for _, id := range change.EnabledProfiles {
		if IsOneBotPlatform(runtime.profileConfig(id).WithDefaults().Platform) {
			return id, ""
		}
	}
	return "", "没有启用了插件的 OneBot 机器人，在插件设置里填「接电话的机器人」"
}

// qqOwnerNames 问出主人在 QQ 上可能显示的名字：好友备注（来电窗口优先显示它）和昵称。
func (r *Runtime) qqOwnerNames(ctx context.Context, profileID, ownerID string) []string {
	_, event, err := r.ownerChatEvent(profileID)
	if err != nil {
		return nil
	}
	data, err := r.callOneBotAPIForEvent(ctx, event, "get_friend_list", map[string]any{"no_cache": true})
	if err != nil {
		return nil
	}
	items, _ := structuredGroupListItems(data)
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok || strings.TrimSpace(stringFromAny(item["user_id"])) != ownerID {
			continue
		}
		var names []string
		for _, key := range []string{"remark", "nickname"} {
			if name := strings.TrimSpace(stringFromAny(item[key])); name != "" {
				names = append(names, name)
			}
		}
		return names
	}
	return nil
}

func qqVoiceCallSeconds(settings SettingValues, key string, fallback time.Duration) time.Duration {
	seconds, ok := numberValue(settings[key])
	if !ok || seconds < 0 {
		return fallback
	}
	return time.Duration(seconds * float64(time.Second))
}
