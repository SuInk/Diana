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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/SuInk/diana/internal/procgroup"
	"github.com/SuInk/diana/model/callbridge"
	"github.com/SuInk/diana/model/voicecall"
)

// 通话插件：主人给机器人打语音电话，机器人接起来实时对话。
//
// 平台不开放语音接口，只能让 SnowLuma 容器里真的客户端去接，再把声音从虚拟
// 声卡里掏出来。容器里的 call-agent 管窗口和声卡（cmd/call-agent），这个插件
// 连着它：来电时核对身份、决定接不接，接通后把声音交给和 WebUI 电话同一套通话
// 引擎（model/voicecall）。设计和部署见 docs/call-plugin.md。
//
// 私聊来电只接主人，群通话可选（见 call_invite.go）。私聊响铃时只看得到窗口上的昵称，账号要接通后才从 SnowLuma 日志里
// 读到。昵称靠 OCR，表情、特殊符号、被截断的长备注都认不准，拿它决定接不接会让主人
// 的电话经常没人接，所以来电一律接起来，接通后按 账号核对：不是主人、读不到都立刻
// 挂断，机器人不开口。代价是别人打来会被接通几秒再挂掉。
//
// 接不起来的原因（缺 ffmpeg、没配语音识别、令牌不对……）私聊告诉主人：这条消息进了
// 私聊记录，主人回头问「电话怎么没接」时机器人自己知道；bot_config 查这个插件也能
// 看到同一份状态（见 RuntimeStatus）。
const (
	callPluginID = "official.call-plugin"

	callPluginSettingAgentURL    = "agent_url"
	callPluginSettingAgentToken  = "agent_token"
	callPluginSettingProfile     = "profile"
	callPluginSettingAnswerDelay = "answer_delay"
	callPluginSettingMaxMinutes  = "max_minutes"
	callPluginSettingBargeIn     = "barge_in"
	callPluginSettingPrivate     = "answer_private"
	callPluginSettingGroup       = "answer_group"

	callPluginDialTimeout  = 10 * time.Second
	callPluginRetryMin     = 3 * time.Second
	callPluginRetryMax     = 30 * time.Second
	callPluginWriteTimeout = 10 * time.Second
	// callPluginNoticeEvery 是同一个问题私聊提醒主人的最短间隔：问题没修好时每来
	// 一通电话都会再碰到一次，不节流就是一通一条。
	callPluginNoticeEvery   = 6 * time.Hour
	callPluginNoticeTimeout = 30 * time.Second
)

// CallPlugin 持有到 call-agent 的唯一一条连接。
type CallPlugin struct {
	mu      sync.Mutex
	runtime *Runtime
	change  PluginStateChange
	// key 是当前连接用的地址和 token，变了才重连。
	key    string
	cancel context.CancelFunc
	status CallPluginStatus
	// calls 是当前这条 agent 连接上的通话集合，主动拨号（phone_call）要往里登记。
	calls *bridgeCallSet
	// noticed 记着每个问题上次私聊提醒主人的时间，见 tellOwner。
	noticed map[string]time.Time
	// notify 发私聊提醒，测试里换掉。
	notify func(ctx context.Context, runtime *Runtime, profileID, text string) error
}

// CallPluginStatus 是插件此刻的状态，排查用。
type CallPluginStatus struct {
	Connected bool   `json:"connected"`
	LastError string `json:"last_error,omitempty"`
	// LastCall 是最近一通电话的处理结果，给人看的一句话。
	LastCall   string    `json:"last_call,omitempty"`
	LastCallAt time.Time `json:"last_call_at,omitempty"`
}

func NewCallPlugin() *CallPlugin {
	return &CallPlugin{noticed: map[string]time.Time{}, notify: notifyCallPluginOwner}
}

