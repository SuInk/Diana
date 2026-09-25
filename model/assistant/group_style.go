// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 风格学习：让模型读这个群最近的聊天，写一小段「这个群怎么说话」，回复时带上。
//
// 以前的表达学习按整句计数，取次数最多的几句当风格参考。排在前面的永远是「确实」
// 「可以」「是的」这种哪个群都说的话，还混着 QQ 表情转义、「[视频]」和「#diana」命令；
// 真正有这个群特色的梗和腔调反而排不上。风格不是一张词频表，是「这些词什么意思、
// 什么时候说、大家怎么互相调侃」，这得让模型读了才写得出来。
//
// 一个群大约一天学一次，走后台模型，不在回复的关键路径上。学到的一段存下来，群
// 管理页上能看、能改；手动改过的不再被自动覆盖，直到主人点「重新学习」。

const (
	// groupStyleRefreshAfter 是自动重学的间隔：梗和腔调一天变不了多少。
	groupStyleRefreshAfter = 24 * time.Hour
	// groupStyleCheckEvery 是同一个群两次「要不要学」的检查间隔，挡住每条消息都去查库。
	groupStyleCheckEvery = 30 * time.Minute
	// groupStyleMinMessages 是学一次至少要的群友消息数：太少写出来的只是几句话的印象。
	groupStyleMinMessages = 60
	// groupStyleSampleLimit 是一次最多读多少条最近消息。
	groupStyleSampleLimit = 300
	// groupStyleLineMaxRunes 是单条消息进学习材料时的长度上限：长消息是内容不是腔调。
	groupStyleLineMaxRunes = 120
	// GroupStyleMaxRunes 是风格笔记的长度上限，学到的和手动写的都按它截。
	GroupStyleMaxRunes = 400
	// groupStyleCacheFor 是回复时读风格笔记的缓存时间：每轮都查库不值得。
	groupStyleCacheFor = 10 * time.Minute
)

