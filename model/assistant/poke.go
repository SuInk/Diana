// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/applog"
)

// 戳一戳（QQ 里也叫拍一拍）。以前机器人只会被戳后回一句「戳我干嘛」，自己从不戳人。
//
// 人用戳一戳是顺手的：被戳了戳回去、叫人没回再戳一下、开完玩笑戳一下、对方说累了
// 轻轻戳一下、说晚安时戳一下代替打字、提醒到点先戳一下。所以它不是一条指令，而是
// 回复时可以顺带做的动作，由模型按场景判断；程序只管三件事：只戳当前对话里能确认
// 身份的人、同一个人不连着戳、同一个会话里总量不刷屏。
const (
	dianaPokeToolName = "poke"
	// pokeSendPersonCooldown 是同一个会话里同一个人两次被戳的最小间隔。
	pokeSendPersonCooldown = 60 * time.Second
	// pokeSendSessionWindow 内同一个会话最多戳 pokeSendSessionLimit 次。
	pokeSendSessionWindow = 10 * time.Minute
	pokeSendSessionLimit  = 3
)

const (
	pokeSceneChat     = "chat"
	pokeSceneBack     = "poke_back"
	pokeSceneReminder = "reminder"
)

var errPokeRateLimited = fmt.Errorf("刚戳过这个人，或者这里最近戳得太多了，这次先不戳")

// sendPoke 戳一下 target。scene 只用于日志，方便统计各场景的触发率。
func (r *Runtime) sendPoke(ctx context.Context, event MessageEvent, target, scene string) (string, error) {
	target = strings.TrimSpace(target)
	action, err := r.sendPokeUnlogged(ctx, event, target)
	r.recordPokeSent(ctx, event, target, scene, action, err)
	if err == nil {
		r.rememberSentPoke(event, target)
	}
	return action, err
}

func (r *Runtime) sendPokeUnlogged(ctx context.Context, event MessageEvent, target string) (string, error) {
	if !IsOneBotPlatform(r.currentPlatform(event)) {
		return "", fmt.Errorf("当前平台不支持戳一戳")
	}
	if err := validatePlatformTargetID(target); err != nil {
		return "", err
	}
	cfg := r.effectiveConfigForEvent(event)
	if selfID := firstNonEmpty(strings.TrimSpace(event.SelfID), strings.TrimSpace(cfg.BotAccount)); selfID != "" && target == selfID {
		return "", fmt.Errorf("不能戳机器人自己")
	}
	if !r.claimPokeSend(event, target, time.Now()) {
		return "", errPokeRateLimited
	}
	groupID := ""
	if event.Kind == EventKindGroup {
		groupID = strings.TrimSpace(event.GroupID)
	}
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return r.dispatchPoke(callCtx, event, groupID, target)
}

// dispatchPoke 按 OneBot 实现的常见命名依次尝试：多数实现用 group_poke /
// friend_poke，部分实现只认 send_poke。
func (r *Runtime) dispatchPoke(ctx context.Context, event MessageEvent, groupID, target string) (string, error) {
	type attempt struct {
		action string
		params map[string]any
	}
	var attempts []attempt
	if groupID != "" {
		params := map[string]any{"group_id": oneBotIDParam(groupID), "user_id": oneBotIDParam(target)}
		attempts = []attempt{{"group_poke", params}, {"send_poke", params}}
	} else {
		params := map[string]any{"user_id": oneBotIDParam(target)}
		attempts = []attempt{{"friend_poke", params}, {"send_poke", params}}
	}
	var errs []string
	for _, candidate := range attempts {
		if _, err := r.callOneBotAPIForEvent(ctx, event, candidate.action, candidate.params); err == nil {
			return candidate.action, nil
		} else {
			errs = append(errs, candidate.action+": "+err.Error())
		}
		if ctx.Err() != nil {
			break
		}
	}
	return "", fmt.Errorf("%s", strings.Join(errs, "；"))
}