func (p *CallPlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID:              callPluginID,
		Name:            "通话插件",
		Version:         "0.2.0",
		Description:     "主人给机器人打语音电话，机器人自动接听并实时对话，人设、记忆和私聊记录与私聊是同一份；也可以打开「接群语音通话」，群里有人邀请机器人加入群通话时它就加入，按那个群的身份聊。需要在 SnowLuma 容器里运行通话插件，并在模型分配里配好语音识别和语音合成。私聊来电都会接起来，接通后对方账号不是主人就立刻挂断；接不起来时机器人会私聊告诉主人原因。",
		Official:        true,
		BuiltIn:         true,
		DefaultDisabled: true,
		Platforms:       []string{PlatformOneBotV11},
		PlatformNotes: map[string]string{
			PlatformOneBotV11: "只支持 SnowLuma 容器里的客户端，需要在容器里部署通话插件（call-agent）。",
		},
		Permissions: []string{"network:websocket", "call:voice"},
		Settings: []PluginSettingSpec{
			{
				Key:         callPluginSettingAgentURL,
				Label:       "通话插件地址",
				Description: "SnowLuma 容器里通话插件的 WebSocket 地址，例如 ws://127.0.0.1:6190/ws；同一个 compose 里写 ws://snowluma:6190/ws。留空不连接。",
				Type:        PluginSettingTypeString,
				Default:     "",
				GlobalOnly:  true,
			},
			{
				Key:         callPluginSettingAgentToken,
				Label:       "通话插件令牌",
				Description: "和容器里的 CALL_AGENT_TOKEN 一致。留空时依次用 Diana 这边的 CALL_AGENT_TOKEN 环境变量、数据目录（数据库所在目录）里的 call-agent-token 文件、接电话那台机器人的 OneBot access token；容器里没设 CALL_AGENT_TOKEN 时 agent 也默认用 OneBot token，一般不用填。",
				Type:        PluginSettingTypeString,
				Default:     "",
				Secret:      true,
				GlobalOnly:  true,
			},
			{
				Key:         callPluginSettingProfile,
				Label:       "接电话的机器人",
				Description: "选容器里登录的那个账号对应的机器人。只有一台 OneBot 机器人时不用选。",
				Type:        PluginSettingTypeBotProfile,
				Default:     "",
				GlobalOnly:  true,
				// 只列 OneBot 机器人：电话是容器里的客户端接的。
				ProfilePlatforms: []string{PlatformOneBotV11},
			},
			{
				Key:         callPluginSettingPrivate,
				Label:       "接主人的私聊电话",
				Description: "主人私聊打来的语音电话。别人私聊打来会被接通几秒、核对 账号后挂断。",
				Type:        PluginSettingTypeBool,
				Default:     true,
				GlobalOnly:  true,
			},
			{
				Key:         callPluginSettingGroup,
				Label:       "接群语音通话",
				Description: "群里有人发起群语音通话并邀请机器人时加入，按那个群的身份聊，群通话里谁说话都会接。是哪个群由模型读来电窗口上的群名、对照机器人所在的群认出来，认不出就不接。",
				Type:        PluginSettingTypeBool,
				Default:     false,
				GlobalOnly:  true,
			},
			{
				Key:         callPluginSettingAnswerDelay,
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
				Key:         callPluginSettingMaxMinutes,
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
				Key:         callPluginSettingBargeIn,
				Label:       "允许打断",
				Description: "对方说话不用等机器人说完：这句转写出字后按新说的话生成回复，新回复出声时打断正在说的那一句；咳嗽、杂音转不出字就不打断。关掉后机器人想和说的时候对方的声音整段丢掉；虚拟声卡没有回声消除，对方外放、机器人老被自己的声音打断时再关。",
				Type:        PluginSettingTypeBool,
				Default:     true,
				GlobalOnly:  true,
			},
		},
	}
}

// Handle 不处理消息：电话从 call-agent 那边来。
func (p *CallPlugin) Handle(context.Context, PluginRequest) (*PluginResponse, error) {
	return nil, nil
}

func (p *CallPlugin) bindRuntime(runtime *Runtime) {
	p.mu.Lock()
	p.runtime = runtime
	p.mu.Unlock()
	p.apply()
}

// PluginStateChanged 跟着开关和设置连上或断开 call-agent。
func (p *CallPlugin) PluginStateChanged(change PluginStateChange) {
	p.mu.Lock()
	p.change = change
	p.mu.Unlock()
	p.apply()
}

// Status 返回连接和最近一通电话的状态。
func (p *CallPlugin) Status() CallPluginStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

func (p *CallPlugin) apply() {
	p.mu.Lock()
	defer p.mu.Unlock()
	url := strings.TrimSpace(p.change.Settings.String(callPluginSettingAgentURL, ""))
	token := strings.TrimSpace(p.change.Settings.String(callPluginSettingAgentToken, ""))
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
	p.calls = nil
	if key == "" {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	calls := newBridgeCallSet(p, nil)
	p.calls = calls
	go func() {
		defer recoverGoroutinePanic("call_plugin.loop")
		defer calls.closeAll()
		p.loop(ctx, url, token, calls)
	}()
}

// RuntimeStatus 报插件此刻能不能接电话、缺什么、最近一通怎么样。插件关着时不报。
func (p *CallPlugin) RuntimeStatus(ctx context.Context) (PluginRuntimeStatus, bool) {
	runtime, change := p.settings()
	status := p.Status()
	if runtime == nil || !change.Enabled {
		return PluginRuntimeStatus{}, false
	}
	var problems []string
	url := strings.TrimSpace(change.Settings.String(callPluginSettingAgentURL, ""))
	switch {
	case url == "":
		problems = append(problems, "还没填通话插件地址：同一个 compose 里写 ws://snowluma:6190/ws，分开部署写 ws://<SnowLuma 所在机器>:6190/ws")
	case !status.Connected:
		reason := "连不上通话插件（" + url + "）"
		if status.LastError != "" {
			reason += "：" + status.LastError
		}
		problems = append(problems, reason+"。确认 SnowLuma 容器换成了带语音组件的镜像、设了 CALL_AGENT_TOKEN，端口 6190 Diana 访问得到")
	}
	if ready := callPluginReadiness(ctx, runtime, change); ready.problem != "" {
		problems = append(problems, ready.problem)
	}
	out := PluginRuntimeStatus{Problems: problems, Recent: status.LastCall}
	if !status.LastCallAt.IsZero() {
		out.RecentAt = status.LastCallAt.Unix()
	}
	if len(problems) == 0 {
		out.Summary = "已连上通话插件，主人打来会自动接"
	} else {
		out.Summary = fmt.Sprintf("有 %d 个问题，电话接不起来", len(problems))
	}
	return out, true
}

// settings 取当前的设置和运行时，每通电话开始时读一次。
func (p *CallPlugin) settings() (*Runtime, PluginStateChange) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.runtime, p.change
}