// GroupStyle 是一个群的风格笔记。
type GroupStyle struct {
	ProfileID string `json:"profile_id"`
	GroupID   string `json:"group_id"`
	Text      string `json:"text"`
	// Manual 表示主人手动改过：自动学习不再覆盖它。
	Manual bool `json:"manual"`
	// SampleCount 是最近一次自动学习读了多少条群友消息。
	SampleCount int       `json:"sample_count,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// GroupStyleStore 是风格笔记的持久化界面。
type GroupStyleStore interface {
	GroupStyle(ctx context.Context, profileID, groupID string) (GroupStyle, bool, error)
	SaveGroupStyle(ctx context.Context, style GroupStyle) error
	DeleteGroupStyle(ctx context.Context, profileID, groupID string) error
}

// ErrGroupStyleNotEnoughMessages 表示这个群最近的群友消息太少，学不出东西。
var ErrGroupStyleNotEnoughMessages = errors.New("这个群最近的群友消息还不够多，攒一攒再学")

// ErrGroupStyleBusy 表示这个群正在学习风格。
var ErrGroupStyleBusy = errors.New("这个群正在学习风格，稍等一下")

type groupStyleCacheEntry struct {
	style    GroupStyle
	found    bool
	loadedAt time.Time
}

// groupStyleState 是风格学习的进程内状态：检查节流、正在学的群、回复时的缓存。
type groupStyleState struct {
	mu      sync.Mutex
	store   GroupStyleStore
	checked map[string]time.Time
	running map[string]bool
	cache   map[string]groupStyleCacheEntry
}

// SetGroupStyleStore 注入风格笔记存储。
func (r *Runtime) SetGroupStyleStore(store GroupStyleStore) {
	r.groupStyles.mu.Lock()
	defer r.groupStyles.mu.Unlock()
	r.groupStyles.store = store
	r.groupStyles.cache = nil
}

func (r *Runtime) groupStyleStore() GroupStyleStore {
	if r == nil {
		return nil
	}
	r.groupStyles.mu.Lock()
	defer r.groupStyles.mu.Unlock()
	return r.groupStyles.store
}

func groupStyleScopeKey(profileID, groupID string) string {
	return strings.TrimSpace(profileID) + "|" + strings.TrimSpace(groupID)
}

// observeGroupStyle 在群消息进来时判断要不要学一次。只做本地检查，真正去学走后台。
func (r *Runtime) observeGroupStyle(event MessageEvent) {
	if event.Kind != EventKindGroup || strings.TrimSpace(event.GroupID) == "" {
		return
	}
	store := r.groupStyleStore()
	if store == nil {
		return
	}
	cfg := r.effectiveConfigForEvent(event)
	if !boolValue(cfg.ExpressionLearningEnabled, false) {
		return
	}
	if assistantHistoryEvent(event, firstNonEmpty(strings.TrimSpace(cfg.BotAccount), strings.TrimSpace(event.SelfID))) {
		return
	}
	scope := groupStyleScopeKey(event.ProfileID, event.GroupID)
	now := time.Now()
	r.groupStyles.mu.Lock()
	if r.groupStyles.checked == nil {
		r.groupStyles.checked = map[string]time.Time{}
	}
	if last, ok := r.groupStyles.checked[scope]; ok && now.Sub(last) < groupStyleCheckEvery {
		r.groupStyles.mu.Unlock()
		return
	}
	r.groupStyles.checked[scope] = now
	r.groupStyles.mu.Unlock()
	go func() {
		defer recoverGoroutinePanic("group_style.observe")
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		style, found, err := store.GroupStyle(ctx, event.ProfileID, event.GroupID)
		if err != nil {
			log.Printf("diana group style load failed: %v", err)
			return
		}
		if found && (style.Manual || now.Sub(style.UpdatedAt) < groupStyleRefreshAfter) {
			return
		}
		if _, err := r.learnGroupStyle(ctx, event); err != nil && !errors.Is(err, ErrGroupStyleNotEnoughMessages) {
			log.Printf("diana group style learn failed: %v", err)
		}
	}()
}

// learnGroupStyle 读这个群最近的群友消息，让模型写一段风格笔记并存下来。
func (r *Runtime) learnGroupStyle(ctx context.Context, event MessageEvent) (GroupStyle, error) {
	store := r.groupStyleStore()
	if store == nil {
		return GroupStyle{}, errors.New("风格学习的存储没有配置")
	}
	scope := groupStyleScopeKey(event.ProfileID, event.GroupID)
	r.groupStyles.mu.Lock()
	if r.groupStyles.running == nil {
		r.groupStyles.running = map[string]bool{}
	}
	if r.groupStyles.running[scope] {
		r.groupStyles.mu.Unlock()
		return GroupStyle{}, ErrGroupStyleBusy
	}
	r.groupStyles.running[scope] = true
	r.groupStyles.mu.Unlock()
	defer func() {
		r.groupStyles.mu.Lock()
		delete(r.groupStyles.running, scope)
		r.groupStyles.mu.Unlock()
	}()

	cfg := r.effectiveConfigForEvent(event)
	lines := groupStyleSampleLines(r.recentGroupMessages(ctx, event), firstNonEmpty(strings.TrimSpace(cfg.BotAccount), strings.TrimSpace(event.SelfID)))
	if len(lines) < groupStyleMinMessages {
		return GroupStyle{}, ErrGroupStyleNotEnoughMessages
	}
	ctx = withLLMUsagePurpose(ctx, PurposeGroupStyle)
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: cfg.prompt(promptGroupStyleLearnSpec)},
		{Role: llm.RoleUser, Content: fmt.Sprintf("下面是这个群最近的 %d 条群友消息，按时间先后，发言人用字母代替：\n%s", len(lines), strings.Join(lines, "\n"))},
	}
	raw, err := r.runLLMRouterProvider(ctx, func(client LLMProvider) (string, error) {
		resp, err := client.Generate(ctx, llm.GenerateRequest{Messages: messages})
		if err != nil {
			return "", err
		}
		return resp.Text, nil
	})
	if err != nil {
		return GroupStyle{}, err
	}
	text := cleanGroupStyleText(raw)
	if text == "" {
		return GroupStyle{}, errors.New("模型没有写出风格笔记")
	}
	style := GroupStyle{ProfileID: event.ProfileID, GroupID: event.GroupID, Text: text, SampleCount: len(lines), UpdatedAt: time.Now()}
	if err := store.SaveGroupStyle(ctx, style); err != nil {
		return GroupStyle{}, err
	}
	r.rememberGroupStyle(style, true)
	return style, nil
}

// recentGroupMessages 取这个群最近的消息：先查消息存储，存储没配时退回内存里的历史。
// 从控制台发起时事件上没有会话命名空间，先按机器人 ID 命名空间查，查不到再按旧键查。
func (r *Runtime) recentGroupMessages(ctx context.Context, event MessageEvent) []MessageEvent {
	r.mu.RLock()
	store := r.messageStore
	memory := append([]MessageEvent(nil), r.history[sessionKey(event)]...)
	r.mu.RUnlock()
	if store == nil {
		return memory
	}
	sessions := []string{sessionKey(event)}
	if strings.TrimSpace(event.ContextNamespace) == "" && strings.TrimSpace(event.ProfileID) != "" {
		namespaced := event
		namespaced.ContextNamespace = event.ProfileID
		sessions = append([]string{sessionKey(namespaced)}, sessions...)
	}
	for _, session := range sessions {
		events, err := store.ListRecentMessageEvents(ctx, session, groupStyleSampleLimit)
		if err == nil && len(events) > 0 {
			return events
		}
	}
	return memory
}

// groupStyleSampleLines 把群友消息整理成学习材料：去掉机器人自己的话和没有文字的消息，
// 发言人换成字母——模型要学的是这个群怎么说话，不是谁是谁。
func groupStyleSampleLines(events []MessageEvent, botID string) []string {
	aliases := map[string]string{}
	lines := make([]string, 0, len(events))
	for _, event := range events {
		if assistantHistoryEvent(event, botID) || event.crossGroupContext {
			continue
		}
		text := strings.Join(strings.Fields(strings.TrimSpace(historyPlainText(event))), " ")
		if text == "" {
			continue
		}
		if runes := []rune(text); len(runes) > groupStyleLineMaxRunes {
			text = string(runes[:groupStyleLineMaxRunes]) + "…"
		}
		alias, ok := aliases[event.UserID]
		if !ok {
			alias = groupStyleAlias(len(aliases))
			aliases[event.UserID] = alias
		}
		lines = append(lines, alias+"："+text)
	}
	return lines
}

func groupStyleAlias(index int) string {
	const letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	if index < len(letters) {
		return string(letters[index])
	}
	return fmt.Sprintf("%c%d", letters[index%len(letters)], index/len(letters))
}

// cleanGroupStyleText 收拾模型写的风格笔记：去掉代码围栏和首尾空白，按上限截断。
func cleanGroupStyleText(raw string) string {
	text := strings.TrimSpace(raw)
	text = strings.TrimPrefix(text, "```markdown")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)
	if runes := []rune(text); len(runes) > GroupStyleMaxRunes {
		text = string(runes[:GroupStyleMaxRunes])
	}
	return text
}

