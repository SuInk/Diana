// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/SuInk/diana/model/llm"
)

// 本地群：主人在控制台里建的群，成员是几台机器人（内置的、平台的都行），只存在
// Diana 里，不发到任何平台。
//
// 每台成员按自己的群会话看这个群（群号 room-<id>，会话按机器人分开，和几台机器人
// 在同一个真实群里时一样）：主人说的话按「对本机说话」交给每个成员，成员都回；
// 成员说的话作为群友发言交给其他成员，由各自的接话判断、人设和回复疲劳决定接不接。
//
// 机器人之间能一直接下去，所以要刹车：主人没插话时，机器人连续发言到 MaxBotTurns
// 条就不再往下分发（照样记进各成员的历史），等主人再说话。「停止讨论」取消正在想
// 的回复，并暂停到主人再说话。
const (
	localRoomGroupPrefix        = "room-"
	localRoomOwnerSender        = "owner"
	localRoomDefaultMaxBotTurns = 8
	localRoomMaxBotTurnsCap     = 50
	localRoomMaxMembers         = 12
	localRoomLogCap             = 300
	localRoomMaxText            = 4000
	localRoomReplyTimeout       = 3 * time.Minute
	localRoomPlanTimeout        = 90 * time.Second
	localRoomPlanMaxBots        = 8
)

// LocalRoom 是一个本地群。
type LocalRoom struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Members []string `json:"members"`
	// MaxBotTurns 是主人没插话时机器人最多连续发言几条，0 按默认 8。
	MaxBotTurns int   `json:"max_bot_turns"`
	CreatedAt   int64 `json:"created_at"`
}

// LocalRoomMessage 是群里的一条消息。SenderID 是 "owner" 或成员机器人 ID。
type LocalRoomMessage struct {
	ID         string   `json:"id"`
	SenderID   string   `json:"sender_id"`
	SenderName string   `json:"sender_name"`
	Text       string   `json:"text"`
	Images     []string `json:"images,omitempty"`
	At         int64    `json:"at"`
	// Kind 为 "call" 的是群通话记录（打了多久），不是谁说的话，不交给成员。
	Kind string `json:"kind,omitempty"`
}

// LocalRoomStatus 是群里讨论此刻的状态。
type LocalRoomStatus struct {
	// Thinking 是正在想回复的成员数。
	Thinking int `json:"thinking"`
	// BotStreak 是主人上次发言以来机器人连续发言的条数。
	BotStreak int `json:"bot_streak"`
	// Paused 为真时机器人之间不再接话，等主人说话：到了刹车条数或被停止。
	Paused bool `json:"paused"`
}

// LocalRoomStore 持久化本地群和群消息。
type LocalRoomStore interface {
	SaveLocalRooms(ctx context.Context, rooms []LocalRoom) error
	LoadLocalRooms(ctx context.Context) ([]LocalRoom, error)
	SaveLocalRoomMessages(ctx context.Context, roomID string, messages []LocalRoomMessage) error
	LoadLocalRoomMessages(ctx context.Context, roomID string) ([]LocalRoomMessage, error)
	DeleteLocalRoomMessages(ctx context.Context, roomID string) error
}

type localRoomHub struct {
	mu     sync.Mutex
	loaded bool
	rooms  []LocalRoom
	logs   map[string][]LocalRoomMessage
	live   map[string]*localRoomLive
	// persistMu 让落盘按顺序来，后写的总是最新状态。
	persistMu sync.Mutex
}

type localRoomLive struct {
	ctx       context.Context
	cancel    context.CancelFunc
	botStreak int
	paused    bool
	thinking  int
	// idle 在没有成员在想时关闭；有人开始想时换一个新的。
	idle    chan struct{}
	subs    map[int]func(LocalRoomMessage)
	nextSub int
	// calls 是正在进行的群通话数，大于 0 时成员按通话口吻回复。
	calls int
	// members 让同一台机器人在一个群里一次只跑一轮。
	members map[string]*sync.Mutex
}

func (r *Runtime) localRoomHub() *localRoomHub {
	r.localRoomsOnce.Do(func() {
		r.localRoomState = &localRoomHub{logs: map[string][]LocalRoomMessage{}, live: map[string]*localRoomLive{}}
	})
	return r.localRoomState
}

func (r *Runtime) localRoomStore() LocalRoomStore {
	r.mu.RLock()
	defer r.mu.RUnlock()
	store, _ := r.messageStore.(LocalRoomStore)
	return store
}

