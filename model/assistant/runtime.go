package assistant

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/llm"

	"github.com/google/uuid"
)

type LLMProvider interface {
	Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error)
}

type LLMProviderFactory func() (LLMProvider, error)

type LLMProviderConfigFactory func(llm.ProviderConfig) (LLMProvider, error)

type LLMProfileStore interface {
	Current() llm.ProviderConfig
	Profiles() llm.ProfileSet
	SaveProfiles(llm.ProfileSet)
}

type LLMModelLister func(context.Context, llm.ProviderConfig) ([]llm.ModelInfo, error)

type ReminderStore interface {
	Reminders() []Reminder
	SaveReminders([]Reminder)
}

type GroupConfigStore interface {
	ConfigForGroup(groupID string) (GroupConfig, bool)
}

type ConfigSaver interface {
	SaveBotConfig(BotConfig)
}

type RuntimeStatus struct {
	Running       bool                `json:"running"`
	Config        ConfigPayload       `json:"config"`
	Channel       ChannelStatus       `json:"channel"`
	NoneBotBridge NoneBotBridgeStatus `json:"nonebot_bridge"`
	Plugins       []PluginState       `json:"plugins"`
	RecentEvents  []EventRecord       `json:"recent_events,omitempty"`
	ActiveWorkers int                 `json:"active_workers"`
	LastError     string              `json:"last_error,omitempty"`
	UpdatedAt     time.Time           `json:"updated_at"`
}

type EventRecord struct {
	At       time.Time `json:"at"`
	Kind     EventKind `json:"kind"`
	UserID   string    `json:"user_id,omitempty"`
	GroupID  string    `json:"group_id,omitempty"`
	Text     string    `json:"text,omitempty"`
	Reply    string    `json:"reply,omitempty"`
	Error    string    `json:"error,omitempty"`
	Handled  bool      `json:"handled"`
	Duration int64     `json:"duration_ms,omitempty"`
}

// EventListener 在运行时记录事件后被调用，用于统计和实时推送。
type EventListener func(EventRecord)

// PrivateMessageInterceptor 可在普通插件/LLM 流程之前消费私聊消息。
// 返回 true 表示消息已处理，不再进入聊天回复链路。
type PrivateMessageInterceptor func(context.Context, MessageEvent, string) bool

type Runtime struct {
	mu                        sync.RWMutex
	cfg                       BotConfig
	channel                   Channel
	bridge                    *NoneBotBridge
	plugins                   *PluginManager
	llmStore                  LLMProfileStore
	modelLister               LLMModelLister
	appLogs                   applog.Writer
	localMedia                LocalMediaSharer
	reminders                 ReminderStore
	groupConfigs              GroupConfigStore
	configSaver               ConfigSaver
	llmFactory                LLMProviderFactory
	llmCfgFactory             LLMProviderConfigFactory
	cancel                    context.CancelFunc
	running                   bool
	lastError                 string
	updatedAt                 time.Time
	eventListener             EventListener
	privateMessageInterceptor PrivateMessageInterceptor

	// members 缓存群成员的群等级与身份，供准入判定使用。
	members *memberCache
	// media 把入站图片下载到本地持久化，后续处理统一读本地文件。
	media *MediaStore
	// now 供测试注入时钟，nil 时用 time.Now。
	now func() time.Time
	// quietNotices 记录各会话上次发静默提示的时间，用于限频。
	quietNotices map[string]time.Time

	// sem 控制同时生成回复的 worker 数，history/recent 支撑上下文和状态页展示。
	sem      chan struct{}
	history  map[string][]historyEntry
	recent   []EventRecord
	activeMu sync.Mutex
	active   int
}

// historyEntry 是会话历史里的一条记录；botReply 非空表示这是机器人自己的回复。
type historyEntry struct {
	event    MessageEvent
	botReply string
}

// SetLLMProviderConfigFactory 注入按 profile 配置创建 LLM provider 的工厂。
func (r *Runtime) SetLLMProviderConfigFactory(factory LLMProviderConfigFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.llmCfgFactory = factory
}

// SetGroupConfigStore 注入群级配置存储，运行时会按消息所在群合并群配置。
func (r *Runtime) SetGroupConfigStore(store GroupConfigStore) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.groupConfigs = store
}

// SetEventListener 注入事件监听器；每条事件记录后会在独立 goroutine 中回调，
// 监听器不能阻塞也不会影响消息处理主流程。
func (r *Runtime) SetEventListener(listener EventListener) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.eventListener = listener
}

// SetPrivateMessageInterceptor 注入私聊消息拦截器，用于主人登录配对等控制面指令。
func (r *Runtime) SetPrivateMessageInterceptor(interceptor PrivateMessageInterceptor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.privateMessageInterceptor = interceptor
}

// NewRuntime 创建 QQ 机器人运行时。
func NewRuntime(cfg BotConfig, channel Channel, plugins *PluginManager, llmStore LLMProfileStore, reminders ReminderStore, configSaver ConfigSaver, llmFactory LLMProviderFactory) *Runtime {
	cfg = cfg.WithDefaults()
	if plugins == nil {
		plugins = NewDefaultPluginManager()
	}
	rt := &Runtime{
		cfg:         cfg,
		channel:     channel,
		bridge:      NewNoneBotBridge(bridgeConfigFromBotConfig(cfg), channel),
		plugins:     plugins,
		llmStore:    llmStore,
		modelLister: defaultLLMModelLister,
		reminders:   reminders,
		configSaver: configSaver,
		llmFactory:  llmFactory,
		updatedAt:   time.Now(),
		sem:         make(chan struct{}, cfg.MaxBotConcurrency),
		history:     map[string][]historyEntry{},
	}
	// 兜底查询走 Runtime 自己的 CallOneBotAPI，这样换 channel 后依然有效。
	rt.members = newMemberCache(rt.CallOneBotAPI)
	return rt
}

// SetLLMModelLister 注入运行时使用的模型列表读取器。
func (r *Runtime) SetLLMModelLister(lister LLMModelLister) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if lister == nil {
		r.modelLister = defaultLLMModelLister
		return
	}
	r.modelLister = lister
}

// llmModelLister 返回当前模型列表读取器。
func (r *Runtime) llmModelLister() LLMModelLister {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.modelLister == nil {
		return defaultLLMModelLister
	}
	return r.modelLister
}

// SetAppLogWriter 注入运行时审计日志写入器。
func (r *Runtime) SetAppLogWriter(writer applog.Writer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.appLogs = writer
}

// SetLocalMediaSharer exposes downloaded media through short-lived URLs that
// a separately deployed OneBot implementation can fetch.
func (r *Runtime) SetLocalMediaSharer(sharer LocalMediaSharer) {
	r.mu.Lock()
	r.localMedia = sharer
	r.mu.Unlock()
}

// appLogWriter 返回当前审计日志写入器。
func (r *Runtime) appLogWriter() applog.Writer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.appLogs
}

// Start 启动 QQ 机器人运行时。
func (r *Runtime) Start(parent context.Context) error {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return nil
	}
	cfg := r.cfg.WithDefaults()
	if !cfg.Enabled {
		r.mu.Unlock()
		return ErrBotDisabled
	}
	if err := cfg.Validate(); err != nil {
		r.mu.Unlock()
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	r.cancel = cancel
	r.running = true
	r.lastError = ""
	r.updatedAt = time.Now()
	// 配置里的最大并发数可能变更，启动时重建 semaphore 才能立即生效。
	r.sem = make(chan struct{}, cfg.MaxBotConcurrency)
	r.mu.Unlock()

	go func() {
		// 提醒循环、NoneBot 桥接和 OneBot 主连接共享同一个启动生命周期。
		go r.runReminderLoop(ctx)
		r.bridge.Start(ctx)
		err := r.channel.Connect(ctx, r.HandleEvent)
		if err != nil && ctx.Err() == nil {
			r.setError(err.Error())
			log.Printf("qqbot runtime stopped: %v", err)
		}
		r.mu.Lock()
		r.running = false
		r.updatedAt = time.Now()
		r.mu.Unlock()
	}()
	return nil
}

// Stop 停止 QQ 机器人运行时并关闭连接。
func (r *Runtime) Stop() error {
	r.mu.Lock()
	cancel := r.cancel
	r.cancel = nil
	r.running = false
	r.updatedAt = time.Now()
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if r.bridge != nil {
		r.bridge.Stop()
	}
	// 先取消 context 再关闭 channel，Connect/readLoop 会尽快从阻塞读里退出。
	return r.channel.Close()
}

