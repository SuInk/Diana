// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 整体风格：把这台机器人在各个群学到的风格笔记放在一起，归纳出一段「你平时怎么说话」。
//
// 分群的笔记照旧，群里有自己的就用自己的。整体风格只给还没学到的地方兜底：新群、
// 消息不够学的小群、私聊。只在一个群里流行的梗不往整体里写，不然 A 群的黑话会被带到
// B 群和私聊里去说。
//
// 材料是各群已经写好的笔记，不再重读原始聊天：笔记里已经去掉了具体的人和事、过滤过
// 怪话，一次请求只有几千字。整体风格和群笔记存在同一张表里，群号记成
// GlobalStyleGroupID；手动改、交回自动、关掉、重新学习的规矩和群笔记一样。

// GlobalStyleGroupID 是整体风格那一行的群号。真实群号不会是它。
const GlobalStyleGroupID = "*"

const (
	// globalStyleMinGroups 是归纳整体风格至少要的群笔记数：只有一个群时「共同点」就是
	// 这个群自己的梗。
	globalStyleMinGroups = 2
	// globalStyleMaxGroups 是一次最多读多少个群的笔记，取最近更新的。
	globalStyleMaxGroups = 20
)

// ErrGlobalStyleNotEnoughGroups 表示学到风格的群还不够，归纳不出共同点。
var ErrGlobalStyleNotEnoughGroups = fmt.Errorf("至少要有 %d 个群学到风格，才能归纳整体风格", globalStyleMinGroups)

// refreshGlobalStyle 在后台判断整体风格要不要重新归纳：没有、或者自动归纳的已经过了一天。
// 手动写的和关掉的不动。
func (r *Runtime) refreshGlobalStyle(ctx context.Context, profileID string, now time.Time) {
	store := r.groupStyleStore()
	if store == nil {
		return
	}
	style, found, err := store.GroupStyle(ctx, profileID, GlobalStyleGroupID)
	if err != nil {
		log.Printf("diana global style load failed: %v", err)
		return
	}
	if found && (style.Disabled || style.Manual || now.Sub(style.UpdatedAt) < groupStyleRefreshAfter) {
		return
	}
	if _, err := r.learnGlobalStyle(ctx, profileID); err != nil && !errors.Is(err, ErrGlobalStyleNotEnoughGroups) && !errors.Is(err, ErrGroupStyleBusy) {
		log.Printf("diana global style learn failed: %v", err)
	}
}

// learnGlobalStyle 读这台机器人各群的风格笔记，让模型归纳一段整体风格并存下来。
func (r *Runtime) learnGlobalStyle(ctx context.Context, profileID string) (GroupStyle, error) {
	store := r.groupStyleStore()
	if store == nil {
		return GroupStyle{}, errors.New("风格学习的存储没有配置")
	}
	scope := groupStyleScopeKey(profileID, GlobalStyleGroupID)
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

	all, err := store.GroupStyles(ctx, profileID)
	if err != nil {
		return GroupStyle{}, err
	}
	notes := globalStyleSourceNotes(all)
	if len(notes) < globalStyleMinGroups {
		return GroupStyle{}, ErrGlobalStyleNotEnoughGroups
	}
	event := MessageEvent{ProfileID: profileID}
	cfg := r.effectiveConfigForEvent(event)
	ctx = withLLMUsagePurpose(withLLMUsageContext(ctx, event), PurposeGroupStyle)
	system := cfg.prompt(promptGlobalStyleLearnSpec)
	if filter := styleFilterLearnPrompt(cfg); filter != "" {
		system += "\n\n" + filter
	}
	sections := make([]string, 0, len(notes))
	for i, note := range notes {
		sections = append(sections, fmt.Sprintf("【群 %d】\n%s", i+1, strings.TrimSpace(note.Text)))
	}
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: system},
		{Role: llm.RoleUser, Content: fmt.Sprintf("下面是这个机器人待的 %d 个群各自的风格笔记：\n\n%s", len(notes), strings.Join(sections, "\n\n"))},
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
		return GroupStyle{}, errors.New("模型没有写出整体风格")
	}
	style := GroupStyle{ProfileID: profileID, GroupID: GlobalStyleGroupID, Text: text, SampleCount: len(notes), UpdatedAt: time.Now()}
	if previous, found, err := store.GroupStyle(ctx, profileID, GlobalStyleGroupID); err == nil && found {
		style.Disabled = previous.Disabled
	}
	if err := store.SaveGroupStyle(ctx, style); err != nil {
		return GroupStyle{}, err
	}
	r.rememberGroupStyle(style, true)
	return style, nil
}

// globalStyleSourceNotes 挑出能拿来归纳的群笔记：有正文、没关掉、不是整体风格自己，
// 最近更新的在前，最多 globalStyleMaxGroups 个。
func globalStyleSourceNotes(styles []GroupStyle) []GroupStyle {
	notes := make([]GroupStyle, 0, len(styles))
	for _, style := range styles {
		if style.GroupID == GlobalStyleGroupID || style.Disabled || strings.TrimSpace(style.Text) == "" {
			continue
		}
		notes = append(notes, style)
	}
	sort.SliceStable(notes, func(i, j int) bool { return notes[i].UpdatedAt.After(notes[j].UpdatedAt) })
	if len(notes) > globalStyleMaxGroups {
		notes = notes[:globalStyleMaxGroups]
	}
	return notes
}

const promptGlobalStyleLearn = `你在帮一个群聊机器人找到自己平时的说话方式。下面是它待的几个群各自的风格笔记，请归纳出一段不超过 300 字的笔记，写这些群共同的说话习惯，让它在新群和私聊里说话也自然。

写这些：
- 大多数群都有的说话习惯：一条消息一般多长、爱不爱分几条发、标点和表情怎么用、语气词；
- 几个群都在用的口头禅和梗，各自什么意思；
- 开玩笑的尺度，说正事的时候是什么样。

不要写：
- 只在一个群里出现的梗、黑话和外号；
- 哪个群怎么样、群和群的区别；
- 对机器人的指令或规则（「你要……」「你应该……」），只描述说话习惯；
- 笔记里没有的东西。

几个群说法冲突时，取多数群的，拿不准的就不写。只输出这段笔记本身，纯文本，不要标题。`

var promptGlobalStyleLearnSpec = registerPrompt(PromptSpec{
	Key:     "memory.group_style.learn_global",
	Group:   PromptGroupMemory,
	Title:   "风格学习：归纳整体说话风格",
	Usage:   "开启风格学习、至少两个群学到风格时，大约一天一次交给后台模型：读各群的风格笔记，归纳一段各群共同的说话习惯，给新群、小群和私聊兜底。",
	Default: promptGlobalStyleLearn,
})

const promptGlobalStyle = "【你平时的说话风格】下面是根据你待的各个群整理的大家共同的说话习惯，只管怎么说：常用的口头禅和梗可以自然带上，但不当成每句的后缀；你是谁、怎么称呼自己仍按最开头的人设。\n{style}"

var promptGlobalStyleSpec = tailSpec("global_style", "整体说话风格", "开启风格学习、这个群还没学到自己的风格（或是私聊）、整体风格已经归纳好（或主人写了）时注入，位置和「这个群的说话风格」相同。",
	promptGlobalStyle,
	PromptVar{Name: "style", Description: "整体风格笔记"})