// ensureLoadedLocked 第一次用到时从库里读群列表。调用方持有 hub.mu。
func (r *Runtime) ensureLocalRoomsLoadedLocked(ctx context.Context, hub *localRoomHub) {
	if hub.loaded {
		return
	}
	hub.loaded = true
	store := r.localRoomStore()
	if store == nil {
		return
	}
	rooms, err := store.LoadLocalRooms(ctx)
	if err != nil {
		log.Printf("diana local rooms load failed: %v", err)
		return
	}
	hub.rooms = rooms
}

func (r *Runtime) localRoomLogLocked(ctx context.Context, hub *localRoomHub, roomID string) []LocalRoomMessage {
	if messages, ok := hub.logs[roomID]; ok {
		return messages
	}
	var messages []LocalRoomMessage
	if store := r.localRoomStore(); store != nil {
		loaded, err := store.LoadLocalRoomMessages(ctx, roomID)
		if err != nil {
			log.Printf("diana local room %s messages load failed: %v", roomID, err)
		}
		messages = loaded
	}
	hub.logs[roomID] = messages
	return messages
}

func (hub *localRoomHub) liveLocked(roomID string) *localRoomLive {
	live := hub.live[roomID]
	if live == nil {
		ctx, cancel := context.WithCancel(context.Background())
		idle := make(chan struct{})
		close(idle)
		live = &localRoomLive{ctx: ctx, cancel: cancel, idle: idle, subs: map[int]func(LocalRoomMessage){}, members: map[string]*sync.Mutex{}}
		hub.live[roomID] = live
	}
	return live
}

func findLocalRoom(rooms []LocalRoom, id string) (LocalRoom, int) {
	for i, room := range rooms {
		if room.ID == id {
			return room, i
		}
	}
	return LocalRoom{}, -1
}

func (room LocalRoom) maxBotTurns() int {
	if room.MaxBotTurns <= 0 {
		return localRoomDefaultMaxBotTurns
	}
	return min(room.MaxBotTurns, localRoomMaxBotTurnsCap)
}

// normalizeLocalRoom 整理名称、成员和刹车条数，成员必须是这台实例里启用着的机器人。
func (r *Runtime) normalizeLocalRoom(room LocalRoom) (LocalRoom, error) {
	room.Name = strings.TrimSpace(room.Name)
	if room.Name == "" {
		return room, errors.New("群名不能为空")
	}
	if len([]rune(room.Name)) > 40 {
		return room, errors.New("群名不能超过 40 个字")
	}
	var members []string
	for _, id := range room.Members {
		id = strings.TrimSpace(id)
		if id == "" || slices.Contains(members, id) {
			continue
		}
		r.mu.RLock()
		cfg, ok := r.profileConfigs[id]
		r.mu.RUnlock()
		if !ok {
			return room, fmt.Errorf("没有 ID 为 %s 的机器人", id)
		}
		if r.profileDisabled(id) {
			return room, fmt.Errorf("机器人「%s」已停用", firstNonEmpty(cfg.Name, id))
		}
		members = append(members, id)
	}
	if len(members) == 0 {
		return room, errors.New("至少拉一台机器人进群")
	}
	if len(members) > localRoomMaxMembers {
		return room, fmt.Errorf("一个群最多 %d 台机器人", localRoomMaxMembers)
	}
	room.Members = members
	if room.MaxBotTurns < 0 {
		room.MaxBotTurns = 0
	}
	room.MaxBotTurns = min(room.MaxBotTurns, localRoomMaxBotTurnsCap)
	return room, nil
}

func (r *Runtime) persistLocalRooms(hub *localRoomHub) {
	store := r.localRoomStore()
	if store == nil {
		return
	}
	hub.mu.Lock()
	rooms := append([]LocalRoom(nil), hub.rooms...)
	hub.mu.Unlock()
	hub.persistMu.Lock()
	defer hub.persistMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.SaveLocalRooms(ctx, rooms); err != nil {
		log.Printf("diana local rooms persist failed: %v", err)
	}
}

func (r *Runtime) persistLocalRoomLog(hub *localRoomHub, roomID string) {
	store := r.localRoomStore()
	if store == nil {
		return
	}
	hub.mu.Lock()
	messages := append([]LocalRoomMessage(nil), hub.logs[roomID]...)
	hub.mu.Unlock()
	hub.persistMu.Lock()
	defer hub.persistMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.SaveLocalRoomMessages(ctx, roomID, messages); err != nil {
		log.Printf("diana local room %s persist failed: %v", roomID, err)
	}
}