// Restart 使用新配置和 channel 重启运行时。
func (r *Runtime) Restart(ctx context.Context, cfg BotConfig, channel Channel) error {
	_ = r.Stop()
	r.mu.Lock()
	r.cfg = cfg.WithDefaults()
	r.channel = channel
	r.mu.Unlock()
	return r.Start(ctx)
}

// UpdateConfig 更新运行时配置并按需重启。
func (r *Runtime) UpdateConfig(ctx context.Context, cfg BotConfig, channel Channel) error {
	cfg = cfg.WithDefaults()
	r.mu.Lock()
	wasRunning := r.running
	r.mu.Unlock()

	if wasRunning {
		// 运行中修改 WebSocket/token 等连接参数时，先停掉旧连接再替换配置。
		_ = r.Stop()
	}

	r.mu.Lock()
	r.cfg = cfg.WithDefaults()
	r.updatedAt = time.Now()
	if channel != nil {
		r.channel = channel
		if r.bridge != nil {
			r.bridge.UpdateConfig(bridgeConfigFromBotConfig(cfg), channel)
		}
	} else if r.bridge != nil {
		r.bridge.UpdateConfig(bridgeConfigFromBotConfig(cfg), r.channel)
	}
	r.mu.Unlock()
	if !wasRunning || !cfg.Enabled {
		return nil
	}
	// 只有原本正在运行且新配置仍启用时才自动重启，避免保存禁用配置又拉起机器人。
	return r.Start(ctx)
}

// Config 返回当前机器人配置。
func (r *Runtime) Config() BotConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg
}

// CallOneBotAPI 通过当前 OneBot channel 调用原生 API。
func (r *Runtime) CallOneBotAPI(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	action = strings.TrimSpace(action)
	if action == "" {
		return nil, fmt.Errorf("qqbot: onebot action is required")
	}
	r.mu.RLock()
	channel := r.channel
	r.mu.RUnlock()
	if channel == nil {
		return nil, fmt.Errorf("qqbot: channel is not configured")
	}
	return channel.CallAPI(ctx, action, params)
}

// SendGroupMessage 通过当前 OneBot channel 向指定 QQ 群发送管理端测试消息。
func (r *Runtime) SendGroupMessage(ctx context.Context, groupID string, text string) (map[string]any, error) {
	groupID = strings.TrimSpace(groupID)
	text = strings.TrimSpace(text)
	if groupID == "" {
		return nil, fmt.Errorf("qqbot: group id is required")
	}
	if text == "" {
		return nil, fmt.Errorf("qqbot: message is required")
	}
	parsedGroupID, err := strconv.ParseInt(groupID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("qqbot: invalid group id %q", groupID)
	}
	return r.CallOneBotAPI(ctx, "send_group_msg", map[string]any{
		"group_id": parsedGroupID,
		"message":  buildOutgoingSegments(OutgoingMessage{Text: text}),
	})
}

// Plugins 返回插件管理器。
func (r *Runtime) Plugins() *PluginManager {
	return r.plugins
}

// Status 返回机器人运行时状态快照。
func (r *Runtime) Status() RuntimeStatus {
	r.mu.RLock()
	cfg := r.cfg
	running := r.running
	lastError := r.lastError
	updatedAt := r.updatedAt
	channel := r.channel
	recent := append([]EventRecord(nil), r.recent...)
	r.mu.RUnlock()
	channelStatus := channel.Status()
	if cfg.BotQQ == "" && channelStatus.SelfID != "" {
		cfg = r.rememberBotQQ(channelStatus.SelfID)
	}

	return RuntimeStatus{
		Running:       running,
		Config:        PayloadFromConfig(cfg),
		Channel:       channelStatus,
		NoneBotBridge: r.bridge.Status(),
		Plugins:       r.plugins.List(),
		RecentEvents:  recent,
		ActiveWorkers: r.activeCount(),
		LastError:     lastError,
		UpdatedAt:     updatedAt,
	}
}

// rememberBotQQ records the account reported by OneBot after the first connection.
func (r *Runtime) rememberBotQQ(selfID string) BotConfig {
	selfID = strings.TrimSpace(selfID)
	if selfID == "" {
		return r.Config()
	}
	r.mu.Lock()
	if r.cfg.BotQQ != "" {
		cfg := r.cfg
		r.mu.Unlock()
		return cfg
	}
	r.cfg.BotQQ = selfID
	r.updatedAt = time.Now()
	cfg := r.cfg
	saver := r.configSaver
	r.mu.Unlock()
	if saver != nil {
		saver.SaveBotConfig(cfg)
	}
	return cfg
}

func (r *Runtime) effectiveConfigForEvent(event MessageEvent) BotConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.effectiveConfigForEventLocked(event)
}

func (r *Runtime) effectiveConfigForEventLocked(event MessageEvent) BotConfig {
	cfg := r.cfg.WithDefaults()
	if event.Kind != EventKindGroup || strings.TrimSpace(event.GroupID) == "" || r.groupConfigs == nil {
		return cfg
	}
	groupCfg, ok := r.groupConfigs.ConfigForGroup(event.GroupID)
	if !ok {
		return cfg
	}
	groupCfg = groupCfg.WithDefaults(event.GroupID, cfg)
	cfg.GroupTriggers = append([]string(nil), groupCfg.GroupTriggers...)
	cfg.WelcomeEnabled = groupCfg.WelcomeEnabled
	cfg.WelcomeMessage = groupCfg.WelcomeMessage
	cfg.RecentContextLimit = groupCfg.RecentContextLimit
	cfg.MaxReplyChars = groupCfg.MaxReplyChars
	if groupCfg.SystemPrompt != "" {
		// 群级人设覆盖全局，同一个机器人在不同群可以是不同角色。
		cfg.SystemPrompt = groupCfg.SystemPrompt
	}
	if groupCfg.ReplyGate != nil {
		// 群级门槛整体替换全局，而不是逐字段合并——界面上一个
		// 「跟随全局 / 自定义」开关就能表达清楚。
		cfg.ReplyGate = groupCfg.ReplyGate.Clone()
	}
	return cfg
}

func (r *Runtime) groupConfigForEvent(event MessageEvent) (GroupConfig, bool) {
	if event.Kind != EventKindGroup || strings.TrimSpace(event.GroupID) == "" {
		return GroupConfig{}, false
	}
	r.mu.RLock()
	store := r.groupConfigs
	base := r.cfg
	r.mu.RUnlock()
	if store == nil {
		return GroupConfig{}, false
	}
	groupCfg, ok := store.ConfigForGroup(event.GroupID)
	if !ok {
		return GroupConfig{}, false
	}
	return groupCfg.WithDefaults(event.GroupID, base), true
}

func (r *Runtime) pluginOverridesForEvent(event MessageEvent) map[string]bool {
	groupCfg, ok := r.groupConfigForEvent(event)
	if !ok || len(groupCfg.PluginOverrides) == 0 {
		return nil
	}
	out := make(map[string]bool, len(groupCfg.PluginOverrides))
	for id, enabled := range groupCfg.PluginOverrides {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		out[id] = enabled
	}
	return out
}