func (p *CallPlugin) setStatus(fn func(*CallPluginStatus)) {
	p.mu.Lock()
	fn(&p.status)
	p.mu.Unlock()
}

func (p *CallPlugin) noteCall(format string, args ...any) {
	text := fmt.Sprintf(format, args...)
	log.Printf("diana: call plugin: %s", text)
	p.setStatus(func(s *CallPluginStatus) { s.LastCall, s.LastCallAt = text, time.Now() })
}

// tellOwner 私聊告诉主人电话为什么没接起来。同一个问题 callPluginNoticeEvery 内只说一次。
// 消息照常进私聊记录，主人回头问起时机器人从历史里就知道发生了什么。
func (p *CallPlugin) tellOwner(runtime *Runtime, profileID, key, text string) {
	if runtime == nil || strings.TrimSpace(profileID) == "" {
		return
	}
	p.mu.Lock()
	if last, ok := p.noticed[key]; ok && time.Since(last) < callPluginNoticeEvery {
		p.mu.Unlock()
		return
	}
	if p.noticed == nil {
		p.noticed = map[string]time.Time{}
	}
	p.noticed[key] = time.Now()
	notify := p.notify
	p.mu.Unlock()
	if notify == nil {
		return
	}
	go func() {
		defer recoverGoroutinePanic("call_plugin.notice")
		ctx, cancel := context.WithTimeout(context.Background(), callPluginNoticeTimeout)
		defer cancel()
		if err := notify(ctx, runtime, profileID, text); err != nil {
			log.Printf("diana: call plugin: 私聊提醒主人失败: %v", err)
		}
	}()
}

// notifyCallPluginOwner 往主人在平台上的私聊发一条提醒。ownerChatEvent 造的是 WebUI
// 聊天页的事件，去掉 webChat 标记才会真的发到平台。
func notifyCallPluginOwner(ctx context.Context, runtime *Runtime, profileID, text string) error {
	cfg, event, err := runtime.ownerChatEvent(profileID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(cfg.OwnerID) == "" {
		return errors.New("机器人还没填主人账号")
	}
	event.webChat = false
	event.Time = time.Now().Unix()
	return runtime.sendSubscriberNotice(ctx, event, "【通话插件】"+text)
}

// loop 保持到 agent 的连接，断了退避重连。
func (p *CallPlugin) loop(ctx context.Context, url, token string, calls *bridgeCallSet) {
	wait := callPluginRetryMin
	for ctx.Err() == nil {
		started := time.Now()
		err := p.run(ctx, url, token, calls)
		p.setStatus(func(s *CallPluginStatus) {
			s.Connected = false
			if err != nil {
				s.LastError = err.Error()
			}
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Printf("diana: call plugin: agent 连接断开: %v", err)
			if errors.Is(err, errBridgeCallAgentAuth) {
				if runtime, change := p.settings(); runtime != nil {
					if profileID, problem := callPluginProfile(runtime, change); problem == "" {
						p.tellOwner(runtime, profileID, "auth", "连不上容器里的通话插件："+err.Error()+"。改好之前你打来的电话都接不起来。令牌在插件页「通话插件」的设置里。")
					}
				}
			}
		}
		if time.Since(started) > time.Minute {
			wait = callPluginRetryMin
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, callPluginRetryMax)
	}
}

func (p *CallPlugin) run(ctx context.Context, url, token string, calls *bridgeCallSet) error {
	dialer := websocket.Dialer{HandshakeTimeout: callPluginDialTimeout}
	header := http.Header{}
	if token == "" {
		// 每次连之前现读：部署时后写进去的令牌文件，下一次重连就能用上，不用重启。
		token = bridgeCallTokenFallback()
	}
	if token == "" {
		token = p.oneBotTokenFallback()
	}
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	conn, resp, err := dialer.DialContext(ctx, url, header)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return errBridgeCallAgentAuth
		}
		return err
	}
	defer conn.Close()
	link := &bridgeCallLink{conn: conn}
	p.setStatus(func(s *CallPluginStatus) { s.Connected, s.LastError = true, "" })
	go func() {
		defer recoverGoroutinePanic("call_plugin.close")
		<-ctx.Done()
		_ = conn.Close()
	}()
	go func() {
		defer recoverGoroutinePanic("call_plugin.keepalive")
		link.keepAlive(ctx)
	}()

	// 通话不跟着这条连接走：Diana 和 agent 之间断一下（Diana 升级重启、网络抖动），
	// 客户端那头的电话还通着，连回来接着说，见 linkLost 和 hello 的处理。
	calls.setLink(link)
	defer calls.linkLost(link)
	defer calls.desktop.detach()
	go func() {
		defer recoverGoroutinePanic("call_plugin.desktop")
		runtime, _ := p.settings()
		calls.desktop.keepAttached(ctx, runtime)
	}()
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
			var msg callbridge.Message
			if json.Unmarshal(data, &msg) != nil {
				continue
			}
			calls.handle(ctx, msg)
		}
	}
}

