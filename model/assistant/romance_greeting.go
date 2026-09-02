// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/llm"
)

// 纪念日主动问候：整月和周年当天，机器人主动私聊恋人一句。
//
// 提示词里的纪念日注入只在对方当天恰好来聊天时才生效——纪念日的意义恰恰是
// 「不用对方先开口」。这里用一个低频轮询补上主动的那一半：扫一遍恋人档案，
// 今天是里程碑就发一条问候。只发私聊、每天至多一条、随恋爱模式总开关走，
// 不需要第二个开关——确立恋人关系本身就是对这类互动的同意，分手即停。
//
// 防重发用档案里的 LastGreetedOn：发送前先落库占位（at-most-once）。发送失败
// 会损失当天的问候，但比进程重启后把同一句祝福发两遍强——祝福重发一次就穿帮。

const (
	// romanceGreetingCheckInterval 是扫描间隔。纪念日按天计，十分钟一查绰绰有余。
	romanceGreetingCheckInterval = 10 * time.Minute
	// romanceGreetingStartHour / EndHour 限制发送时段：凌晨的祝福不浪漫，只吵人。
	romanceGreetingStartHour = 9
	romanceGreetingEndHour   = 22
	// romanceGreetingScanLimit 封顶一轮扫描的档案数。恋人通常个位数，这个上限
	// 只是防御性地挡住异常庞大的档案表。
	romanceGreetingScanLimit = 500
	romanceGreetingTimeout   = 20 * time.Second
)

// UserMemoryListStore 是能翻页列出长期档案的存储。SQLite 实现有，测试的内存
// 实现可以没有——没有就不扫，功能安静降级。
type UserMemoryListStore interface {
	ListUserMemories(ctx context.Context, botProfileID, query string, limit, offset int) ([]UserMemoryProfile, int, error)
}

// runRomanceGreetingLoop 启动纪念日问候轮询。
func (r *Runtime) runRomanceGreetingLoop(ctx context.Context) {
	ticker := time.NewTicker(romanceGreetingCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.dispatchRomanceAnniversaryGreetings(ctx)
		}
	}
}

// dispatchRomanceAnniversaryGreetings 扫一遍恋人档案，把今天该发的祝福发出去。
func (r *Runtime) dispatchRomanceAnniversaryGreetings(ctx context.Context) {
	r.mu.RLock()
	store := r.userMemory
	r.mu.RUnlock()
	lister, ok := store.(UserMemoryListStore)
	if !ok {
		return
	}
	now := r.clock()
	hour := now.Local().Hour()
	if hour < romanceGreetingStartHour || hour >= romanceGreetingEndHour {
		return
	}
	for _, cfg := range r.romanceEnabledConfigs() {
		r.greetRomanceMilestones(ctx, lister, cfg, now)
	}
}

// romanceEnabledConfigs 列出开了恋爱模式的机器人配置。
func (r *Runtime) romanceEnabledConfigs() []BotConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	configs := make([]BotConfig, 0, len(r.profileConfigs)+1)
	seen := map[string]bool{}
	appendConfig := func(cfg BotConfig) {
		id := strings.TrimSpace(cfg.ID)
		if seen[id] || !boolValue(cfg.RomanceEnabled, false) {
			return
		}
		seen[id] = true
		configs = append(configs, cfg)
	}
	appendConfig(r.cfg)
	for _, cfg := range r.profileConfigs {
		appendConfig(cfg)
	}
	return configs
}

func (r *Runtime) greetRomanceMilestones(ctx context.Context, lister UserMemoryListStore, cfg BotConfig, now time.Time) {
	profileID := strings.TrimSpace(cfg.ID)
	today := now.Local().Format("2006-01-02")
	scanned := 0
	for offset := 0; scanned < romanceGreetingScanLimit; {
		listCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		profiles, _, err := lister.ListUserMemories(listCtx, profileID, "", 100, offset)
		cancel()
		if err != nil {
			log.Printf("chatbot romance greeting scan failed: %v", err)
			return
		}
		if len(profiles) == 0 {
			return
		}
		for _, profile := range profiles {
			scanned++
			if !romanceActive(profile) || profile.Romance.Since.IsZero() {
				continue
			}
			note := romanceMilestoneNote(profile.Romance.Since, now)
			if note == "" || profile.Romance.LastGreetedOn == today {
				continue
			}
			r.deliverRomanceGreeting(ctx, cfg, profile, note, today)
		}
		offset += len(profiles)
	}
}