// HandleEvent 处理 OneBot 消息或通知事件。
func (r *Runtime) HandleEvent(ctx context.Context, event MessageEvent) error {
	if r.isSelfMessage(event) {
		return nil
	}
	if event.Kind == EventKindNotice {
		return r.handleNotice(ctx, event)
	}
	// 放在准入判定之前：被机器人忽略的群消息同样能喂缓存，
	// 等级信息就靠群里的日常水聊免费攒起来。
	r.members.Observe(event)
	text := PlainText(event.Segments)
	if text == "" {
		text = event.RawMessage
	}
	if event.Kind == EventKindPrivate {
		r.mu.RLock()
		interceptor := r.privateMessageInterceptor
		r.mu.RUnlock()
		if interceptor != nil && interceptor(ctx, event, text) {
			r.record(EventRecord{
				At:      time.Now(),
				Kind:    event.Kind,
				UserID:  event.UserID,
				Text:    "[控制台登录配对]",
				Handled: true,
			})
			return nil
		}
	}
	if r.bridge != nil {
		// NoneBot 桥只做旁路转发，不影响本地插件和 LLM 回复流程。
		r.bridge.ForwardEvent(event)
	}
	r.remember(event)
	if !r.shouldHandle(event, text) {
		r.maybeNotifyQuietHours(ctx, event, text)
		r.record(EventRecord{At: time.Now(), Kind: event.Kind, UserID: event.UserID, GroupID: event.GroupID, Text: text, Handled: false})
		return nil
	}

	select {
	case r.sem <- struct{}{}:
		r.incActive(1)
	case <-ctx.Done():
		return ctx.Err()
	}
	go func() {
		// 回复生成放到 goroutine，避免 OneBot read loop 被慢模型调用卡住。
		defer func() {
			<-r.sem
			r.incActive(-1)
		}()
		start := time.Now()
		record := EventRecord{
			At:      start,
			Kind:    event.Kind,
			UserID:  event.UserID,
			GroupID: event.GroupID,
			Text:    text,
			Handled: true,
		}
		reply, err := r.replyTo(ctx, event, text)
		record.Duration = time.Since(start).Milliseconds()
		if err != nil {
			record.Error = err.Error()
			r.setError(err.Error())
			// 完整错误已进事件记录和状态页，聊天里只发截断后的摘要，避免刷屏或带出内部细节。
			if cfg := r.effectiveConfigForEvent(event); boolValue(cfg.ErrorNotifyEnabled, true) {
				_ = r.send(ctx, event, cfg.ErrorReplyPrefix+truncateForChat(err.Error(), 160))
			}
			r.record(record)
			return
		}
		record.Reply = reply
		if reply != "" {
			// 机器人自己的回复也计入会话上下文，下一轮模型才能接住"你刚才说的"这类指代。
			r.rememberReply(event, reply)
		}
		r.record(record)
	}()
	return nil
}

// shouldHandle 判断消息是否需要机器人回复。
//
// 拆成「准入」和「触发匹配」两步：静默时段提示需要区分「被门槛挡掉」和
// 「本来就没触发」，混在一起判断不出来。
func (r *Runtime) shouldHandle(event MessageEvent, text string) bool {
	cfg := r.effectiveConfigForEvent(event)
	return r.admits(cfg, event) && r.matchesTrigger(cfg, event, text)
}

// admits 做准入判定：群是否在工作范围内、是否被禁用、是否过得了回复门槛。
func (r *Runtime) admits(cfg BotConfig, event MessageEvent) bool {
	if event.Kind == EventKindPrivate {
		// 私聊只受用户黑名单和时段约束，群相关的门槛不适用。
		return r.replyGateAllows(cfg, event)
	}
	if event.Kind != EventKindGroup {
		return false
	}
	if !cfg.GroupAdmission.Allows(event.GroupID) {
		return false
	}
	if r.isGroupDisabled(event.GroupID) {
		return false
	}
	return r.replyGateAllows(cfg, event)
}

// matchesTrigger 判断消息内容本身是否构成一次触发，不涉及任何准入判断。
func (r *Runtime) matchesTrigger(cfg BotConfig, event MessageEvent, text string) bool {
	if event.Kind == EventKindPrivate {
		return true
	}
	if event.Kind != EventKindGroup {
		return false
	}
	if event.ToMe {
		return true
	}
	if r.resolverEnabledForEvent(event) && hasKnownResolverMediaURL(event, text) {
		// Social media links are a direct plugin trigger, matching the behavior
		// of the original Go resolver before the assistant package migration.
		return true
	}
	// 有些 OneBot 实现不会把 at 转成 ToMe，这里用 raw_message 再兜底一次。
	if cfg.BotQQ != "" && strings.Contains(event.RawMessage, "[CQ:at,qq="+cfg.BotQQ+"]") {
		return true
	}
	trimmed := strings.TrimSpace(text)
	for _, trigger := range cfg.GroupTriggers {
		if strings.HasPrefix(trimmed, trigger) {
			return true
		}
	}
	return false
}

// quietNoticeInterval 限制静默期提示的频率。群里连着有人喊机器人时，
// 不限频会把「现在休息中」刷成新的骚扰。
const quietNoticeInterval = time.Hour

// maybeNotifyQuietHours 在「本来会回复、但正处于静默时段」时给一句提示。
// 只针对时段拦截：黑名单和等级不足都保持沉默，不解释原因。
func (r *Runtime) maybeNotifyQuietHours(ctx context.Context, event MessageEvent, text string) {
	cfg := r.effectiveConfigForEvent(event)
	gate := cfg.ReplyGate
	if gate == nil || gate.QuietReply == "" {
		return
	}
	if gate.WithinActiveHours(r.clock()) {
		// 不是时段挡的，别把别的拦截原因也提示出去。
		return
	}
	ownerID := strings.TrimSpace(cfg.OwnerID)
	if ownerID != "" && event.UserID == ownerID && gate.OwnerBypassEnabled() {
		return
	}
	if gate.IsBlocked(event.UserID) || gate.IsExempt(event.UserID) {
		return
	}
	if event.Kind == EventKindGroup {
		if !cfg.GroupAdmission.Allows(event.GroupID) || r.isGroupDisabled(event.GroupID) {
			return
		}
	} else if event.Kind != EventKindPrivate {
		return
	}
	if !r.matchesTrigger(cfg, event, text) {
		return
	}
	if !r.allowQuietNotice(event) {
		return
	}
	_ = r.send(ctx, event, gate.QuietReply)
}

// allowQuietNotice 按会话限频，返回 true 表示这次可以提示。
func (r *Runtime) allowQuietNotice(event MessageEvent) bool {
	scope := "private:" + event.UserID
	if event.Kind == EventKindGroup {
		scope = "group:" + event.GroupID
	}
	now := r.clock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.quietNotices == nil {
		r.quietNotices = map[string]time.Time{}
	}
	if last, ok := r.quietNotices[scope]; ok && now.Sub(last) < quietNoticeInterval {
		return false
	}
	r.quietNotices[scope] = now
	return true
}

// replyGateAllows 判断消息是否通过回复门槛。
//
// 判定顺序是刻意的：先做纯本地的廉价判断，最后才做可能要发 OneBot 请求的
// 等级校验，避免被时段或黑名单挡掉的消息还白查一次群成员。
func (r *Runtime) replyGateAllows(cfg BotConfig, event MessageEvent) bool {
	gate := cfg.ReplyGate
	if gate == nil {
		return true
	}
	ownerID := strings.TrimSpace(cfg.OwnerID)
	if ownerID != "" && event.UserID == ownerID && gate.OwnerBypassEnabled() {
		// 主人永远畅通，否则时段或等级配错了就把自己锁在门外，
		// QQ 侧没有任何补救手段。
		return true
	}
	if gate.IsBlocked(event.UserID) {
		return false
	}
	if gate.IsExempt(event.UserID) {
		return true
	}
	if !gate.WithinActiveHours(r.clock()) {
		return false
	}
	// 群等级是 QQ 独有的概念，只有 OneBot 平台才查。否则 Telegram 上每条
	// 未命中缓存的消息都会白白调一次不存在的 get_group_member_info。
	if event.Kind == EventKindGroup && gate.MinGroupLevel > 0 && IsOneBotPlatform(cfg.Platform) {
		level, known := r.members.LevelFor(event)
		if !gate.LevelAllows(level, known) {
			return false
		}
	}
	return true
}

// SetMediaStore 注入入站媒体持久化存储。未设置时图片地址原样交给模型，
// 这在服务商能直接访问该地址时仍可用，只是不稳定。
func (r *Runtime) SetMediaStore(store *MediaStore) {
	r.mu.Lock()
	r.media = store
	r.mu.Unlock()
}

// resolveImageForLLM 把图片地址换成本地文件的 base64 data URL。
//
// 下载或读取失败时回落到原地址：识图退化成「看服务商能不能拉到」，
// 总好过整条消息丢掉图片。
func (r *Runtime) resolveImageForLLM(ctx context.Context, imageURL string) string {
	r.mu.RLock()
	store := r.media
	r.mu.RUnlock()
	if store == nil {
		return imageURL
	}
	path, err := store.Fetch(ctx, imageURL)
	if err != nil {
		log.Printf("media: fetch %s failed: %v", redactURLQuery(imageURL), err)
		return imageURL
	}
	dataURL, err := store.DataURL(path)
	if err != nil {
		log.Printf("media: encode %s failed: %v", filepath.Base(path), err)
		return imageURL
	}
	return dataURL
}

// clock 返回当前时间，测试可注入。
func (r *Runtime) clock() time.Time {
	r.mu.RLock()
	now := r.now
	r.mu.RUnlock()
	if now != nil {
		return now()
	}
	return time.Now()
}