// bridgeCallTokenFallback 是插件设置里没填令牌时的来源：先看环境变量 CALL_AGENT_TOKEN（同一个
// compose 里给 Diana 和 SnowLuma 配同一个值就行），再看数据目录里的 call-agent-token 文件
// （原生安装时部署脚本或运维写进去）。令牌是部署时生成的一串随机数，不该要人进 WebUI 抄一遍。
func bridgeCallTokenFallback() string {
	if token := strings.TrimSpace(os.Getenv("CALL_AGENT_TOKEN")); token != "" {
		return token
	}
	if dbPath := strings.TrimSpace(os.Getenv("APP_DB_PATH")); dbPath != "" {
		if data, err := os.ReadFile(filepath.Join(filepath.Dir(dbPath), bridgeCallTokenFileName)); err == nil {
			return strings.TrimSpace(string(data))
		}
	}
	return ""
}

const bridgeCallTokenFileName = "call-agent-token"

// oneBotTokenFallback 是最后的默认：接电话那台机器人的 OneBot access token。agent 没设
// CALL_AGENT_TOKEN 时也默认用 SnowLuma 的 OneBot token，两边不用另配就对得上。
func (p *CallPlugin) oneBotTokenFallback() string {
	runtime, change := p.settings()
	if runtime == nil {
		return ""
	}
	profileID, problem := callPluginProfile(runtime, change)
	if problem != "" {
		return ""
	}
	return strings.TrimSpace(runtime.profileConfig(profileID).WithDefaults().OneBotAccessToken)
}

var errBridgeCallAgentAuth = errors.New("令牌不对，和容器里的 CALL_AGENT_TOKEN 核对一下")

var errBridgeCallAgentGone = errors.New("和通话插件的连接断了")

// bridgeCallLink 串行写到 agent 的连接。
type bridgeCallLink struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

func (l *bridgeCallLink) send(msg callbridge.Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return l.write(websocket.TextMessage, data)
}

func (l *bridgeCallLink) write(kind int, data []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = l.conn.SetWriteDeadline(time.Now().Add(callPluginWriteTimeout))
	return l.conn.WriteMessage(kind, data)
}