// deliverRomanceGreeting 先占位再发送：宁可漏一天，不能一天发两遍。
func (r *Runtime) deliverRomanceGreeting(ctx context.Context, cfg BotConfig, profile UserMemoryProfile, note string, today string) {
	event := MessageEvent{
		Kind:      EventKindPrivate,
		Platform:  cfg.Platform,
		ProfileID: strings.TrimSpace(cfg.ID),
		UserID:    profile.UserID,
	}
	claimed := *profile.Romance
	claimed.LastGreetedOn = today
	if _, written := r.writeUserMemory(event, UserMemoryUpdate{Administrative: true, SetRomance: &claimed}); !written {
		return
	}
	greeting := r.generateRomanceGreeting(ctx, event, profile, note)
	err := r.send(ctx, event, greeting)
	if err == nil {
		r.record(EventRecord{
			At:        time.Now(),
			Kind:      EventKindPrivate,
			Platform:  event.Platform,
			ProfileID: event.ProfileID,
			UserID:    event.UserID,
			Text:      "[romance] anniversary",
			Reply:     greeting,
			Handled:   true,
			Outcome:   "romance_greeting",
			Decision:  "replied",
			Reason:    "恋爱纪念日主动问候",
		})
	}
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	entry := applog.Entry{
		Kind:    applog.KindOperation,
		Level:   applog.LevelInfo,
		Action:  "chatbot.romance_greeting",
		Message: "纪念日问候已发送",
		Actor:   oneBotEventActor(event),
		Metadata: map[string]any{
			"user_id":  profile.UserID,
			"note":     note,
			"greeting": truncateRunesFromStart(greeting, 200),
		},
	}
	if err != nil {
		entry.Kind = applog.KindError
		entry.Level = applog.LevelError
		entry.Message = "纪念日问候发送失败，今天不再重试"
		entry.Detail = err.Error()
	}
	_ = writer.AppendLog(ctx, entry)
}

// generateRomanceGreeting 用人设语气生成祝福；模型不可用时退回朴素模板——
// 纪念日漏掉比措辞平淡严重得多。
func (r *Runtime) generateRomanceGreeting(ctx context.Context, event MessageEvent, profile UserMemoryProfile, note string) string {
	ctx = withLLMUsagePurpose(ctx, "romance_greeting")
	who := firstNonEmpty(strings.TrimSpace(profile.DisplayName), profile.UserID)
	instruction := fmt.Sprintf(
		"%s你和 %s 是确立了关系的恋人，这条消息由你主动发起，对方还没说话。"+
			"给对方发一条纪念日问候：1 到 3 句，语气符合你的人设和你们的关系，可以提到在一起的时间和某种具体的心意；"+
			"不要提系统、提醒、定时任务或机器人身份，不要用括号描写动作，只输出要发送的话。",
		note, who)
	messages := r.withUserFacingPersona(event, []llm.Message{{Role: llm.RoleUser, Content: instruction}})
	callCtx, cancel := context.WithTimeout(ctx, romanceGreetingTimeout)
	defer cancel()
	raw, err := r.runLLMRouterProvider(callCtx, func(client LLMProvider) (string, error) {
		resp, err := client.Generate(callCtx, llm.GenerateRequest{Messages: messages})
		if err != nil {
			return "", err
		}
		return resp.Text, nil
	})
	if err == nil {
		if greeting := strings.TrimSpace(raw); greeting != "" {
			return truncateRunesPlain(greeting, 300)
		}
	}
	return note + "纪念日快乐！"
}