func (r *Runtime) resolverEnabledForEvent(event MessageEvent) bool {
	return r.plugins != nil && r.plugins.EnabledWithOverrides(resolverPluginID, r.pluginOverridesForEvent(event))
}

// replyTo 执行 owner 命令、插件和 LLM 回复链路。
func (r *Runtime) replyTo(ctx context.Context, event MessageEvent, text string) (string, error) {
	cfg := r.effectiveConfigForEvent(event)
	// 每条消息单独限时，防止慢模型/插件占住并发槽太久。
	ctx, cancel := context.WithTimeout(ctx, cfg.RequestTimeout)
	defer cancel()

	cleanText := r.cleanInput(event, text)
	if cfg.MaxInputChars > 0 && len([]rune(cleanText)) > cfg.MaxInputChars {
		cleanText = string([]rune(cleanText)[:cfg.MaxInputChars])
	}
	if reply, handled := r.handleOwnerCommand(event, cleanText); handled {
		// owner 指令优先级最高，避免“切模型/禁群”等管理命令被普通 LLM 回复吞掉。
		if err := r.send(ctx, event, reply); err != nil {
			return "", err
		}
		return reply, nil
	}

	pluginOverrides := r.pluginOverridesForEvent(event)
	pluginResponses := r.plugins.RunWithOverrides(ctx, PluginRequest{
		Event:          event,
		Text:           cleanText,
		OwnerID:        cfg.OwnerID,
		LLMStore:       r.llmStore,
		LLMModelLister: r.llmModelLister(),
		AppLogs:        r.appLogWriter(),
	}, pluginOverrides)
	for _, resp := range pluginResponses {
		if resp.Reply != "" || len(resp.ImageURLs) > 0 || len(resp.VideoURLs) > 0 {
			// A media-producing plugin owns the reply so the downloaded assets are
			// sent immediately instead of being reduced to LLM context.
			if err := r.sendPluginResponse(ctx, event, resp); err != nil {
				return "", err
			}
			return directPluginReply(resp), nil
		}
	}

	resolveImage := func(imageURL string) string { return r.resolveImageForLLM(ctx, imageURL) }
	messages := []llm.Message{{Role: llm.RoleSystem, Content: r.systemPrompt(event, pluginResponses)}}
	for _, entry := range r.contextHistory(event) {
		if entry.botReply != "" {
			// 机器人历史回复以 assistant 角色进入上下文，模型才能接住指代和追问。
			messages = append(messages, llm.Message{Role: llm.RoleAssistant, Content: entry.botReply})
			continue
		}
		// 上下文只追加同会话的历史用户消息，当前消息本身会在最后单独加入。
		if entry.event.MessageID == event.MessageID {
			continue
		}
		historyMessage := llmMessageFromEvent(entry.event, historyPromptText(entry.event), cfg.PromptImageOnlyText, resolveImage)
		if runtimeLLMMessageEmpty(historyMessage) {
			continue
		}
		messages = append(messages, historyMessage)
	}
	messages = append(messages, llmMessageFromEvent(event, cleanText, cfg.PromptImageOnlyText, resolveImage))
	messages = capImageParts(messages, maxImagePartsPerRequest)

	agentTools, err := r.plugins.AgentToolsWithOverrides(pluginOverrides)
	if err != nil {
		return "", err
	}
	reply, err := r.generateReplyWithAgentTools(ctx, cfg, messages, agentTools)
	if err != nil {
		return "", err
	}
	if reply == "" {
		reply = "我这边没有生成有效回复。"
	}
	if err := r.send(ctx, event, reply); err != nil {
		return "", err
	}
	return reply, nil
}

// messagesContainImages 判断这次请求是否携带图片内容。
func messagesContainImages(messages []llm.Message) bool {
	for _, message := range messages {
		for _, part := range message.Parts {
			if part.Type == llm.ContentPartImageURL {
				return true
			}
		}
	}
	return false
}

func (r *Runtime) generateReply(ctx context.Context, cfg BotConfig, messages []llm.Message) (string, error) {
	return r.generateReplyWithAgentTools(ctx, cfg, messages, nil)
}

func (r *Runtime) generateReplyWithAgentTools(ctx context.Context, cfg BotConfig, messages []llm.Message, extraTools []agent.Tool) (string, error) {
	// 带图片的请求优先用「识图」组模型；未配置该组时自动回落到对话组。
	group := llm.GroupChat
	if messagesContainImages(messages) {
		group = llm.GroupVision
	}
	return r.runLLMProviderForGroup(ctx, group, func(client LLMProvider) (string, error) {
		if cfg.AgentEnabled || len(extraTools) > 0 {
			// Agent 模式允许模型调用受限本地工具；普通模式只走一次 LLM 生成。
			agentConfig := agent.Config{
				WorkDir:          cfg.AgentWorkDir,
				MaxSteps:         cfg.AgentMaxSteps,
				SkillRoots:       cfg.AgentSkillRoots,
				MCPConfigPath:    cfg.AgentMCPConfigPath,
				CommandAllowlist: cfg.AgentCommandAllowlist,
				CommandTimeoutMS: cfg.AgentCommandTimeoutMS,
				BrowserCDPURL:    cfg.AgentBrowserCDPURL,
				BrowserTimeoutMS: cfg.AgentBrowserTimeoutMS,
			}
			registry := agent.NewToolRegistry()
			if cfg.AgentEnabled {
				var err error
				registry, err = agent.NewCodexToolRegistry(ctx, agentConfig)
				if err != nil {
					return "", err
				}
			}
			for _, tool := range extraTools {
				registry.Register(tool)
			}
			agentRunner, err := agent.NewRunner(client, agentConfig, registry)
			if err != nil {
				return "", err
			}
			defer agentRunner.Close()
			resp, err := agentRunner.Run(ctx, agent.Request{Messages: messages})
			if err != nil {
				return "", err
			}
			return normalizeReply(resp.Text, cfg.MaxReplyChars, boolValue(cfg.MarkdownToPlain, true)), nil
		}
		resp, err := client.Generate(ctx, llm.GenerateRequest{Messages: messages})
		if err != nil {
			return "", err
		}
		return normalizeReply(resp.Text, cfg.MaxReplyChars, boolValue(cfg.MarkdownToPlain, true)), nil
	})
}

type llmProviderRunFunc func(LLMProvider) (string, error)

func (r *Runtime) runLLMProvider(ctx context.Context, run llmProviderRunFunc) (string, error) {
	return r.runLLMProviderForGroup(ctx, llm.GroupChat, run)
}

// runLLMProviderForGroup 按语义分组选择模型执行；专用组（识图/意图/生图）没有配置时
// 回落到对话组的现有轮换降级链路。
func (r *Runtime) runLLMProviderForGroup(ctx context.Context, group string, run llmProviderRunFunc) (string, error) {
	r.mu.RLock()
	cfgFactory := r.llmCfgFactory
	factory := r.llmFactory
	store := r.llmStore
	r.mu.RUnlock()

	if cfgFactory != nil && store != nil {
		return r.runLLMProviderWithFailover(ctx, store, cfgFactory, group, run)
	}
	if factory == nil {
		return "", fmt.Errorf("qqbot: llm provider is not configured")
	}
	client, err := factory()
	if err != nil {
		return "", err
	}
	return run(client)
}

// roleBoundConfigs 按机器人配置的用途分配解析候选配置链：绑定单渠道时只有
// 一个候选；绑定分组时组内全部渠道按顺序轮换降级，模型统一用绑定的模型。
// 指定用途未绑定时回退 chat 绑定；返回空表示完全未配置分配，走旧降级链。
func (r *Runtime) roleBoundConfigs(set llm.ProfileSet, group string) []llm.ProviderConfig {
	r.mu.RLock()
	roles := r.cfg.ModelRoles
	r.mu.RUnlock()
	if len(roles) == 0 {
		return nil
	}
	key := llm.NormalizeProfileGroup(group)
	if key == llm.GroupChat {
		key = "chat"
	}
	role, ok := roles[key]
	if !ok {
		role, ok = roles["chat"]
	}
	if !ok {
		return nil
	}
	if role.Group != "" {
		profiles := set.GroupProfiles(role.Group)
		out := make([]llm.ProviderConfig, 0, len(profiles))
		for _, profile := range profiles {
			cfg := profile.Config.WithDefaults()
			cfg.Model = role.Model
			out = append(out, cfg)
		}
		return out
	}
	for _, profile := range set.Profiles {
		if profile.ID == role.ProfileID {
			cfg := profile.Config.WithDefaults()
			cfg.Model = role.Model
			return []llm.ProviderConfig{cfg}
		}
	}
	// 绑定指向的渠道已被删除，回退旧链路。
	return nil
}