func (l *bridgeCallLink) keepAlive(ctx context.Context) {
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

// bridgeCallSet 是一条 agent 连接上的电话。agent 同一时间只有一通，但响铃核对和
// 挂断是异步的，按 call ID 记着更稳。
type bridgeCallSet struct {
	plugin *CallPlugin

	mu sync.Mutex
	// link 是当前到 agent 的连接，断开期间为 nil。
	link *bridgeCallLink
	// linkGen 每换一次连接加一，断线计时到点时据此判断期间连回来过没有。
	linkGen int
	// resumeGrace 是断线后通话保留多久，测试里调短。
	resumeGrace time.Duration
	// answered 是我们决定接的电话：ID → 接电话的机器人和主人。只有它们接通后才开会话，
	// 别人在 noVNC 里手动接的电话不归机器人管。
	answered map[string]bridgeCallTarget
	active   *activeBridgeCall
	// intent 是 phone_call 登记的、接下来要拨出去的那通电话。
	intent *bridgeCallIntent
	// desktop 把 agent 那块屏幕接进桌面控制，见 call_desktop.go。
	desktop *callDesktopBridge
}

type bridgeCallTarget struct {
	profileID string
	ownerID   string
	// groupID 非空是群通话：接通后不核对账号（群通话没有那条推送），按群聊身份对话。
	groupID   string
	groupName string
	caller    string
	// dialed 是机器人自己拨出去的（phone_call 登记过）：不核对账号，接通后先开口。
	dialed bool
	reason string
}

type activeBridgeCall struct {
	id      string
	session *voicecall.Session
	cancel  context.CancelFunc
	call    *VoiceCall
	target  bridgeCallTarget
}

// describe 是给人看的「哪通电话」。
func (t bridgeCallTarget) describe() string {
	if t.groupID != "" {
		return "群「" + firstNonEmpty(t.groupName, t.groupID) + "」的群通话"
	}
	return "主人的电话"
}

// bridgeCallResumeGrace 是和 agent 断线后通话保留多久：Diana 升级重启一般十几秒就连回来。
const bridgeCallResumeGrace = 60 * time.Second

func newBridgeCallSet(plugin *CallPlugin, link *bridgeCallLink) *bridgeCallSet {
	set := &bridgeCallSet{plugin: plugin, link: link, resumeGrace: bridgeCallResumeGrace, answered: map[string]bridgeCallTarget{}}
	set.desktop = newCallDesktopBridge(set)
	return set
}

func (s *bridgeCallSet) currentLink() *bridgeCallLink {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.link
}

func (s *bridgeCallSet) send(msg callbridge.Message) error {
	link := s.currentLink()
	if link == nil {
		return errBridgeCallAgentGone
	}
	return link.send(msg)
}

func (s *bridgeCallSet) setLink(link *bridgeCallLink) {
	s.mu.Lock()
	s.link = link
	s.linkGen++
	s.mu.Unlock()
}

// linkLost 是到 agent 的连接断了。正在打的电话先留着，resumeGrace 内连回来就接着说；
// 到点还没连回来才挂掉这边的会话。
func (s *bridgeCallSet) linkLost(link *bridgeCallLink) {
	s.mu.Lock()
	if s.link != link {
		s.mu.Unlock()
		return
	}
	s.link = nil
	s.linkGen++
	gen := s.linkGen
	active := s.active != nil
	grace := s.resumeGrace
	s.mu.Unlock()
	if !active {
		return
	}
	s.plugin.noteCall("和通话插件断线，通话先留着等重连")
	time.AfterFunc(grace, func() {
		defer recoverGoroutinePanic("call_plugin.resume_grace")
		s.mu.Lock()
		expired := s.linkGen == gen
		s.mu.Unlock()
		if expired {
			s.plugin.noteCall("和通话插件断线超过 %s 没连回来，通话结束", grace)
			s.closeAll()
		}
	})
}

// resume 处理 agent 连上时报的「有一通已经接通的电话」。同一通就接着说；这边没有
// 这通（Diana 重启过）而对方是主人，就重新开会话接着聊。
func (s *bridgeCallSet) resume(ctx context.Context, msg callbridge.Message) {
	s.mu.Lock()
	active := s.active
	s.mu.Unlock()
	if active != nil && active.id == msg.Call {
		s.plugin.noteCall("通话插件连回来了，通话继续")
		return
	}
	if active != nil {
		s.ended(active.id)
	}
	runtime, change := s.plugin.settings()
	if runtime == nil {
		return
	}
	ready := callPluginReadiness(ctx, runtime, change)
	if ready.problem != "" || msg.PeerUIN == "" || msg.PeerUIN != ready.ownerID {
		// 不是主人的、认不出来的，都不是这边接的电话，不碰。
		return
	}
	s.mu.Lock()
	s.answered[msg.Call] = bridgeCallTarget{profileID: ready.profileID, ownerID: ready.ownerID}
	s.mu.Unlock()
	s.plugin.noteCall("接上了重连前主人那通电话")
	s.connected(ctx, msg)
}

func (s *bridgeCallSet) handle(ctx context.Context, msg callbridge.Message) {
	switch msg.Type {
	case callbridge.TypeHello:
		switch {
		case msg.State == callbridge.StateConnected && msg.Call != "":
			s.resume(ctx, msg)
		case msg.State == callbridge.StateRinging && msg.Call != "":
			s.endActiveExcept("")
			go func() {
				defer recoverGoroutinePanic("call_plugin.screen")
				s.screen(ctx, msg)
			}()
		default:
			// 断线期间那通电话已经挂了。
			s.endActiveExcept("")
		}
	case callbridge.TypeRinging:
		go func() {
			defer recoverGoroutinePanic("call_plugin.screen")
			s.screen(ctx, msg)
		}()
	case callbridge.TypeOutgoing:
		s.outgoing(msg)
	case callbridge.TypeConnected:
		s.connected(ctx, msg)
	case callbridge.TypeEnded:
		s.ended(msg.Call)
	case callbridge.TypeDesktopResult:
		s.desktop.deliver(msg)
	case callbridge.TypeError:
		s.plugin.noteCall("agent 报错: %s", msg.Error)
		if runtime, change := s.plugin.settings(); runtime != nil {
			if profileID, problem := callPluginProfile(runtime, change); problem == "" {
				s.plugin.tellOwner(runtime, profileID, "agent:"+msg.Error, "容器里的通话插件报错："+msg.Error+"。")
			}
		}
	}
}

// screen 决定接不接：前提都齐了就接。来电窗口上的昵称只用来记日志，不拿它挡电话，
// 身份在接通后按 账号核对（见 connected）。前提不齐时不接，并私聊告诉主人缺什么。
func (s *bridgeCallSet) screen(ctx context.Context, msg callbridge.Message) {
	defer recoverGoroutinePanic("call_plugin.screen")
	runtime, change := s.plugin.settings()
	if runtime == nil {
		return
	}
	ready := callPluginReadiness(ctx, runtime, change)
	if ready.problem != "" {
		s.plugin.noteCall("来电「%s」没接：%s", msg.Name, ready.problem)
		s.plugin.tellOwner(runtime, ready.profileID, "ready:"+ready.problem, "刚才有人打语音电话给我，我没接起来："+ready.problem+"。")
		return
	}
	var listGroups func(context.Context) ([]bridgeCallGroup, error)
	answerGroup := change.Settings.Bool(callPluginSettingGroup, false)
	if answerGroup {
		listGroups = runtime.bridgeCallGroupLister(ready.profileID)
	}
	judged := time.Now()
	invite := runtime.classifyBridgeCallInvite(ctx, ready.profileID, msg, listGroups)
	log.Printf("diana: call plugin: 来电判断（%s）：来电=%v 群通话=%v 来电人=%q 群=%s", invite.how, invite.incoming, invite.group, invite.caller, invite.groupID)
	target := bridgeCallTarget{profileID: ready.profileID, ownerID: ready.ownerID, caller: firstNonEmpty(invite.caller, msg.Name)}
	switch {
	case !invite.incoming:
		s.plugin.noteCall("「语音通话」窗口不是来电，不处理（%s）", invite.how)
		return
	case invite.group && !answerGroup:
		s.plugin.noteCall("「%s」邀请加入群通话，没接：插件设置里没打开「接群语音通话」", target.caller)
		return
	case invite.group && invite.groupID == "":
		s.plugin.noteCall("「%s」邀请加入群通话，没接：认不出是哪个群（%s）", target.caller, invite.how)
		return
	case invite.group:
		target.groupID, target.groupName = invite.groupID, invite.groupName
	case !change.Settings.Bool(callPluginSettingPrivate, true):
		s.plugin.noteCall("「%s」私聊来电，没接：插件设置里关了「接主人的私聊电话」", target.caller)
		return
	}
	// 判断来电已经响了一会儿，从「响铃多久后接听」里扣掉。
	delay := max(0, callPluginSeconds(change.Settings, callPluginSettingAnswerDelay, 2*time.Second)-time.Since(judged))
	select {
	case <-ctx.Done():
		return
	case <-time.After(delay):
	}
	s.mu.Lock()
	s.answered[msg.Call] = target
	s.mu.Unlock()
	if err := s.send(callbridge.Message{Type: callbridge.TypeAnswer, Call: msg.Call}); err != nil {
		s.plugin.noteCall("接听来电「%s」失败: %v", msg.Name, err)
	}
}

// callPluginReady 是接电话前要齐的东西。problem 非空时接不了，内容是给人看的原因，
// 带上怎么修。
type callPluginReady struct {
	profileID string
	ownerID   string
	problem   string
}

func callPluginReadiness(ctx context.Context, runtime *Runtime, change PluginStateChange) callPluginReady {
	profileID, problem := callPluginProfile(runtime, change)
	if problem != "" {
		return callPluginReady{problem: problem}
	}
	ready := callPluginReady{profileID: profileID}
	ready.ownerID = strings.TrimSpace(runtime.profileConfig(profileID).WithDefaults().OwnerID)
	if ready.ownerID == "" {
		ready.problem = "机器人还没填主人账号，认不出哪通是主人打的"
		return ready
	}
	if info := runtime.VoiceCallInfo(ctx, profileID); info.Problem != "" {
		ready.problem = info.Problem
		return ready
	}
	if _, err := lookResolverCommand("ffmpeg"); err != nil {
		ready.problem = "Diana 这台机器没装 ffmpeg，机器人说的话转不成电话里能播的声音。插件页「通话插件」的运行依赖里可以一键安装；Docker 部署换成完整版镜像"
	}
	return ready
}

// connected 是第二道核对：接通后读到的 账号必须是主人，读不到也挂。
func (s *bridgeCallSet) connected(ctx context.Context, msg callbridge.Message) {
	s.mu.Lock()
	target, ours := s.answered[msg.Call]
	s.mu.Unlock()
	if !ours {
		return
	}
	if target.groupID == "" && !target.dialed && msg.PeerUIN != target.ownerID {
		_ = s.send(callbridge.Message{Type: callbridge.TypeHangup, Call: msg.Call})
		if msg.PeerUIN != "" {
			// 别人打来的：这是正常的拦截，不打扰主人。
			s.plugin.noteCall("来电「%s」是 %s，不是主人，已挂断", msg.Name, msg.PeerUIN)
			return
		}
		s.plugin.noteCall("来电「%s」接通后读不到对方账号，已挂断", msg.Name)
		if runtime, _ := s.plugin.settings(); runtime != nil {
			s.plugin.tellOwner(runtime, target.profileID, "no-peer", "刚才接起来一通语音电话，但读不到对方的账号，认不出是不是你，只好挂了。多半是通话插件读不到 SnowLuma 的日志（容器里的 CALL_AGENT_LOG_DIR），或者 SnowLuma 升级后日志格式变了。")
		}
		return
	}
	runtime, change := s.plugin.settings()
	if runtime == nil {
		return
	}
	var call *VoiceCall
	var err error
	if target.groupID != "" {
		call, err = runtime.NewGroupVoiceCall(ctx, target.profileID, target.groupID, target.groupName, voiceCallMessagePrefix+"bridge-"+msg.Call)
	} else {
		call, err = runtime.NewVoiceCall(ctx, target.profileID, voiceCallMessagePrefix+"bridge-"+msg.Call)
	}
	if err != nil {
		s.plugin.noteCall("%s接通了，但开不了通话: %v", target.describe(), err)
		_ = s.send(callbridge.Message{Type: callbridge.TypeHangup, Call: msg.Call})
		s.plugin.tellOwner(runtime, target.profileID, "open:"+err.Error(), target.describe()+"接通了，但通话开不起来："+err.Error()+"，只好先挂了。")
		return
	}
	call.streamWAV = true
	callCtx, cancel := context.WithCancel(ctx)
	out := &bridgeCallTransport{ctx: callCtx, set: s, call: msg.Call, hangupDelay: bridgeCallHangupGrace}
	session := voicecall.NewSession(callCtx, call, out, voicecall.Options{CallID: msg.Call, BotID: target.profileID})
	bargeIn := change.Settings.Bool(callPluginSettingBargeIn, true)
	if err := session.HandleControl(voicecall.ClientMessage{
		Type:         voicecall.ClientStart,
		Format:       voicecall.FormatPCM16,
		SampleRate:   callbridge.SampleRate,
		Segmentation: voicecall.SegmentByVAD,
		BargeIn:      &bargeIn,
	}); err != nil {
		cancel()
		session.Close()
		call.Finish()
		s.plugin.noteCall("通话开始失败: %v", err)
		_ = s.send(callbridge.Message{Type: callbridge.TypeHangup, Call: msg.Call})
		return
	}
	s.mu.Lock()
	if s.active != nil {
		s.active.close()
	}
	s.active = &activeBridgeCall{id: msg.Call, session: session, cancel: cancel, call: call, target: target}
	s.mu.Unlock()
	s.plugin.noteCall("接通了%s", target.describe())
	if target.dialed {
		// 机器人打过去的：先开口说明来意。
		_ = session.HandleControl(voicecall.ClientMessage{Type: voicecall.ClientText, Text: bridgeCallOpener(target)})
	}

	maxDuration := time.Duration(change.Settings.Int(callPluginSettingMaxMinutes, 30)) * time.Minute
	go func() {
		defer recoverGoroutinePanic("call_plugin.max-duration")
		select {
		case <-callCtx.Done():
		case <-time.After(maxDuration):
			s.plugin.noteCall("通话到了 %s 上限，自动挂断", maxDuration)
			_ = s.send(callbridge.Message{Type: callbridge.TypeHangup, Call: msg.Call})
		}
	}()
}

func (s *bridgeCallSet) ended(id string) {
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
		s.plugin.noteCall("%s挂断了", active.target.describe())
	}
}