// claimPokeSend 同时检查单人冷却和会话总量，通过才占用额度。
func (r *Runtime) claimPokeSend(event MessageEvent, target string, now time.Time) bool {
	session := sessionKey(event)
	personKey := session + "|" + target
	r.pokeMu.Lock()
	defer r.pokeMu.Unlock()
	if r.pokeLastSent == nil {
		r.pokeLastSent = map[string]time.Time{}
	}
	if r.pokeSessionSent == nil {
		r.pokeSessionSent = map[string][]time.Time{}
	}
	if last, ok := r.pokeLastSent[personKey]; ok && now.Sub(last) < pokeSendPersonCooldown {
		return false
	}
	recent := r.pokeSessionSent[session][:0]
	for _, at := range r.pokeSessionSent[session] {
		if now.Sub(at) < pokeSendSessionWindow {
			recent = append(recent, at)
		}
	}
	if len(recent) >= pokeSendSessionLimit {
		r.pokeSessionSent[session] = recent
		return false
	}
	r.pokeLastSent[personKey] = now
	r.pokeSessionSent[session] = append(recent, now)
	for key, at := range r.pokeLastSent {
		if now.Sub(at) > time.Hour {
			delete(r.pokeLastSent, key)
		}
	}
	return true
}

func (r *Runtime) recordPokeSent(ctx context.Context, event MessageEvent, target, scene, action string, err error) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	entry := applog.Entry{
		Kind:    applog.KindOperation,
		Level:   applog.LevelInfo,
		Action:  "poke_sent",
		Message: "机器人戳了一下对方",
		Actor:   oneBotEventActor(event),
		Target:  target,
		Metadata: map[string]any{
			"group_id": event.GroupID,
			"user_id":  target,
			"scene":    scene,
			"action":   action,
		},
		CreatedAt: time.Now(),
	}
	if err != nil {
		entry.Message = "戳一戳没有发出"
		entry.Detail = err.Error()
		entry.Metadata["skipped"] = err == errPokeRateLimited
		if err != errPokeRateLimited {
			entry.Kind = applog.KindError
			entry.Level = applog.LevelError
		}
	}
	_ = writer.AppendLog(ctx, entry)
}

// dianaPokeTool 让回复时可以顺手戳一下对话里的人。
type dianaPokeTool struct {
	runtime *Runtime
	event   MessageEvent
}

func newDianaPokeTool(runtime *Runtime, event MessageEvent) *dianaPokeTool {
	return &dianaPokeTool{runtime: runtime, event: event}
}

func (t *dianaPokeTool) Name() string { return dianaPokeToolName }

func (t *dianaPokeTool) Description() string {
	return "戳一戳（QQ 的拍一拍）：像人一样顺手用，不需要对方要求。适合的时候：对方刚戳了你想戳回去；叫了对方没回想再叫一下；开完玩笑或调侃时轻轻戳一下；对方说累、难过、想被安慰时戳一下表示你在；对方说晚安、回来了这类不需要长篇回应的话时，戳一下就是回应。" +
		"不要用的时候：严肃讨论或正在吵架、对方心情很差不想被打扰、你们还不熟、对方说过别戳、刚戳过或群里正热闹。戳完正文照常写，不要在回复里说「我戳了你一下」。" +
		"只能戳当前消息的发送者、被引用的人或被 @ 的人；user_id 省略时戳发送者。同一个人一分钟内只能戳一次，同一个会话十分钟内最多三次，超了会被拒绝，拒绝了就算了，不要反复重试。"
}

func (t *dianaPokeTool) InputSchema() map[string]any {
	return toolObjectSchema(nil, map[string]any{
		"user_id": toolStringParam("要戳的人：当前消息发送者、被引用的人或被 @ 的人的账号 ID；省略时戳发送者。"),
	})
}

func (t *dianaPokeTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("戳一戳：运行时不可用")
	}
	target := strings.TrimSpace(configToolString(input, "user_id"))
	if target == "" {
		target = strings.TrimSpace(t.event.UserID)
	}
	if !pokeTargetInEvent(t.event, target) {
		return "", fmt.Errorf("只能戳当前消息的发送者、被引用的人或被 @ 的人")
	}
	action, err := t.runtime.sendPoke(ctx, t.event, target, pokeSceneChat)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`{"ok":true,"user_id":%q,"action":%q,"message":"已经戳了对方一下，正文里不要再说「我戳了你」。"}`, target, action), nil
}

// pokeTargetInEvent 核对目标确实出现在当前这条消息里。
func pokeTargetInEvent(event MessageEvent, target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	if target == strings.TrimSpace(event.UserID) {
		return true
	}
	if event.Quoted != nil && target == strings.TrimSpace(event.Quoted.UserID) {
		return true
	}
	for _, mentioned := range mentionedUserIDs(event.Segments) {
		if target == strings.TrimSpace(mentioned) {
			return true
		}
	}
	return false
}