func (r *Runtime) runLLMProviderWithFailover(ctx context.Context, store LLMProfileStore, factory LLMProviderConfigFactory, group string, run llmProviderRunFunc) (string, error) {
	set := store.Profiles().WithDefaults()
	// 机器人配置里按用途分配了模型时优先使用；分组绑定沿候选链轮换降级。
	if candidates := r.roleBoundConfigs(set, group); len(candidates) > 0 {
		var lastErr error
		for _, cfg := range candidates {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			client, err := factory(cfg)
			if err == nil {
				reply, runErr := run(client)
				err = runErr
				if err == nil {
					return reply, nil
				}
			}
			lastErr = err
			if !shouldFailoverLLMError(err) {
				return "", err
			}
		}
		return "", fmt.Errorf("qqbot: 用途分配的渠道均不可用: %w", lastErr)
	}
	// 专用组按组内顺序降级且不改动激活项；对话组维持"从激活项开始轮换、成功即激活"的老行为。
	updateActive := false
	attempts := set.GroupProfiles(group)
	if llm.NormalizeProfileGroup(group) == llm.GroupChat || len(attempts) == 0 {
		attempts = set.ActiveGroupProfiles()
		updateActive = true
	}
	if len(attempts) == 0 {
		return "", fmt.Errorf("qqbot: no llm profile is configured")
	}
	group = llm.NormalizeProfileGroup(attempts[0].Group)
	var lastErr error
	for _, profile := range attempts {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		client, err := factory(profile.Config)
		if err == nil {
			reply, runErr := run(client)
			err = runErr
			if err == nil {
				if updateActive && profile.ID != set.ActiveID {
					set.ActiveID = profile.ID
					store.SaveProfiles(set)
				}
				return reply, nil
			}
		}
		lastErr = err
		if !shouldFailoverLLMError(err) {
			return "", err
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("unknown llm profile error")
	}
	if len(attempts) == 1 {
		return "", lastErr
	}
	return "", fmt.Errorf("qqbot: llm profiles in group %q are unavailable: %w", group, lastErr)
}

func shouldFailoverLLMError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{
		"401", "403", "429",
		"unauthorized", "forbidden", "too many requests",
		"api key", "apikey", "authentication", "auth",
		"permission", "permission_error",
		"quota", "insufficient_quota", "billing", "credit",
		"rate limit", "rate_limit",
		"未授权", "无权限", "额度", "限流", "失效", "无效",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

var chineseWeekdays = [...]string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}

// systemPrompt 组合系统提示词和插件上下文。
func (r *Runtime) systemPrompt(event MessageEvent, pluginResponses []PluginResponse) string {
	cfg := r.effectiveConfigForEvent(event)
	var builder strings.Builder
	builder.WriteString(cfg.SystemPrompt)
	// 以下增强默认全开，分发部署可在机器人设置里按人设逐项关闭。
	if boolValue(cfg.PromptChineseSlangHint, true) {
		appendPromptSection(&builder, cfg.PromptChineseSlangText)
	}
	if boolValue(cfg.PromptInjectPlaintextRules, true) {
		appendPromptSection(&builder, cfg.PromptPlaintextRulesText)
	}
	if boolValue(cfg.PromptInjectTime, true) {
		now := time.Now()
		appendPromptSection(&builder, renderPromptTemplate(cfg.PromptTimeTemplate, map[string]string{
			"datetime": now.Format("2006-01-02 15:04"),
			"weekday":  chineseWeekdays[now.Weekday()],
		}))
	}
	if event.Kind == EventKindGroup && boolValue(cfg.PromptInjectGroupSender, true) {
		appendPromptSection(&builder, renderPromptTemplate(cfg.PromptGroupSenderTemplate, map[string]string{
			"sender": event.SenderNameOrID(),
		}))
	}
	for _, resp := range pluginResponses {
		if strings.TrimSpace(resp.Context) == "" {
			continue
		}
		// 插件上下文只进系统提示，不直接暴露给用户，最终话术仍由 LLM 统一生成。
		builder.WriteString("\n\n【插件上下文】\n")
		builder.WriteString(resp.Context)
	}
	return builder.String()
}

func appendPromptSection(builder *strings.Builder, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	builder.WriteString("\n")
	builder.WriteString(text)
}

func renderPromptTemplate(template string, values map[string]string) string {
	replacements := make([]string, 0, len(values)*2)
	for key, value := range values {
		replacements = append(replacements, "{"+key+"}", value)
	}
	return strings.NewReplacer(replacements...).Replace(template)
}

// cleanInput 清理机器人 at 和空白后生成模型输入。
func (r *Runtime) cleanInput(event MessageEvent, text string) string {
	cfg := r.effectiveConfigForEvent(event)
	// 优先使用 segment 转出的可读文本，保留 @ 和触发词，但不把 CQ 协议码直接交给模型。
	text = readableEventText(event, text)
	text = strings.TrimSpace(text)
	if imageOnlyPrompt(text, event) {
		return cfg.PromptImageOnlyText
	}
	if text == "" {
		return cfg.PromptWakeOnlyText
	}
	return text
}

func readableEventText(event MessageEvent, fallback string) string {
	if text := strings.TrimSpace(PlainText(event.Segments)); text != "" {
		return normalizeChatWhitespace(text)
	}
	if text := strings.TrimSpace(event.RawMessage); text != "" {
		if strings.Contains(text, "[CQ:") {
			if parsed := strings.TrimSpace(PlainText(CQToSegments(text))); parsed != "" {
				return normalizeChatWhitespace(parsed)
			}
		}
		return normalizeChatWhitespace(text)
	}
	return normalizeChatWhitespace(fallback)
}

func normalizeChatWhitespace(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func historyPromptText(event MessageEvent) string {
	text := PlainText(event.Segments)
	if text == "" {
		text = event.RawMessage
	}
	text = strings.TrimSpace(text)
	if text == "" && len(ImageURLs(event.Segments)) > 0 {
		text = "[图片]"
	}
	if text == "" {
		return ""
	}
	return fmt.Sprintf("%s: %s", event.SenderNameOrID(), text)
}

// maxImagePartsPerRequest 限制单次请求携带的图片数量。
//
// 上下文默认保留 20 条且每轮重放，图片改成 base64 之后不设上限的话，
// 一次请求能堆到几十 MB，服务商会直接拒绝，表现是整条回复失败。
// 保留最近的几张即可：追问「这是什么」时要看的就是刚发的那张。
const maxImagePartsPerRequest = 4

// capImageParts 从后往前保留最近的若干张图片，更早的图片降级成文本标记。
// 从后往前是关键：最新的图片才是用户正在问的那张。
func capImageParts(messages []llm.Message, limit int) []llm.Message {
	if limit <= 0 {
		return messages
	}
	kept := 0
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if len(msg.Parts) == 0 {
			continue
		}
		parts := make([]llm.ContentPart, 0, len(msg.Parts))
		dropped := false
		for j := len(msg.Parts) - 1; j >= 0; j-- {
			part := msg.Parts[j]
			if part.Type != llm.ContentPartImageURL {
				parts = append(parts, part)
				continue
			}
			if kept < limit {
				kept++
				parts = append(parts, part)
				continue
			}
			dropped = true
		}
		if !dropped {
			continue
		}
		// parts 是倒序收集的，写回前翻转还原顺序。
		for l, r := 0, len(parts)-1; l < r; l, r = l+1, r-1 {
			parts[l], parts[r] = parts[r], parts[l]
		}
		if !hasImagePart(parts) {
			// 整条消息的图片都被丢掉时退回纯文本，避免留下只有文本部件的空壳。
			messages[i] = llm.Message{Role: msg.Role, Content: msg.Content}
			continue
		}
		messages[i] = llm.Message{Role: msg.Role, Content: msg.Content, Parts: parts}
	}
	return messages
}

func hasImagePart(parts []llm.ContentPart) bool {
	for _, part := range parts {
		if part.Type == llm.ContentPartImageURL {
			return true
		}
	}
	return false
}

func llmMessageFromEvent(event MessageEvent, text string, imageOnlyText string, resolveImage func(string) string) llm.Message {
	text = strings.TrimSpace(text)
	imageURLs := ImageURLs(event.Segments)
	if len(imageURLs) == 0 {
		return llm.Message{Role: llm.RoleUser, Content: text}
	}
	if imageOnlyPrompt(text, event) {
		text = imageOnlyText
	}
	parts := make([]llm.ContentPart, 0, len(imageURLs)+1)
	if text != "" {
		parts = append(parts, llm.ContentPart{Type: llm.ContentPartText, Text: text})
	}
	for _, imageURL := range imageURLs {
		if resolveImage != nil {
			imageURL = resolveImage(imageURL)
		}
		parts = append(parts, llm.ContentPart{Type: llm.ContentPartImageURL, ImageURL: imageURL, Detail: "auto"})
	}
	return llm.Message{Role: llm.RoleUser, Content: text, Parts: parts}
}

func imageOnlyPrompt(text string, event MessageEvent) bool {
	if len(ImageURLs(event.Segments)) == 0 {
		return false
	}
	text = strings.TrimSpace(text)
	return text == "" || text == "[图片]"
}

func runtimeLLMMessageEmpty(msg llm.Message) bool {
	if strings.TrimSpace(msg.Content) != "" {
		return false
	}
	return len(msg.Parts) == 0
}

// sendRetryAttempts 控制单条消息的最大发送次数，退避间隔给 NapCat 断线重连留时间。
const sendRetryAttempts = 3

// 变量形式便于测试缩短等待；生产值权衡了重连时间和用户等待感。
var (
	sendRetryBackoff = 700 * time.Millisecond
	// sendChunkInterval 是多段回复之间的间隔，连续快速发送容易触发风控或乱序。
	sendChunkInterval = 300 * time.Millisecond
)

// send 按私聊或群聊规则发送回复。
func (r *Runtime) send(ctx context.Context, event MessageEvent, reply string) error {
	cfg := r.effectiveConfigForEvent(event)
	chunks := splitReply(reply, cfg.DirectReplyChunkSize)

	// 超过阈值的长回复优先走合并转发，聊天窗口只占一条；失败再回退普通分段。
	if cfg.ForwardReplyThreshold > 0 && len([]rune(reply)) > cfg.ForwardReplyThreshold {
		if err := r.sendForward(ctx, event, chunks); err == nil {
			return nil
		} else if ctx.Err() != nil {
			return err
		}
	}

	// 分段间隔与首段引用/@ 行为都可按配置个性化。
	chunkInterval := time.Duration(cfg.SendChunkIntervalMS) * time.Millisecond
	if chunkInterval <= 0 {
		chunkInterval = sendChunkInterval
	}
	sent := 0
	for _, chunk := range chunks {
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		if sent > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(chunkInterval):
			}
		}
		msg := OutgoingMessage{Text: chunk}
		if event.Kind == EventKindGroup {
			msg.GroupID = event.GroupID
			if sent == 0 {
				// 只在第一段带 reply 和 at：既标明回复对象，又避免后续每段都刷一次 @。
				if boolValue(cfg.ReplyReferenceEnabled, true) {
					msg.ReplyMessageID = event.MessageID
				}
				if boolValue(cfg.MentionUserEnabled, true) {
					msg.MentionUserID = event.UserID
				}
			}
		} else {
			msg.UserID = event.UserID
		}
		if err := r.sendWithRetry(ctx, msg, cfg.SendRetryAttempts); err != nil {
			return err
		}
		sent++
	}
	return nil
}