// LocalRooms 列出全部本地群，新建的在前。
func (r *Runtime) LocalRooms(ctx context.Context) []LocalRoom {
	hub := r.localRoomHub()
	hub.mu.Lock()
	defer hub.mu.Unlock()
	r.ensureLocalRoomsLoadedLocked(ctx, hub)
	out := append([]LocalRoom(nil), hub.rooms...)
	slices.SortStableFunc(out, func(a, b LocalRoom) int { return int(b.CreatedAt - a.CreatedAt) })
	return out
}

// CreateLocalRoom 建一个本地群。
func (r *Runtime) CreateLocalRoom(ctx context.Context, room LocalRoom) (LocalRoom, error) {
	room, err := r.normalizeLocalRoom(room)
	if err != nil {
		return room, err
	}
	room.ID = strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	room.CreatedAt = time.Now().Unix()
	hub := r.localRoomHub()
	hub.mu.Lock()
	r.ensureLocalRoomsLoadedLocked(ctx, hub)
	hub.rooms = append(hub.rooms, room)
	hub.logs[room.ID] = nil
	hub.mu.Unlock()
	r.persistLocalRooms(hub)
	return room, nil
}

// UpdateLocalRoom 改群名、成员和刹车条数。
func (r *Runtime) UpdateLocalRoom(ctx context.Context, room LocalRoom) (LocalRoom, error) {
	normalized, err := r.normalizeLocalRoom(room)
	if err != nil {
		return room, err
	}
	hub := r.localRoomHub()
	hub.mu.Lock()
	r.ensureLocalRoomsLoadedLocked(ctx, hub)
	current, index := findLocalRoom(hub.rooms, room.ID)
	if index < 0 {
		hub.mu.Unlock()
		return room, errors.New("群不存在")
	}
	normalized.ID, normalized.CreatedAt = current.ID, current.CreatedAt
	hub.rooms[index] = normalized
	hub.mu.Unlock()
	r.persistLocalRooms(hub)
	return normalized, nil
}

// DeleteLocalRoom 解散本地群：停下讨论，删掉群和群消息。成员各自历史里的记录不动。
func (r *Runtime) DeleteLocalRoom(ctx context.Context, id string) error {
	hub := r.localRoomHub()
	hub.mu.Lock()
	r.ensureLocalRoomsLoadedLocked(ctx, hub)
	_, index := findLocalRoom(hub.rooms, id)
	if index < 0 {
		hub.mu.Unlock()
		return errors.New("群不存在")
	}
	hub.rooms = slices.Delete(hub.rooms, index, index+1)
	delete(hub.logs, id)
	if live := hub.live[id]; live != nil {
		live.cancel()
		delete(hub.live, id)
	}
	hub.mu.Unlock()
	r.persistLocalRooms(hub)
	if store := r.localRoomStore(); store != nil {
		if err := store.DeleteLocalRoomMessages(ctx, id); err != nil {
			log.Printf("diana local room %s delete messages failed: %v", id, err)
		}
	}
	return nil
}

// LocalRoomHistory 返回群、群消息（旧的在前）和讨论状态。
func (r *Runtime) LocalRoomHistory(ctx context.Context, id string) (LocalRoom, []LocalRoomMessage, LocalRoomStatus, error) {
	hub := r.localRoomHub()
	hub.mu.Lock()
	defer hub.mu.Unlock()
	r.ensureLocalRoomsLoadedLocked(ctx, hub)
	room, index := findLocalRoom(hub.rooms, id)
	if index < 0 {
		return room, nil, LocalRoomStatus{}, errors.New("群不存在")
	}
	messages := append([]LocalRoomMessage(nil), r.localRoomLogLocked(ctx, hub, id)...)
	return room, messages, hub.statusLocked(room), nil
}

func (hub *localRoomHub) statusLocked(room LocalRoom) LocalRoomStatus {
	live := hub.live[room.ID]
	if live == nil {
		return LocalRoomStatus{}
	}
	return LocalRoomStatus{Thinking: live.thinking, BotStreak: live.botStreak, Paused: live.paused || live.botStreak >= room.maxBotTurns()}
}

// StopLocalRoom 停止讨论：正在想的回复作废，机器人之间不再接话，等主人再说话。
func (r *Runtime) StopLocalRoom(ctx context.Context, id string) (LocalRoomStatus, error) {
	hub := r.localRoomHub()
	hub.mu.Lock()
	defer hub.mu.Unlock()
	r.ensureLocalRoomsLoadedLocked(ctx, hub)
	room, index := findLocalRoom(hub.rooms, id)
	if index < 0 {
		return LocalRoomStatus{}, errors.New("群不存在")
	}
	live := hub.liveLocked(id)
	live.cancel()
	live.ctx, live.cancel = context.WithCancel(context.Background())
	live.paused = true
	return hub.statusLocked(room), nil
}