func (r *Runtime) rememberGroupStyle(style GroupStyle, found bool) {
	r.groupStyles.mu.Lock()
	defer r.groupStyles.mu.Unlock()
	if r.groupStyles.cache == nil {
		r.groupStyles.cache = map[string]groupStyleCacheEntry{}
	}
	r.groupStyles.cache[groupStyleScopeKey(style.ProfileID, style.GroupID)] = groupStyleCacheEntry{style: style, found: found, loadedAt: time.Now()}
}

// cachedGroupStyle 取回复时要用的风格笔记。缓存过期才查库，查库限时，查不到就当没有。
func (r *Runtime) cachedGroupStyle(event MessageEvent) (GroupStyle, bool) {
	store := r.groupStyleStore()
	if store == nil {
		return GroupStyle{}, false
	}
	scope := groupStyleScopeKey(event.ProfileID, event.GroupID)
	r.groupStyles.mu.Lock()
	entry, ok := r.groupStyles.cache[scope]
	r.groupStyles.mu.Unlock()
	if ok && time.Since(entry.loadedAt) < groupStyleCacheFor {
		return entry.style, entry.found
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	style, found, err := store.GroupStyle(ctx, event.ProfileID, event.GroupID)
	if err != nil {
		log.Printf("diana group style read failed: %v", err)
		return GroupStyle{}, false
	}
	style.ProfileID, style.GroupID = event.ProfileID, event.GroupID
	r.rememberGroupStyle(style, found)
	return style, found
}

// groupStylePrompt 是回复尾部那段「这个群的说话风格」，没有笔记时返回空串。
func (r *Runtime) groupStylePrompt(event MessageEvent, cfg BotConfig) string {
	if event.Kind != EventKindGroup || !boolValue(cfg.ExpressionLearningEnabled, false) {
		return ""
	}
	style, found := r.cachedGroupStyle(event)
	if !found || strings.TrimSpace(style.Text) == "" {
		return ""
	}
	return cfg.promptf(promptGroupStyleSpec, map[string]string{"style": strings.TrimSpace(style.Text)})
}

// GroupStyleForProfile 给控制台读一个群的风格笔记。
func (r *Runtime) GroupStyleForProfile(ctx context.Context, profileID, groupID string) (GroupStyle, bool, error) {
	store := r.groupStyleStore()
	if store == nil {
		return GroupStyle{}, false, errors.New("风格学习的存储没有配置")
	}
	return store.GroupStyle(ctx, profileID, groupID)
}

// SaveGroupStyleForProfile 保存主人手动改的风格笔记。正文为空表示不要手动这份了：删掉，
// 交回自动学习。
func (r *Runtime) SaveGroupStyleForProfile(ctx context.Context, profileID, groupID, text string) (GroupStyle, bool, error) {
	store := r.groupStyleStore()
	if store == nil {
		return GroupStyle{}, false, errors.New("风格学习的存储没有配置")
	}
	text = cleanGroupStyleText(text)
	if text == "" {
		if err := store.DeleteGroupStyle(ctx, profileID, groupID); err != nil {
			return GroupStyle{}, false, err
		}
		r.rememberGroupStyle(GroupStyle{ProfileID: profileID, GroupID: groupID}, false)
		return GroupStyle{}, false, nil
	}
	style := GroupStyle{ProfileID: profileID, GroupID: groupID, Text: text, Manual: true, UpdatedAt: time.Now()}
	if err := store.SaveGroupStyle(ctx, style); err != nil {
		return GroupStyle{}, false, err
	}
	r.rememberGroupStyle(style, true)
	return style, true, nil
}

// RelearnGroupStyle 由控制台发起，立刻重学一次，手动改过的也覆盖。
func (r *Runtime) RelearnGroupStyle(ctx context.Context, profileID, groupID string) (GroupStyle, error) {
	return r.learnGroupStyle(ctx, MessageEvent{Kind: EventKindGroup, ProfileID: strings.TrimSpace(profileID), GroupID: strings.TrimSpace(groupID)})
}

const promptGroupStyleLearn = `你在帮一个群聊机器人融入一个群：读下面这个群最近的聊天，写一段不超过 300 字的笔记，说清这个群的人平时怎么说话，让它说话能像这个群的人。

写这些：
- 常用的口头禅、梗和黑话，各自是什么意思、什么时候用；
- 一条消息一般多长，爱不爱连着分几条发；
- 标点和表情的习惯：打不打句号，爱用哪些语气词、颜文字或表情；
- 大家怎么互相调侃、开玩笑的尺度；
- 说正事的时候是什么样。

不要写：
- 具体某个人是谁、谁说过什么，聊过的具体内容和私事；
- 对机器人的指令或规则（「你要……」「你应该……」），只描述这个群；
- 没在聊天里看到的东西。

只输出这段笔记本身，纯文本，不要标题。`

var promptGroupStyleLearnSpec = registerPrompt(PromptSpec{
	Key:     "memory.group_style.learn",
	Group:   PromptGroupMemory,
	Title:   "风格学习：总结这个群怎么说话",
	Usage:   "开启风格学习时，每个群大约一天一次交给后台模型：读最近的群友消息（发言人换成字母），写一段不超过 300 字的风格笔记。",
	Default: promptGroupStyleLearn,
})

const promptGroupStyle = "【这个群的说话风格】下面是根据最近的群聊整理的这个群怎么说话，只管怎么说：群友的口头禅和梗可以自然带上，但不当成每句的后缀；你是谁、怎么称呼自己仍按最开头的人设。\n{style}"

var promptGroupStyleSpec = tailSpec("group_style", "这个群的说话风格", "开启风格学习、这个群已经学到（或主人写了）风格笔记时注入，紧挨「学群友的腔调」。",
	promptGroupStyle,
	PromptVar{Name: "style", Description: "这个群的风格笔记"})