const resolverLocalMediaTTL = 10 * time.Minute

func directPluginReply(resp PluginResponse) string {
	if reply := strings.TrimSpace(resp.Reply); reply != "" {
		return reply
	}
	return strings.TrimSpace(resp.Context)
}

// sendPluginResponse sends text and remote images in one OneBot message. Local
// videos are exposed through an expiring random URL so NapCat may run in a
// different container without sharing Diana's temporary directory.
func (r *Runtime) sendPluginResponse(ctx context.Context, event MessageEvent, resp PluginResponse) error {
	r.mu.RLock()
	sharer := r.localMedia
	r.mu.RUnlock()
	videoURLs := make([]string, 0, len(resp.VideoURLs))
	localPaths := make([]string, 0, len(resp.VideoURLs))
	for _, value := range resp.VideoURLs {
		if path := localMediaPath(value); path != "" {
			if sharer == nil {
				return fmt.Errorf("qqbot: local media sharing is not configured")
			}
			sharedURL, ok := sharer.Share(path, resolverLocalMediaTTL)
			if !ok {
				return fmt.Errorf("qqbot: cannot share downloaded media %q", filepath.Base(path))
			}
			videoURLs = append(videoURLs, sharedURL)
			localPaths = append(localPaths, path)
			continue
		}
		if value = strings.TrimSpace(value); value != "" {
			videoURLs = append(videoURLs, value)
		}
	}

	msg := OutgoingMessage{
		Text:      directPluginReply(resp),
		ImageURLs: append([]string(nil), resp.ImageURLs...),
		VideoURLs: videoURLs,
	}
	cfg := r.effectiveConfigForEvent(event)
	if event.Kind == EventKindGroup {
		msg.GroupID = event.GroupID
		if boolValue(cfg.ReplyReferenceEnabled, true) {
			msg.ReplyMessageID = event.MessageID
		}
		if boolValue(cfg.MentionUserEnabled, true) {
			msg.MentionUserID = event.UserID
		}
	} else {
		msg.UserID = event.UserID
	}
	if err := r.sendWithRetry(ctx, msg, cfg.SendRetryAttempts); err != nil {
		cleanupLocalMediaFilesLater(localPaths, time.Second)
		return err
	}
	cleanupLocalMediaFilesLater(localPaths, resolverLocalMediaTTL)
	return nil
}

// sendWithRetry 发送单条消息，对非取消类错误做带退避的重试。
func (r *Runtime) sendWithRetry(ctx context.Context, msg OutgoingMessage, attempts int) error {
	if attempts <= 0 {
		attempts = sendRetryAttempts
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * sendRetryBackoff):
			}
		}
		r.mu.RLock()
		channel := r.channel
		r.mu.RUnlock()
		if channel == nil {
			return fmt.Errorf("qqbot: channel is not configured")
		}
		lastErr = channel.Send(ctx, msg)
		if lastErr == nil {
			return nil
		}
		if ctx.Err() != nil {
			// 上下文已取消/超时说明这条回复整体作废，不再重试。
			return lastErr
		}
	}
	return fmt.Errorf("qqbot: send failed after %d attempts: %w", attempts, lastErr)
}

// sendForward 把长回复按分段打包成合并转发消息发送。
func (r *Runtime) sendForward(ctx context.Context, event MessageEvent, chunks []string) error {
	cfg := r.effectiveConfigForEvent(event)
	botID := strings.TrimSpace(cfg.BotQQ)
	if botID == "" {
		botID = strings.TrimSpace(event.SelfID)
	}
	if botID == "" {
		return fmt.Errorf("qqbot: bot qq is unknown, cannot build forward nodes")
	}
	nickname := strings.TrimSpace(cfg.Name)
	if nickname == "" {
		nickname = "Diana"
	}

	nodes := make([]map[string]any, 0, len(chunks))
	for _, chunk := range chunks {
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		nodes = append(nodes, map[string]any{
			"type": "node",
			"data": map[string]any{
				"user_id":  botID,
				"nickname": nickname,
				"content": []map[string]any{
					{"type": "text", "data": map[string]string{"text": chunk}},
				},
			},
		})
	}
	if len(nodes) == 0 {
		return nil
	}

	params := map[string]any{"messages": nodes}
	action := "send_private_forward_msg"
	if event.Kind == EventKindGroup {
		action = "send_group_forward_msg"
		groupID, err := strconv.ParseInt(event.GroupID, 10, 64)
		if err != nil {
			return err
		}
		params["group_id"] = groupID
	} else {
		userID, err := strconv.ParseInt(event.UserID, 10, 64)
		if err != nil {
			return err
		}
		params["user_id"] = userID
	}

	r.mu.RLock()
	channel := r.channel
	r.mu.RUnlock()
	if channel == nil {
		return fmt.Errorf("qqbot: channel is not configured")
	}
	_, err := channel.CallAPI(ctx, action, params)
	return err
}