// LocalRoomSend 主人在群里说一句：交给每个成员，成员的发言再按刹车规则交给其他
// 成员。emit 收到这一轮讨论里出现的每条消息（不含主人这句），讨论停下（没有成员
// 在想）或 ctx 结束时返回。讨论本身不跟着 ctx 走：页面关了，正在想的照样说完。
func (r *Runtime) LocalRoomSend(ctx context.Context, id, text string, emit func(LocalRoomMessage)) (LocalRoomMessage, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return LocalRoomMessage{}, errors.New("消息是空的")
	}
	if len([]rune(text)) > localRoomMaxText {
		return LocalRoomMessage{}, fmt.Errorf("消息不能超过 %d 字", localRoomMaxText)
	}
	hub := r.localRoomHub()
	hub.mu.Lock()
	r.ensureLocalRoomsLoadedLocked(ctx, hub)
	room, index := findLocalRoom(hub.rooms, id)
	if index < 0 {
		hub.mu.Unlock()
		return LocalRoomMessage{}, errors.New("群不存在")
	}
	live := hub.liveLocked(id)
	// 主人说话：刹车清零，停止状态解除。
	live.botStreak, live.paused = 0, false
	subID := live.nextSub
	live.nextSub++
	if emit != nil {
		live.subs[subID] = emit
	}
	hub.mu.Unlock()
	defer func() {
		hub.mu.Lock()
		delete(live.subs, subID)
		hub.mu.Unlock()
	}()

	msg := LocalRoomMessage{ID: "room-in-" + strconv.FormatInt(time.Now().UnixNano(), 36), SenderID: localRoomOwnerSender, SenderName: "主人", Text: text, At: time.Now().Unix()}
	r.appendLocalRoomMessage(room, msg, false)
	r.dispatchLocalRoomMessage(room, msg)

	hub.mu.Lock()
	idle := live.idle
	hub.mu.Unlock()
	select {
	case <-idle:
	case <-ctx.Done():
	}
	return msg, nil
}

// appendLocalRoomMessage 记进群消息、推给正在看的页面。bot 为真时计一条机器人连续发言。
func (r *Runtime) appendLocalRoomMessage(room LocalRoom, msg LocalRoomMessage, bot bool) {
	hub := r.localRoomHub()
	hub.mu.Lock()
	messages := append(r.localRoomLogLocked(context.Background(), hub, room.ID), msg)
	if len(messages) > localRoomLogCap {
		messages = append([]LocalRoomMessage(nil), messages[len(messages)-localRoomLogCap:]...)
	}
	hub.logs[room.ID] = messages
	live := hub.liveLocked(room.ID)
	if bot {
		live.botStreak++
	}
	subs := make([]func(LocalRoomMessage), 0, len(live.subs))
	for _, sub := range live.subs {
		subs = append(subs, sub)
	}
	hub.mu.Unlock()
	for _, sub := range subs {
		sub(msg)
	}
	r.persistLocalRoomLog(hub, room.ID)
}

// dispatchLocalRoomMessage 把一条群消息交给除发言人外的每个成员。机器人的发言到了
// 刹车条数（或讨论被停止）后只记进成员历史，不再让成员回。
func (r *Runtime) dispatchLocalRoomMessage(room LocalRoom, msg LocalRoomMessage) {
	hub := r.localRoomHub()
	hub.mu.Lock()
	current, index := findLocalRoom(hub.rooms, room.ID)
	if index < 0 {
		hub.mu.Unlock()
		return
	}
	room = current
	live := hub.liveLocked(room.ID)
	fromOwner := msg.SenderID == localRoomOwnerSender
	reply := fromOwner || !live.paused && live.botStreak < room.maxBotTurns()
	ctx := live.ctx
	hub.mu.Unlock()

	for _, member := range room.Members {
		if member == msg.SenderID {
			continue
		}
		event, err := r.localRoomEvent(room, member, msg)
		if err != nil {
			log.Printf("diana local room %s skip member %s: %v", room.ID, member, err)
			continue
		}
		if !reply {
			r.remember(r.bindInboundEventIdentity(event))
			continue
		}
		hub.mu.Lock()
		if live.thinking == 0 {
			live.idle = make(chan struct{})
		}
		live.thinking++
		lock := live.members[member]
		if lock == nil {
			lock = &sync.Mutex{}
			live.members[member] = lock
		}
		hub.mu.Unlock()
		go func(member string, event MessageEvent) {
			defer recoverGoroutinePanic("local_room.reply")
			defer func() {
				hub.mu.Lock()
				live.thinking--
				if live.thinking == 0 {
					close(live.idle)
				}
				hub.mu.Unlock()
			}()
			lock.Lock()
			defer lock.Unlock()
			// 排队时讨论被停止了：只记历史，不回。
			if ctx.Err() != nil {
				r.remember(r.bindInboundEventIdentity(event))
				return
			}
			r.replyInLocalRoom(ctx, room, member, event)
		}(member, event)
	}
}