// endActiveExcept 结束不是 keep 的那通正在打的电话。
func (s *bridgeCallSet) endActiveExcept(keep string) {
	s.mu.Lock()
	active := s.active
	s.mu.Unlock()
	if active != nil && active.id != keep {
		s.ended(active.id)
	}
}

func (s *bridgeCallSet) audio(frame []byte) {
	s.mu.Lock()
	active := s.active
	s.mu.Unlock()
	if active != nil {
		active.session.HandleAudio(frame)
	}
}

func (s *bridgeCallSet) closeAll() {
	s.mu.Lock()
	active := s.active
	s.active = nil
	s.mu.Unlock()
	if active != nil {
		active.close()
	}
}

func (c *activeBridgeCall) close() {
	c.cancel()
	c.session.Close()
	// 最后一轮的话落进历史以后，再记这通电话打了多久。
	c.call.Finish()
}

// bridgeCallTransport 把通话引擎的下行交给 agent：一句合成好的音频收齐后转成 16kHz
// 裸 PCM 发过去；打断时让 agent 丢掉还没播的。字幕类消息 客户端那头用不上，丢掉。
type bridgeCallTransport struct {
	ctx context.Context
	// set 给出当前到 agent 的连接：断线重连后换了连接，通话照样往新连接上写。
	// 只有测试直接给 link。
	set  *bridgeCallSet
	link *bridgeCallLink
	// call 是 agent 那边的通话 ID：机器人自己挂断时要带着它让 agent 点挂断。
	call string
	mime string
	buf  bytes.Buffer
	// wav 非空时这一句是 WAV，收到一段就降采样交给 agent，见 call_stream.go。
	wav *callWAVStream
	// playUntil 估算 agent 那边已收到的声音要播到什么时候：一句 PCM 交过去是立刻
	// 返回的，真正播完还要这么久。机器人挂断时等它播完再让 agent 点挂断，否则告别
	// 那句还在缓冲里电话就断了。
	playUntil time.Time
	// hangupDelay 是播完之后再多等的余量，测试里设成 0。
	hangupDelay time.Duration
}