// handleNotice 处理群通知事件。
func (r *Runtime) handleNotice(ctx context.Context, event MessageEvent) error {
	cfg := r.effectiveConfigForEvent(event)
	if !cfg.WelcomeEnabled {
		return nil
	}
	if event.SubType != "group_increase" || event.GroupID == "" || event.UserID == "" {
		return nil
	}
	if !cfg.GroupAdmission.Allows(event.GroupID) {
		return nil
	}
	if r.isGroupDisabled(event.GroupID) {
		return nil
	}
	// 欢迎语同样受时段约束，半夜不该往群里弹消息。新人的群等级必然是
	// 最低档，这里只判时段，不套等级门槛。
	if gate := cfg.ReplyGate; gate != nil && !gate.WithinActiveHours(r.clock()) {
		return nil
	}
	// 只处理群成员增加通知，避免把其它 notice 类型误当作可回复消息。
	welcome := strings.ReplaceAll(cfg.WelcomeMessage, "{user_id}", event.UserID)
	msg := OutgoingMessage{
		GroupID:       event.GroupID,
		Text:          welcome,
		MentionUserID: event.UserID,
	}
	if err := r.channel.Send(ctx, msg); err != nil {
		r.setError(err.Error())
		return err
	}
	r.record(EventRecord{
		At:      time.Now(),
		Kind:    event.Kind,
		UserID:  event.UserID,
		GroupID: event.GroupID,
		Text:    "[notice] group_increase",
		Reply:   welcome,
		Handled: true,
	})
	return nil
}

// remember 记录当前会话的最近上下文。
func (r *Runtime) remember(event MessageEvent) {
	r.appendHistory(event, historyEntry{event: event})
}

// rememberReply 把机器人自己的回复也计入会话历史，供后续轮次作为 assistant 上下文。
func (r *Runtime) rememberReply(event MessageEvent, reply string) {
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return
	}
	r.appendHistory(event, historyEntry{event: event, botReply: reply})
}

func (r *Runtime) appendHistory(event MessageEvent, entry historyEntry) {
	session := sessionKey(event)
	r.mu.Lock()
	defer r.mu.Unlock()
	history := append(r.history[session], entry)
	limit := r.effectiveConfigForEventLocked(event).RecentContextLimit
	if limit <= 0 {
		limit = 20
	}
	if len(history) > limit {
		// 每个会话只保留最近 N 条，避免长时间运行后内存和 prompt 无限增长。
		history = history[len(history)-limit:]
	}
	r.history[session] = history
}

// contextHistory 返回当前会话历史副本。
func (r *Runtime) contextHistory(event MessageEvent) []historyEntry {
	session := sessionKey(event)
	r.mu.RLock()
	defer r.mu.RUnlock()
	// 返回副本，生成回复时遍历历史不会和新消息写入互相影响。
	return append([]historyEntry(nil), r.history[session]...)
}

// record 记录状态页最近事件。
func (r *Runtime) record(record EventRecord) {
	r.mu.Lock()
	r.recent = append([]EventRecord{record}, r.recent...)
	if len(r.recent) > 20 {
		// 状态页只展示最近事件，超过 20 条截断即可。
		r.recent = r.recent[:20]
	}
	r.updatedAt = time.Now()
	listener := r.eventListener
	r.mu.Unlock()
	if listener != nil {
		// 异步通知，统计或推送阻塞时不影响消息处理。
		go listener(record)
	}
}

// setError 更新运行时最后错误。
func (r *Runtime) setError(message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastError = message
	r.updatedAt = time.Now()
}

// incActive 调整活跃 worker 计数。
func (r *Runtime) incActive(delta int) {
	r.activeMu.Lock()
	defer r.activeMu.Unlock()
	r.active += delta
}

// activeCount 返回当前活跃 worker 数。
func (r *Runtime) activeCount() int {
	r.activeMu.Lock()
	defer r.activeMu.Unlock()
	return r.active
}

// sessionKey 根据事件生成上下文会话 key。
func sessionKey(event MessageEvent) string {
	if event.Kind == EventKindGroup {
		return "group:" + event.GroupID
	}
	return "private:" + event.UserID
}

// handleOwnerCommand 处理 owner 的强格式管理命令。
func (r *Runtime) handleOwnerCommand(event MessageEvent, text string) (string, bool) {
	cfg := r.Config().WithDefaults()
	if strings.TrimSpace(cfg.OwnerID) == "" || event.UserID != cfg.OwnerID {
		return "", false
	}

	// 这些是强格式管理命令；自然语言切模型由官方 LLM 配置插件处理。
	command := strings.TrimSpace(text)
	switch {
	case command == "lllm 列表":
		return r.renderLLMProfiles(), true
	case command == "lllm 当前":
		return r.renderCurrentLLMProfile(), true
	case strings.HasPrefix(command, "lllm 切换 "):
		name := strings.TrimSpace(strings.TrimPrefix(command, "lllm 切换 "))
		return r.switchLLMProfile(name)
	case command == "群 列表":
		return r.renderDisabledGroups(), true
	case strings.HasPrefix(command, "群 禁用 "):
		groupID := strings.TrimSpace(strings.TrimPrefix(command, "群 禁用 "))
		return r.disableGroup(groupID), true
	case strings.HasPrefix(command, "群 启用 "):
		groupID := strings.TrimSpace(strings.TrimPrefix(command, "群 启用 "))
		return r.enableGroup(groupID), true
	case command == "提醒 列表":
		return r.renderReminders(), true
	case strings.HasPrefix(command, "提醒 删除 "):
		id := strings.TrimSpace(strings.TrimPrefix(command, "提醒 删除 "))
		return r.deleteReminder(id), true
	case strings.HasPrefix(command, "提醒 添加 "):
		args := strings.TrimSpace(strings.TrimPrefix(command, "提醒 添加 "))
		return r.addReminder(event, args), true
	case command == "清空上下文":
		r.clearSessionHistory(event)
		return "已清空当前会话上下文。", true
	case command == "帮助" || command == "菜单":
		return "可用命令：lllm 列表、lllm 当前、lllm 切换 <名称>、群 列表、群 禁用 <群号>、群 启用 <群号>、提醒 添加 <时长> <内容>、提醒 列表、提醒 删除 <ID>、清空上下文。也可以直接说：把提供商切到 gemini、把模型换成 gemini-2.5-pro。模型必须存在于当前 provider 的模型列表里。", true
	default:
		return "", false
	}
}

// renderLLMProfiles 渲染 LLM 配置档列表。
func (r *Runtime) renderLLMProfiles() string {
	if r.llmStore == nil {
		return "当前未接入 LLM 配置集。"
	}
	set := r.llmStore.Profiles()
	if len(set.Profiles) == 0 {
		return "当前没有可用的 LLM 配置。"
	}
	profiles := append([]llm.Profile(nil), set.Profiles...)
	sort.Slice(profiles, func(i, j int) bool {
		return profiles[i].Name < profiles[j].Name
	})
	lines := []string{"LLM 配置列表："}
	for _, profile := range profiles {
		prefix := "- "
		if profile.ID == set.ActiveID {
			prefix = "* "
		}
		lines = append(lines, fmt.Sprintf("%s%s [%s] (%s / %s)", prefix, profile.Name, llm.NormalizeProfileGroup(profile.Group), profile.Config.Provider, profile.Config.Model))
	}
	return strings.Join(lines, "\n")
}

// renderCurrentLLMProfile 渲染当前 LLM 配置档。
func (r *Runtime) renderCurrentLLMProfile() string {
	if r.llmStore == nil {
		return "当前未接入 LLM 配置集。"
	}
	profile, ok := r.llmStore.Profiles().Current()
	if !ok {
		return "当前没有激活的 LLM 配置。"
	}
	return fmt.Sprintf("当前 LLM：%s\n分组：%s\nProvider：%s\nModel：%s", profile.Name, llm.NormalizeProfileGroup(profile.Group), profile.Config.Provider, profile.Config.Model)
}

// switchLLMProfile 按名称切换 LLM 配置档。
func (r *Runtime) switchLLMProfile(name string) (string, bool) {
	if r.llmStore == nil {
		return "当前未接入 LLM 配置集。", true
	}
	set := r.llmStore.Profiles()
	for _, profile := range set.Profiles {
		if profile.Name != name {
			continue
		}
		// 只切换 active profile，不修改任何 provider/model 具体参数。
		set.ActiveID = profile.ID
		r.llmStore.SaveProfiles(set)
		return fmt.Sprintf("已切换到 LLM 配置：%s", profile.Name), true
	}
	return "没有找到对应的 LLM 配置。", true
}

// clearSessionHistory 清空当前会话上下文。
func (r *Runtime) clearSessionHistory(event MessageEvent) {
	session := sessionKey(event)
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.history, session)
}