// localRoomEvent 造出成员 member 眼里的这条群消息。
func (r *Runtime) localRoomEvent(room LocalRoom, member string, msg LocalRoomMessage) (MessageEvent, error) {
	r.mu.RLock()
	cfg, ok := r.profileConfigs[member]
	r.mu.RUnlock()
	if !ok {
		return MessageEvent{}, errors.New("机器人不存在")
	}
	cfg = cfg.WithDefaults()
	segments := []MessageSegment{{Type: "text", Data: map[string]string{"text": msg.Text}}}
	for _, url := range msg.Images {
		segments = append(segments, MessageSegment{Type: "image", Data: map[string]string{"url": url}})
	}
	event := MessageEvent{
		ProfileID:        cfg.ID,
		ContextNamespace: cfg.ID,
		Platform:         NormalizePlatformID(cfg.Platform),
		Kind:             EventKindGroup,
		MessageType:      string(EventKindGroup),
		GroupID:          localRoomGroupPrefix + room.ID,
		GroupName:        room.Name,
		SelfID:           localRoomMemberAccount(cfg),
		MessageID:        msg.ID,
		Time:             msg.At,
		RawMessage:       msg.Text,
		Segments:         segments,
		webChat:          true,
	}
	if r.localRoomCalling(room.ID) {
		event.voiceCall, event.roomCall = true, true
	}
	if msg.SenderID == localRoomOwnerSender {
		_, owner, err := r.ownerChatEvent(member)
		if err != nil {
			return MessageEvent{}, err
		}
		event.UserID, event.SenderUsername, event.SenderName = owner.UserID, owner.SenderUsername, "主人"
		// 主人在群里说话就是对每个成员说：你一发言他们就发言。
		event.ToMe = true
		return event, nil
	}
	r.mu.RLock()
	sender, ok := r.profileConfigs[msg.SenderID]
	r.mu.RUnlock()
	if !ok {
		return MessageEvent{}, errors.New("发言的机器人不存在了")
	}
	event.UserID = localRoomMemberAccount(sender.WithDefaults())
	event.SenderName = firstNonEmpty(msg.SenderName, sender.Name)
	return event, nil
}

// localRoomMemberAccount 是机器人在本地群里的账号：填了机器人账号就用它，否则按机器人
// ID 编一个，保证成员之间的发言分得开。
func localRoomMemberAccount(cfg BotConfig) string {
	return firstNonEmpty(strings.TrimSpace(cfg.BotAccount), "local-"+strings.TrimSpace(cfg.ID))
}

// replyInLocalRoom 让成员走一轮群聊回复流程：接话判断、人设、回复疲劳都照常，回复
// 不发到平台，交给群。
func (r *Runtime) replyInLocalRoom(ctx context.Context, room LocalRoom, member string, event MessageEvent) {
	ctx, cancel := context.WithTimeout(ctx, localRoomReplyTimeout)
	defer cancel()
	r.mu.RLock()
	cfg := r.profileConfigs[member]
	r.mu.RUnlock()
	name := firstNonEmpty(strings.TrimSpace(cfg.Name), member)
	sink := func(out WebChatMessage) {
		if ctx.Err() != nil {
			return
		}
		reply := LocalRoomMessage{ID: out.ID, SenderID: member, SenderName: name, Text: out.Text, Images: out.Images, At: out.At}
		r.appendLocalRoomMessage(room, reply, true)
		r.dispatchLocalRoomMessage(room, reply)
	}
	event = r.bindInboundEventIdentity(event)
	ctx = withWebChatSink(ctx, sink)
	ctx = withLLMUsageContext(ctx, event)
	ctx = r.withDebugTraceContext(ctx, event)
	ctx = withContextBudgetCap(ctx, r.effectiveConfigForEvent(event).MaxContextTokens)
	if event.ToMe {
		r.noteDirectedInbound(event)
	}
	r.noteSenderTurnArrival(event)
	prepared, routedText, handled, outcome := r.prepareMessageEvent(ctx, event)
	if !handled {
		r.finishSenderTurn(event)
		return
	}
	select {
	case r.sem <- struct{}{}:
		r.incActive(1)
	case <-ctx.Done():
		r.finishSenderTurn(event)
		return
	}
	defer func() {
		<-r.sem
		r.incActive(-1)
	}()
	if _, err := r.replyAndRecord(ctx, prepared, routedText, outcome); err != nil && ctx.Err() == nil {
		log.Printf("diana local room %s member %s reply failed: %v", room.ID, member, err)
	}
}