const bridgeCallHangupGrace = 600 * time.Millisecond

// bridgeCallHangupWaitCap 是等告别播完的上限：估算错了也不能让电话一直不挂。
const bridgeCallHangupWaitCap = 30 * time.Second

func (t *bridgeCallTransport) WriteJSON(msg voicecall.ServerMessage) error {
	switch msg.Type {
	case voicecall.ServerAudioStart:
		t.mime = msg.MIME
		t.buf.Reset()
		t.wav = nil
		if isWAVMIME(msg.MIME) {
			t.wav = &callWAVStream{}
		}
	case voicecall.ServerAudioEnd:
		if t.wav != nil {
			t.wav = nil
			return nil
		}
		audio := append([]byte(nil), t.buf.Bytes()...)
		t.buf.Reset()
		pcm, err := decodeCallSpeechToPCM(t.ctx, audio, t.mime)
		if err != nil {
			log.Printf("diana: call plugin: 合成的语音转 PCM 失败: %v", err)
			return nil
		}
		return t.play(pcm)
	case voicecall.ServerInterrupted:
		t.buf.Reset()
		t.wav = nil
		t.playUntil = time.Now()
		if link := t.currentLink(); link != nil {
			return link.send(callbridge.Message{Type: callbridge.TypeFlush})
		}
		return nil
	case voicecall.ServerHangup:
		// 告别那句已经交给 agent 了，等它播完再挂。
		wait := min(time.Until(t.playUntil)+t.hangupDelay, bridgeCallHangupWaitCap)
		call := t.call
		go func() {
			defer recoverGoroutinePanic("call_plugin.hangup")
			if wait > 0 {
				time.Sleep(wait)
			}
			link := t.currentLink()
			if link == nil {
				log.Printf("diana: call plugin: 挂断时和通话插件断着线")
				return
			}
			if err := link.send(callbridge.Message{Type: callbridge.TypeHangup, Call: call}); err != nil {
				log.Printf("diana: call plugin: 挂断失败: %v", err)
			}
		}()
		return nil
	case voicecall.ServerError:
		if msg.Fatal {
			log.Printf("diana: call plugin: 通话出错: %s", msg.Text)
		}
	}
	return nil
}