// renderDisabledGroups 渲染禁用群列表。
func (r *Runtime) renderDisabledGroups() string {
	cfg := r.Config().WithDefaults()
	if len(cfg.DisabledGroups) == 0 {
		return "当前没有被禁用的群。"
	}
	lines := []string{"已禁用群列表："}
	for _, groupID := range cfg.DisabledGroups {
		lines = append(lines, "- "+groupID)
	}
	return strings.Join(lines, "\n")
}

// disableGroup 禁用指定群的机器人响应。
func (r *Runtime) disableGroup(groupID string) string {
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return "用法：群 禁用 <群号>"
	}
	cfg := r.Config().WithDefaults()
	for _, existing := range cfg.DisabledGroups {
		if existing == groupID {
			return "这个群已经处于禁用状态。"
		}
	}
	cfg.DisabledGroups = append(cfg.DisabledGroups, groupID)
	cfg = cfg.WithDefaults()
	r.mu.Lock()
	r.cfg = cfg
	r.updatedAt = time.Now()
	r.mu.Unlock()
	if r.configSaver != nil {
		// 群开关由聊天指令修改，必须立即落盘，否则重启后会丢失。
		r.configSaver.SaveBotConfig(cfg)
	}
	return "已禁用该群的机器人响应。"
}

// enableGroup 恢复指定群的机器人响应。
func (r *Runtime) enableGroup(groupID string) string {
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return "用法：群 启用 <群号>"
	}
	cfg := r.Config().WithDefaults()
	next := make([]string, 0, len(cfg.DisabledGroups))
	removed := false
	for _, existing := range cfg.DisabledGroups {
		if existing == groupID {
			removed = true
			continue
		}
		next = append(next, existing)
	}
	if !removed {
		return "这个群当前没有被禁用。"
	}
	cfg.DisabledGroups = next
	cfg = cfg.WithDefaults()
	r.mu.Lock()
	r.cfg = cfg
	r.updatedAt = time.Now()
	r.mu.Unlock()
	if r.configSaver != nil {
		// 与禁用保持对称，恢复群响应后同步保存配置。
		r.configSaver.SaveBotConfig(cfg)
	}
	return "已恢复该群的机器人响应。"
}

// renderReminders 渲染提醒列表。
func (r *Runtime) renderReminders() string {
	if r.reminders == nil {
		return "当前未启用提醒功能。"
	}
	items := r.reminders.Reminders()
	if len(items) == 0 {
		return "当前没有待触发的提醒。"
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].TriggerAt.Before(items[j].TriggerAt)
	})
	lines := []string{"提醒列表："}
	for _, item := range items {
		lines = append(lines, fmt.Sprintf("- %s | %s | %s", item.ID, item.TriggerAt.Format("2006-01-02 15:04:05"), item.Message))
	}
	return strings.Join(lines, "\n")
}

// deleteReminder 删除指定提醒。
func (r *Runtime) deleteReminder(id string) string {
	if r.reminders == nil {
		return "当前未启用提醒功能。"
	}
	items := r.reminders.Reminders()
	next := make([]Reminder, 0, len(items))
	removed := false
	for _, item := range items {
		if item.ID == id {
			removed = true
			continue
		}
		next = append(next, item)
	}
	if !removed {
		return "没有找到对应的提醒。"
	}
	r.reminders.SaveReminders(next)
	return "提醒已删除。"
}

// addReminder 创建新的聊天提醒。
func (r *Runtime) addReminder(event MessageEvent, args string) string {
	if r.reminders == nil {
		return "当前未启用提醒功能。"
	}
	parts := strings.Fields(args)
	if len(parts) < 2 {
		return "用法：提醒 添加 <时长> <内容>"
	}
	// 直接使用 Go duration 语法，owner 在 QQ 里输入 10m/2h/30s 即可。
	duration, err := time.ParseDuration(parts[0])
	if err != nil || duration <= 0 {
		return "提醒时长格式不对，例如 10m、2h、30s。"
	}
	message := strings.TrimSpace(strings.TrimPrefix(args, parts[0]))
	if message == "" {
		return "提醒内容不能为空。"
	}
	reminder := Reminder{
		ID:        uuid.NewString()[:8],
		OwnerID:   event.UserID,
		GroupID:   event.GroupID,
		UserID:    event.UserID,
		Message:   message,
		TriggerAt: time.Now().Add(duration),
		CreatedAt: time.Now(),
	}
	items := append(r.reminders.Reminders(), reminder)
	r.reminders.SaveReminders(items)
	return fmt.Sprintf("提醒已创建：%s，%s 后提醒你。", reminder.ID, duration.String())
}

// runReminderLoop 启动提醒轮询循环。
func (r *Runtime) runReminderLoop(ctx context.Context) {
	if r.reminders == nil {
		return
	}
	// 简单轮询足够支撑本地提醒；避免引入额外调度器状态。
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.fireDueReminders(ctx)
		}
	}
}

// fireDueReminders 发送到期提醒并更新剩余列表。
func (r *Runtime) fireDueReminders(ctx context.Context) {
	items := r.reminders.Reminders()
	if len(items) == 0 {
		return
	}
	now := time.Now()
	remaining := make([]Reminder, 0, len(items))
	for _, item := range items {
		if item.TriggerAt.After(now) {
			remaining = append(remaining, item)
			continue
		}
		msg := OutgoingMessage{
			UserID: item.UserID,
			Text:   "提醒你：" + item.Message,
		}
		if item.GroupID != "" {
			msg.GroupID = item.GroupID
		}
		if err := r.channel.Send(ctx, msg); err != nil {
			// 发送失败的提醒保留，下轮继续尝试，避免网络抖动导致提醒丢失。
			remaining = append(remaining, item)
			r.setError(err.Error())
		}
	}
	r.reminders.SaveReminders(remaining)
}

// SenderNameOrID 返回发送者昵称或 ID。
func (event MessageEvent) SenderNameOrID() string {
	if event.SenderName != "" {
		return event.SenderName
	}
	if event.UserID != "" {
		return event.UserID
	}
	return "用户"
}

// normalizeReply 清理并截断模型回复。
// truncateForChat 截断发进聊天的长文本，完整内容保留在日志和事件记录里。
func truncateForChat(text string, maxRunes int) string {
	runes := []rune(strings.TrimSpace(text))
	if maxRunes <= 0 || len(runes) <= maxRunes {
		return string(runes)
	}
	return string(runes[:maxRunes]) + "…"
}

func normalizeReply(reply string, maxRunes int, markdownPlain bool) string {
	// QQ 不渲染 Markdown，默认把模型输出里的标记降级成纯文本再限长；可按配置关闭。
	if markdownPlain {
		reply = markdownToPlain(reply)
	}
	reply = strings.TrimSpace(reply)
	if maxRunes > 0 && len([]rune(reply)) > maxRunes {
		reply = string([]rune(reply)[:maxRunes]) + "..."
	}
	return reply
}

// isSelfMessage 判断事件是否来自机器人自身。
func (r *Runtime) isSelfMessage(event MessageEvent) bool {
	cfg := r.Config().WithDefaults()
	if event.UserID == "" || cfg.BotQQ == "" {
		return false
	}
	return event.UserID == cfg.BotQQ
}

// isGroupDisabled 判断群是否被禁用。
func (r *Runtime) isGroupDisabled(groupID string) bool {
	r.mu.RLock()
	cfg := r.cfg.WithDefaults()
	store := r.groupConfigs
	r.mu.RUnlock()
	if store != nil {
		if groupCfg, ok := store.ConfigForGroup(groupID); ok && !groupCfg.WithDefaults(groupID, cfg).Enabled {
			return true
		}
	}
	for _, disabled := range cfg.DisabledGroups {
		if disabled == groupID {
			return true
		}
	}
	return false
}

// splitReply 将长回复按手动标记和长度切分。
func splitReply(reply string, chunkSize int) []string {
	if chunkSize <= 0 {
		chunkSize = 500
	}
	// <botbr> 是提示词里约定的手动分段标记，先按它切，再按长度兜底切块。
	manual := strings.Split(reply, "<botbr>")
	var out []string
	for _, part := range manual {
		runes := []rune(strings.TrimSpace(part))
		for len(runes) > chunkSize {
			out = append(out, string(runes[:chunkSize]))
			runes = runes[chunkSize:]
		}
		if len(runes) > 0 {
			out = append(out, string(runes))
		}
	}
	return out
}
