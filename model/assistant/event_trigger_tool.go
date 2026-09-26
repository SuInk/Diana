// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	dianaEventTriggerToolName = "event_trigger"

	eventTriggerWhereHere     = "here"
	eventTriggerWhereGroup    = "group"
	eventTriggerWhereAnywhere = "anywhere"

	maximumEventTriggerKeywordRunes = 50
)

type dianaEventTriggerTool struct {
	runtime *Runtime
	event   MessageEvent
}

type dianaEventTrigger struct {
	ID          string    `json:"id"`
	Summary     string    `json:"summary"`
	Action      string    `json:"action"`
	Message     string    `json:"message"`
	DeliverTo   string    `json:"deliver_to"`
	Repeat      bool      `json:"repeat"`
	Status      string    `json:"status"`
	FireCount   int       `json:"fire_count"`
	LastFiredAt time.Time `json:"last_fired_at,omitempty"`
	ExpiresAt   time.Time `json:"expires_at,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
}

type dianaEventTriggerResult struct {
	OK      bool                `json:"ok"`
	Action  string              `json:"action"`
	Message string              `json:"message,omitempty"`
	Trigger *dianaEventTrigger  `json:"trigger,omitempty"`
	Items   []dianaEventTrigger `json:"items,omitempty"`
}

func newDianaEventTriggerTool(runtime *Runtime, event MessageEvent) *dianaEventTriggerTool {
	return &dianaEventTriggerTool{runtime: runtime, event: event}
}

func (t *dianaEventTriggerTool) Name() string {
	return dianaEventTriggerToolName
}

func (t *dianaEventTriggerTool) Description() string {
	return `创建和管理持久化的事件触发任务：「某人下次说话时提醒他」「有人提到某个词时做某事」「有人进群时做某事」。条件满足时发一句固定文本，或者跑一轮带工具的 agent 按指令去做。按时间点触发的改用 reminder，按固定间隔重复的改用 subscription。不得用口头答应、run_command 或后台进程代替。`
}

func (t *dianaEventTriggerTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("要执行的操作。cancel 只停止并保留记录，delete 才彻底删除。",
			"create", "list", "cancel", "delete"),
		"event": toolEnumParam("触发事件：message 是有人发消息（默认），member_join 是有人进群。",
			eventTriggerEventMessage, eventTriggerEventMemberJoin),
		"where": toolEnumParam("在哪里盯：here 是当前会话（默认）；group 是 group_id 指定的群；anywhere 是这台机器人所在的任何群和私聊。group 和 anywhere 仅主人可用。",
			eventTriggerWhereHere, eventTriggerWhereGroup, eventTriggerWhereAnywhere),
		"group_id": toolStringParam("where=group 时要盯的群号。"),
		"users":    toolStringArrayParam("触发者账号列表，最多 " + itoa(maximumEventTriggerUsers) + " 个。不传默认只有当前说话的人；\"*\" 表示任何人。指定别人或 \"*\" 仅主人可用。"),
		"keywords": toolStringArrayParam("可选：消息里含任一关键词才触发（不区分大小写），最多 " + itoa(maximumEventTriggerKeywords) + " 个。"),
		"pattern":  toolStringParam("可选：消息须匹配的 Go 正则，最多 " + itoa(maximumEventTriggerPattern) + " 个字符。和 keywords 同时给时两者都要满足。"),
		"action": toolEnumParam("触发后做什么：message 原样发出 message 里的文本（默认）；agent 把 message 当成指令，结合触发消息跑一轮带工具的 agent，把结果发出去。",
			eventTriggerActionMessage, eventTriggerActionAgent),
		"message": toolStringParam("action=message 时是要发的文本，action=agent 时是要执行的指令，最多 " + itoa(maximumReminderMessageRunes) + " 个字符。create 必填。"),
		"deliver_to": toolEnumParam("结果发到哪：event 是事件发生的会话，并引用、@ 触发者（默认）；origin 是现在这条会话，适合「他上线了告诉我」。",
			eventTriggerDeliverEvent, eventTriggerDeliverOrigin),
		"repeat":     toolBoolParam("true 表示每次满足条件都触发；默认 false，触发一次就用掉。"),
		"cooldown":   toolStringParam("repeat=true 时两次触发的最短间隔，Go 时长写法，默认 " + defaultEventTriggerCooldown.String() + "，范围 " + minimumEventTriggerCooldown.String() + " 到 " + maximumEventTriggerCooldown.String() + "。"),
		"expires_in": toolStringParam("多久之后自动失效，Go 时长写法，默认 " + defaultEventTriggerLifetime.String() + "，最长 " + maximumEventTriggerLifetime.String() + "。到期一次都没触发会告诉设置的人。"),
		"id":         toolStringParam("要取消或删除的任务 ID；可先用 list 查到。"),
	})
}

// 创建分两档：只盯当前会话（where 省略或 here）是 create；盯别的群或任何地方是
// create_elsewhere。后者等于一条随时往别的会话发话、甚至在别处跑 Agent 的通道，
// 安全模式按这个名字只关它，见 AgentSafeModeRules。
const (
	eventTriggerOpCreate          = "create"
	eventTriggerOpCreateElsewhere = "create_elsewhere"
)

// CanonicalOperation 是 Run 实际执行的操作：add 算 create，remove 算 delete，
// 创建再按 where 分成 create 和 create_elsewhere。按操作拦截时用同一套换算。
func (t *dianaEventTriggerTool) CanonicalOperation(input map[string]any) string {
	switch operation := strings.ToLower(strings.TrimSpace(configToolString(input, "operation"))); operation {
	case "create", "add":
		switch strings.ToLower(strings.TrimSpace(configToolString(input, "where"))) {
		case "", eventTriggerWhereHere:
			return eventTriggerOpCreate
		}
		return eventTriggerOpCreateElsewhere
	case "remove":
		return "delete"
	default:
		return operation
	}
}

func (t *dianaEventTriggerTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("diana event trigger: runtime is not configured")
	}
	ownerID := strings.TrimSpace(t.event.UserID)
	// 按 id 取消、删除之前先认归属：别的机器人名下的触发任务按找不到处理，见 taskOfOtherBot。
	if id := strings.TrimSpace(configToolString(input, "id")); id != "" && t.runtime.taskOfOtherBot(id, t.event) {
		return "", fmt.Errorf("没有找到事件触发任务 %s", id)
	}
	switch t.CanonicalOperation(input) {
	case eventTriggerOpCreate, eventTriggerOpCreateElsewhere:
		if eventTriggerRunFromContext(ctx) {
			return "", fmt.Errorf("触发任务执行期间不能再创建触发任务")
		}
		owner := t.runtime.relationshipPolicy(ctx, t.event).Owner
		spec, message, err := parseEventTriggerCreate(input, t.event, owner, time.Now())
		if err != nil {
			return "", err
		}
		item, err := t.runtime.addEventTrigger(ctx, t.event, spec, message)
		if err != nil {
			return "", err
		}
		return marshalDianaEventTriggerResult(dianaEventTriggerResult{
			OK: true, Action: "created", Message: "已创建并持久化事件触发任务：" + eventTriggerSummary(item, spec) + "。",
			Trigger: eventTriggerForTool(item),
		})
	case "list":
		items := t.runtime.eventTriggers(ownerID)
		out := make([]dianaEventTrigger, 0, len(items))
		for _, item := range items {
			if view := eventTriggerForTool(item); view != nil {
				out = append(out, *view)
			}
		}
		return marshalDianaEventTriggerResult(dianaEventTriggerResult{
			OK: true, Action: "listed", Message: fmt.Sprintf("当前共有 %d 个事件触发任务。", len(out)), Items: out,
		})
	case "cancel":
		id := strings.TrimSpace(configToolString(input, "id"))
		if id == "" {
			return "", fmt.Errorf("取消触发任务时必须提供 id")
		}
		item, err := t.runtime.cancelEventTrigger(ownerID, id)
		if err != nil {
			return "", err
		}
		return marshalDianaEventTriggerResult(dianaEventTriggerResult{
			OK: true, Action: "cancelled", Message: "事件触发任务已取消并释放额度，记录仍保留。", Trigger: eventTriggerForTool(item),
		})
	case "delete":
		id := strings.TrimSpace(configToolString(input, "id"))
		if id == "" {
			return "", fmt.Errorf("删除触发任务时必须提供 id")
		}
		removed, err := t.runtime.deleteEventTrigger(ownerID, id)
		if err != nil {
			return "", err
		}
		if !removed {
			return "", fmt.Errorf("没有找到属于当前用户的事件触发任务 %s", id)
		}
		return marshalDianaEventTriggerResult(dianaEventTriggerResult{OK: true, Action: "deleted", Message: "事件触发任务已删除。"})
	default:
		return "", fmt.Errorf("operation 必须是 create、list、cancel 或 delete")
	}
}

// parseEventTriggerCreate 校验参数并补默认值。权限边界都在这里判：非主人只能盯
// 当前会话里的自己——替别人设触发器，就是让机器人去盯一个没同意被盯的人。
func parseEventTriggerCreate(input map[string]any, event MessageEvent, owner bool, now time.Time) (EventTrigger, string, error) {
	spec := EventTrigger{
		Event:     strings.ToLower(strings.TrimSpace(configToolString(input, "event"))),
		Action:    strings.ToLower(strings.TrimSpace(configToolString(input, "action"))),
		DeliverTo: strings.ToLower(strings.TrimSpace(configToolString(input, "deliver_to"))),
		Repeat:    toolInputBool(input, "repeat"),
	}
	if spec.Event == "" {
		spec.Event = eventTriggerEventMessage
	}
	if spec.Event != eventTriggerEventMessage && spec.Event != eventTriggerEventMemberJoin {
		return EventTrigger{}, "", fmt.Errorf("event 必须是 message 或 member_join")
	}
	if spec.Action == "" {
		spec.Action = eventTriggerActionMessage
	}
	if spec.Action != eventTriggerActionMessage && spec.Action != eventTriggerActionAgent {
		return EventTrigger{}, "", fmt.Errorf("action 必须是 message 或 agent")
	}
	if spec.DeliverTo == "" {
		spec.DeliverTo = eventTriggerDeliverEvent
	}
	if spec.DeliverTo != eventTriggerDeliverEvent && spec.DeliverTo != eventTriggerDeliverOrigin {
		return EventTrigger{}, "", fmt.Errorf("deliver_to 必须是 event 或 origin")
	}

	message := strings.TrimSpace(configToolString(input, "message"))
	if message == "" {
		return EventTrigger{}, "", fmt.Errorf("message 不能为空")
	}
	if len([]rune(message)) > maximumReminderMessageRunes {
		return EventTrigger{}, "", fmt.Errorf("message 不能超过 %d 个字符", maximumReminderMessageRunes)
	}

	self := strings.TrimSpace(event.UserID)
	inPrivate := strings.TrimSpace(event.GroupID) == ""
	where := strings.ToLower(strings.TrimSpace(configToolString(input, "where")))
	switch where {
	case "", eventTriggerWhereHere:
		if inPrivate && spec.Event == eventTriggerEventMemberJoin {
			return EventTrigger{}, "", fmt.Errorf("私聊里没有进群事件；要盯某个群的进群，传 where=group 和 group_id")
		}
	case eventTriggerWhereGroup:
		spec.WatchGroupID = strings.TrimSpace(configToolString(input, "group_id"))
		if spec.WatchGroupID == "" {
			return EventTrigger{}, "", fmt.Errorf("where=group 时必须提供 group_id")
		}
	case eventTriggerWhereAnywhere:
		spec.WatchAnywhere = true
	default:
		return EventTrigger{}, "", fmt.Errorf("where 必须是 here、group 或 anywhere")
	}
	watchesHere := !spec.WatchAnywhere && spec.WatchGroupID == ""
	if !owner && !watchesHere {
		return EventTrigger{}, "", fmt.Errorf("只有主人可以盯当前会话以外的地方")
	}

	users, err := toolStringValues(input["users"])
	if err != nil {
		return EventTrigger{}, "", fmt.Errorf("users %w", err)
	}
	for _, raw := range users {
		user := strings.TrimSpace(raw)
		if user == eventTriggerAnyUser {
			spec.UserIDs = []string{eventTriggerAnyUser}
			break
		}
		if user = normalizeRelationshipUserID(user); user != "" && !containsString(spec.UserIDs, user) {
			spec.UserIDs = append(spec.UserIDs, user)
		}
	}
	if len(spec.UserIDs) == 0 {
		if self == "" {
			return EventTrigger{}, "", fmt.Errorf("拿不到当前说话的人，users 必须显式给出")
		}
		spec.UserIDs = []string{self}
	}
	if len(spec.UserIDs) > maximumEventTriggerUsers {
		return EventTrigger{}, "", fmt.Errorf("users 最多 %d 个", maximumEventTriggerUsers)
	}
	onlySelf := len(spec.UserIDs) == 1 && spec.UserIDs[0] == self
	if !owner && !onlySelf {
		return EventTrigger{}, "", fmt.Errorf("只有主人可以替别人或任何人设触发任务；你只能设自己相关的")
	}
	if watchesHere && inPrivate && !onlySelf {
		return EventTrigger{}, "", fmt.Errorf("私聊里只有你自己会说话；要盯别人，传 where=group 或 where=anywhere")
	}

	keywords, err := toolStringValues(input["keywords"])
	if err != nil {
		return EventTrigger{}, "", fmt.Errorf("keywords %w", err)
	}
	for _, raw := range keywords {
		keyword := strings.TrimSpace(raw)
		if keyword == "" || containsString(spec.Keywords, keyword) {
			continue
		}
		if len([]rune(keyword)) > maximumEventTriggerKeywordRunes {
			return EventTrigger{}, "", fmt.Errorf("关键词不能超过 %d 个字符", maximumEventTriggerKeywordRunes)
		}
		spec.Keywords = append(spec.Keywords, keyword)
	}
	if len(spec.Keywords) > maximumEventTriggerKeywords {
		return EventTrigger{}, "", fmt.Errorf("keywords 最多 %d 个", maximumEventTriggerKeywords)
	}
	spec.Pattern = strings.TrimSpace(configToolString(input, "pattern"))
	if len([]rune(spec.Pattern)) > maximumEventTriggerPattern {
		return EventTrigger{}, "", fmt.Errorf("pattern 不能超过 %d 个字符", maximumEventTriggerPattern)
	}
	if spec.Pattern != "" {
		if _, err := regexp.Compile(spec.Pattern); err != nil {
			return EventTrigger{}, "", fmt.Errorf("pattern 不是合法的正则：%v", err)
		}
	}
	if spec.Event == eventTriggerEventMemberJoin && (len(spec.Keywords) > 0 || spec.Pattern != "") {
		return EventTrigger{}, "", fmt.Errorf("进群事件没有消息文本，不能带 keywords 或 pattern")
	}

	if spec.Repeat {
		cooldown := defaultEventTriggerCooldown
		if raw := strings.TrimSpace(configToolString(input, "cooldown")); raw != "" {
			cooldown, err = time.ParseDuration(strings.ToLower(raw))
			if err != nil || cooldown < minimumEventTriggerCooldown || cooldown > maximumEventTriggerCooldown {
				return EventTrigger{}, "", fmt.Errorf("cooldown 须是 %s 到 %s 之间的 Go 时长", minimumEventTriggerCooldown, maximumEventTriggerCooldown)
			}
		}
		spec.CooldownSeconds = int64(cooldown / time.Second)
	}
	lifetime := defaultEventTriggerLifetime
	if raw := strings.TrimSpace(configToolString(input, "expires_in")); raw != "" {
		lifetime, err = time.ParseDuration(strings.ToLower(raw))
		if err != nil || lifetime <= 0 || lifetime > maximumEventTriggerLifetime {
			return EventTrigger{}, "", fmt.Errorf("expires_in 须是不超过 %s 的正 Go 时长", maximumEventTriggerLifetime)
		}
	}
	spec.ExpiresAt = now.Add(lifetime)
	return spec, message, nil
}

func (r *Runtime) addEventTrigger(ctx context.Context, event MessageEvent, spec EventTrigger, message string) (Reminder, error) {
	if r.reminders == nil {
		return Reminder{}, fmt.Errorf("当前未启用提醒功能")
	}
	if spec.Action == eventTriggerActionAgent && !r.effectiveConfigForEvent(event).AgentEnabled {
		return Reminder{}, fmt.Errorf("Agent 已禁用，不能创建 agent 动作的触发任务")
	}
	limit := r.relationshipPolicy(ctx, event).personalScheduleLimit()
	now := time.Now()
	r.reminderMu.Lock()
	defer r.reminderMu.Unlock()
	items := r.reminders.Reminders()
	count := 0
	for _, existing := range items {
		if existing.OwnerID != event.UserID {
			continue
		}
		if existingSpec, ok := EventTriggerSpec(existing); ok && eventTriggerArmed(existing, existingSpec, now) {
			count++
		}
	}
	if count >= limit {
		return Reminder{}, fmt.Errorf("当前最多可以有 %d 个生效中的事件触发任务，额度已满", limit)
	}
	item := Reminder{
		ID:               uuid.NewString()[:8],
		Kind:             ReminderKindEventTrigger,
		Platform:         event.Platform,
		ProfileID:        event.ProfileID,
		ContextNamespace: event.ContextNamespace,
		OwnerID:          event.UserID,
		GroupID:          event.GroupID,
		UserID:           event.UserID,
		Message:          message,
		EventTriggerJSON: encodeEventTrigger(spec),
		CreatedAt:        now,
	}
	if err := r.reminders.SaveReminders(append(items, item)); err != nil {
		return Reminder{}, fmt.Errorf("保存触发任务失败: %w", err)
	}
	return item, nil
}

func (r *Runtime) eventTriggers(ownerID string) []Reminder {
	if r.reminders == nil {
		return nil
	}
	r.reminderMu.Lock()
	defer r.reminderMu.Unlock()
	out := make([]Reminder, 0)
	for _, item := range r.reminders.Reminders() {
		if reminderIsEventTrigger(item) && item.OwnerID == ownerID {
			out = append(out, item)
		}
	}
	return out
}

// CancelEventTrigger 停掉一个触发任务，记录保留。WebUI 和聊天工具共用。
func (r *Runtime) CancelEventTrigger(ownerID, id string) (Reminder, error) {
	return r.cancelEventTrigger(ownerID, id)
}

// DeleteEventTrigger 彻底删除一个触发任务。
func (r *Runtime) DeleteEventTrigger(ownerID, id string) (bool, error) {
	return r.deleteEventTrigger(ownerID, id)
}

func (r *Runtime) cancelEventTrigger(ownerID, id string) (Reminder, error) {
	if r.reminders == nil {
		return Reminder{}, fmt.Errorf("当前未启用提醒功能")
	}
	r.reminderMu.Lock()
	defer r.reminderMu.Unlock()
	items := r.reminders.Reminders()
	for index := range items {
		item := &items[index]
		if !reminderIsEventTrigger(*item) || item.OwnerID != ownerID || item.ID != id {
			continue
		}
		if !item.CancelledAt.IsZero() {
			return Reminder{}, fmt.Errorf("触发任务 %s 已经停止", id)
		}
		item.CancelledAt = time.Now()
		if err := r.reminders.SaveReminders(items); err != nil {
			return Reminder{}, fmt.Errorf("取消触发任务失败: %w", err)
		}
		return *item, nil
	}
	return Reminder{}, fmt.Errorf("没有找到属于当前用户的事件触发任务 %s", id)
}

func (r *Runtime) deleteEventTrigger(ownerID, id string) (bool, error) {
	if r.reminders == nil {
		return false, fmt.Errorf("当前未启用提醒功能")
	}
	r.reminderMu.Lock()
	defer r.reminderMu.Unlock()
	items := r.reminders.Reminders()
	next := make([]Reminder, 0, len(items))
	removed := false
	for _, item := range items {
		if reminderIsEventTrigger(item) && item.OwnerID == ownerID && item.ID == id {
			removed = true
			continue
		}
		next = append(next, item)
	}
	if !removed {
		return false, nil
	}
	if err := r.reminders.SaveReminders(next); err != nil {
		return false, fmt.Errorf("删除触发任务失败: %w", err)
	}
	return true, nil
}

// eventTriggerSummary 把条件写成一句人话，列表、工具返回和 WebUI 共用。
func eventTriggerSummary(item Reminder, spec EventTrigger) string {
	var where string
	switch {
	case spec.WatchAnywhere:
		where = "任何会话里"
	case spec.WatchGroupID != "":
		where = "群 " + spec.WatchGroupID + " 里"
	case strings.TrimSpace(item.GroupID) != "":
		where = "群 " + strings.TrimSpace(item.GroupID) + " 里"
	default:
		where = "私聊里"
	}
	who := strings.Join(spec.UserIDs, "、") + " "
	if len(spec.UserIDs) == 1 && spec.UserIDs[0] == eventTriggerAnyUser {
		who = "任何人"
	}
	what := "说话"
	if spec.Event == eventTriggerEventMemberJoin {
		what = "进群"
	}
	parts := []string{where, who + what + "时"}
	if len(spec.Keywords) > 0 {
		parts = append(parts, "含「"+strings.Join(spec.Keywords, "」或「")+"」")
	}
	if spec.Pattern != "" {
		parts = append(parts, "匹配 /"+spec.Pattern+"/")
	}
	action := "发提醒"
	if spec.Action == eventTriggerActionAgent {
		action = "执行指令"
	}
	if spec.DeliverTo == eventTriggerDeliverOrigin {
		action += "（发回设置处）"
	}
	parts = append(parts, action)
	if spec.Repeat {
		parts = append(parts, "每次都触发，冷却 "+eventTriggerCooldown(spec).String())
	} else {
		parts = append(parts, "触发一次")
	}
	return strings.Join(parts, "，")
}

// EventTriggerSummary 给 WebUI 用。
func EventTriggerSummary(item Reminder) string {
	spec, ok := EventTriggerSpec(item)
	if !ok {
		return ""
	}
	return eventTriggerSummary(item, spec)
}

// EventTriggerStatus 给 WebUI 用。
func EventTriggerStatus(item Reminder) string {
	spec, ok := EventTriggerSpec(item)
	if !ok {
		return "cancelled"
	}
	return eventTriggerStatus(item, spec)
}

// EventTriggerConsumesQuota 给 WebUI 用：还能被点燃的才占额度，和 addEventTrigger 的统计口径一致。
func EventTriggerConsumesQuota(item Reminder) bool {
	spec, ok := EventTriggerSpec(item)
	return ok && eventTriggerArmed(item, spec, time.Now())
}

func eventTriggerStatusLabel(status string) string {
	switch status {
	case "expired":
		return "已到期"
	case "cancelled":
		return "已取消"
	case "used":
		return "已触发"
	case "retrying":
		return "上次失败"
	default:
		return "等待触发"
	}
}

func eventTriggerForTool(item Reminder) *dianaEventTrigger {
	spec, ok := EventTriggerSpec(item)
	if !ok {
		return nil
	}
	return &dianaEventTrigger{
		ID:          item.ID,
		Summary:     eventTriggerSummary(item, spec),
		Action:      spec.Action,
		Message:     item.Message,
		DeliverTo:   spec.DeliverTo,
		Repeat:      spec.Repeat,
		Status:      eventTriggerStatus(item, spec),
		FireCount:   spec.FireCount,
		LastFiredAt: item.LastRunAt,
		ExpiresAt:   spec.ExpiresAt,
		LastError:   item.LastError,
	}
}

func marshalDianaEventTriggerResult(result dianaEventTriggerResult) (string, error) {
	body, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}