func (t *bridgeCallTransport) currentLink() *bridgeCallLink {
	if t.set != nil {
		return t.set.currentLink()
	}
	return t.link
}

func (t *bridgeCallTransport) WriteBinary(data []byte) error {
	if t.wav != nil {
		pcm, ok := t.wav.write(data)
		if ok {
			return t.play(pcm)
		}
		// 不是能直接解的 WAV：攒起来，整句收齐再交给 ffmpeg。
		t.buf.Write(t.wav.raw())
		t.wav = nil
		return nil
	}
	t.buf.Write(data)
	return nil
}

// play 把一段 16kHz PCM 交给 agent，顺带估算它要播到什么时候。
func (t *bridgeCallTransport) play(pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}
	now := time.Now()
	if t.playUntil.Before(now) {
		t.playUntil = now
	}
	t.playUntil = t.playUntil.Add(time.Duration(len(pcm)/2) * time.Second / time.Duration(callbridge.SampleRate))
	link := t.currentLink()
	if link == nil {
		// 断线期间这一段就丢了，连回来接着说。
		return nil
	}
	return link.write(websocket.BinaryMessage, pcm)
}

// decodeCallSpeechToPCM 用 ffmpeg 把一句合成的音频（mp3、wav、ogg……）转成 16kHz 单声道裸 PCM。
func decodeCallSpeechToPCM(ctx context.Context, audio []byte, mime string) ([]byte, error) {
	if len(audio) == 0 {
		return nil, errors.New("空音频")
	}
	ffmpeg, err := lookResolverCommand("ffmpeg")
	if err != nil {
		return nil, errors.New("没装 ffmpeg")
	}
	cmd := procgroup.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-i", "pipe:0",
		"-ac", "1", "-ar", fmt.Sprint(callbridge.SampleRate), "-f", "s16le", "pipe:1")
	cmd.Stdin = bytes.NewReader(audio)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: %w: %s", mime, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// callPluginProfile 选出接电话的机器人：设置里选的，否则启用了插件的第一台 OneBot 机器人。
func callPluginProfile(runtime *Runtime, change PluginStateChange) (string, string) {
	if id := strings.TrimSpace(change.Settings.String(callPluginSettingProfile, "")); id != "" {
		if !IsOneBotPlatform(runtime.profileConfig(id).WithDefaults().Platform) {
			return "", "插件设置里选的机器人「" + id + "」不是 OneBot 机器人，换成容器里登录的那个账号"
		}
		return id, ""
	}
	for _, id := range change.EnabledProfiles {
		if IsOneBotPlatform(runtime.profileConfig(id).WithDefaults().Platform) {
			return id, ""
		}
	}
	return "", "没有启用了这个插件的 OneBot 机器人，在插件设置里选「接电话的机器人」"
}

func callPluginSeconds(settings SettingValues, key string, fallback time.Duration) time.Duration {
	seconds, ok := numberValue(settings[key])
	if !ok || seconds < 0 {
		return fallback
	}
	return time.Duration(seconds * float64(time.Second))
}