// LocalRoomPlan 是「一句话建群」解析出来的群和成员。
type LocalRoomPlan struct {
	RoomName string             `json:"room_name"`
	Bots     []LocalRoomPlanBot `json:"bots"`
}

// LocalRoomPlanBot 是要新建的一台内置机器人。
type LocalRoomPlanBot struct {
	Name string `json:"name"`
	// Persona 是这台机器人的人设（SOUL.md），写进 SystemPrompt。
	Persona string `json:"persona"`
}

const localRoomPlanPrompt = `主人想在控制台里建一个群，拉几台新机器人进来一起聊天。根据主人的话，给出群名和每台机器人的名字、人设。

只输出一个 JSON 对象，不要代码块、不要解释：
{"room_name": "群名", "bots": [{"name": "名字", "persona": "人设"}]}

要求：
- 主人点了名的就用主人给的名字，按主人说的顺序；主人只说了数量没给名字时，自己起贴切的名字。最多 8 台。
- 名字是现成角色（动漫、游戏、小说里的人物）时，按这个角色广为人知的性格、口头禅、和群里其他人的关系来写人设；不认识的就按名字和主人的描述合理编一个。
- persona 用第二人称写给这台机器人看，150 到 300 字：你是谁、性格、说话方式（口癖、语气、长短）、和群里其他成员的关系、在群聊里的习惯。写成能直接当人设用的段落，不要列表。
- room_name 简短，主人说了群名就用主人的。`

// PlanLocalRoom 让模型把主人的一句话拆成群名和几台带人设的机器人。template 是
// 拿来借用模型分配的那台机器人。
func (r *Runtime) PlanLocalRoom(ctx context.Context, template, request string) (LocalRoomPlan, error) {
	request = strings.TrimSpace(request)
	if request == "" {
		return LocalRoomPlan{}, errors.New("说说要建什么样的群")
	}
	_, event, err := r.ownerChatEvent(template)
	if err != nil {
		return LocalRoomPlan{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, localRoomPlanTimeout)
	defer cancel()
	ctx = withModelConfigEvent(ctx, event)
	ctx = withLLMUsagePurpose(withLLMUsageContext(ctx, event), "local_room_plan")
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: localRoomPlanPrompt},
		{Role: llm.RoleUser, Content: request},
	}
	raw, err := r.runLLMProviderForGroup(ctx, llm.GroupBackground, func(client LLMProvider) (string, error) {
		resp, err := client.Generate(ctx, llm.GenerateRequest{Messages: messages})
		if err != nil {
			return "", err
		}
		return resp.Text, nil
	})
	if err != nil {
		return LocalRoomPlan{}, err
	}
	return parseLocalRoomPlan(raw)
}

func parseLocalRoomPlan(raw string) (LocalRoomPlan, error) {
	raw = strings.TrimSpace(raw)
	if start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}"); start >= 0 && end > start {
		raw = raw[start : end+1]
	}
	var plan LocalRoomPlan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		return plan, fmt.Errorf("模型没给出能用的建群方案：%w", err)
	}
	plan.RoomName = strings.TrimSpace(plan.RoomName)
	var bots []LocalRoomPlanBot
	for _, bot := range plan.Bots {
		bot.Name, bot.Persona = strings.TrimSpace(bot.Name), strings.TrimSpace(bot.Persona)
		if bot.Name == "" || len(bots) >= localRoomPlanMaxBots {
			continue
		}
		bots = append(bots, bot)
	}
	if len(bots) == 0 {
		return plan, errors.New("模型没给出任何机器人")
	}
	plan.Bots = bots
	if plan.RoomName == "" {
		plan.RoomName = bots[0].Name + "的群"
	}
	return plan, nil
}
