// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"sync"
	"time"
	"unicode"
)

// 发言人名录把「昵称 → 账号」接到聊天记录上，三处在用：
//
//   - chat_history 按昵称查某人的发言（sender_name，或模型把昵称误填进 sender_user_id）；
//   - Telegram 的群成员候选：Bot API 拉不到全群成员表，通道内存里的近期发言者重启
//     就清空，名录让很久没说话的人也能按昵称找到账号；
//   - 记忆召回：当前消息里点到的人（@、引用、直接说昵称）的个人记忆提权。
//
// 名录只说明「这个账号在这里用过这个昵称发言」，不证明此刻在群，也不带任何身份权限。
const (
	speakerLookupTimeout = 2 * time.Second
	// speakerDirectoryCacheTTL 是记忆召回用的会话名录缓存时长。每条消息都要比对一遍
	// 正文里有没有点到谁，不值得次次查库；新面孔晚几分钟进名录无所谓。
	speakerDirectoryCacheTTL = 3 * time.Minute
	// speakerDirectoryRecallLimit 是记忆召回时参与昵称比对的最近发言人数。
	speakerDirectoryRecallLimit = 300
	// maximumMentionedSubjects 封顶一条消息点到的人数，避免长文把整个名录都点亮。
	maximumMentionedSubjects = 4
	// maximumResolvedSenders 是按昵称查聊天记录时最多合并的账号数。
	maximumResolvedSenders = 5
)

type speakerDirectoryCache struct {
	mu      sync.Mutex
	entries map[string]speakerDirectoryEntry
}

type speakerDirectoryEntry struct {
	at       time.Time
	speakers []MessageSpeaker
}

func (r *Runtime) speakerStore() MessageSpeakerStore {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	store := r.messageStore
	r.mu.RUnlock()
	speakers, _ := store.(MessageSpeakerStore)
	return speakers
}

// resolveSpeakers 在当前会话（crossGroup 时是同一机器人的全部群）的名录里按昵称、
// 旧昵称、@用户名或账号找人。精确命中的排前面。
func (r *Runtime) resolveSpeakers(ctx context.Context, event MessageEvent, name string, crossGroup bool, limit int) ([]MessageSpeaker, error) {
	store := r.speakerStore()
	name = strings.TrimSpace(name)
	if store == nil || name == "" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, speakerLookupTimeout)
	defer cancel()
	query := MessageSpeakerQuery{Session: sessionKey(event), Name: name, Limit: limit}
	if crossGroup {
		query.CrossSession, query.SessionPrefix = true, groupHistorySessionPrefix(event)
	}
	speakers, err := store.ListMessageSpeakers(ctx, query)
	return r.dropSelfSpeakers(event, speakers), err
}

// sessionSpeakers 按最近发言列出当前会话的人，带短时缓存。
func (r *Runtime) sessionSpeakers(ctx context.Context, event MessageEvent, limit int) []MessageSpeaker {
	store := r.speakerStore()
	if store == nil {
		return nil
	}
	key := sessionKey(event)
	r.speakerCache.mu.Lock()
	entry, ok := r.speakerCache.entries[key]
	r.speakerCache.mu.Unlock()
	if ok && time.Since(entry.at) < speakerDirectoryCacheTTL {
		return entry.speakers
	}
	ctx, cancel := context.WithTimeout(ctx, speakerLookupTimeout)
	defer cancel()
	speakers, err := store.ListMessageSpeakers(ctx, MessageSpeakerQuery{Session: key, Limit: limit})
	if err != nil {
		return entry.speakers
	}
	speakers = r.dropSelfSpeakers(event, speakers)
	r.speakerCache.mu.Lock()
	if r.speakerCache.entries == nil || len(r.speakerCache.entries) >= 512 {
		r.speakerCache.entries = map[string]speakerDirectoryEntry{}
	}
	r.speakerCache.entries[key] = speakerDirectoryEntry{at: time.Now(), speakers: speakers}
	r.speakerCache.mu.Unlock()
	return speakers
}

// dropSelfSpeakers 去掉机器人自己的账号。出站消息本就不进名录，这里防的是旧数据
// 和多机器人同群时把另一个机器人自己的号当人找。
func (r *Runtime) dropSelfSpeakers(event MessageEvent, speakers []MessageSpeaker) []MessageSpeaker {
	self := map[string]bool{}
	for _, id := range []string{event.SelfID, r.effectiveConfigForEvent(event).BotAccount} {
		if id = strings.TrimSpace(id); id != "" {
			self[id] = true
		}
	}
	if len(self) == 0 {
		return speakers
	}
	kept := speakers[:0:0]
	for _, speaker := range speakers {
		if !self[speaker.UserID] {
			kept = append(kept, speaker)
		}
	}
	return kept
}

// preferExactSpeakers 有精确命中时只留精确命中的：查「小明」不该把「小明的猫」也并进来。
func preferExactSpeakers(speakers []MessageSpeaker, limit int) []MessageSpeaker {
	var exact []MessageSpeaker
	for _, speaker := range speakers {
		if speaker.ExactMatch {
			exact = append(exact, speaker)
		}
	}
	if len(exact) > 0 {
		speakers = exact
	}
	if limit > 0 && len(speakers) > limit {
		speakers = speakers[:limit]
	}
	return speakers
}

// mentionedSubjectUserIDs 找出这条消息点到的其他人：@ 目标、被引用消息的作者，以及
// 正文里直接写出的昵称或 @用户名。当前发言者和机器人自己不算。
func (r *Runtime) mentionedSubjectUserIDs(ctx context.Context, event MessageEvent, text string) []string {
	exclude := map[string]bool{strings.TrimSpace(event.UserID): true, strings.TrimSpace(event.SelfID): true, "": true}
	if account := strings.TrimSpace(r.effectiveConfigForEvent(event).BotAccount); account != "" {
		exclude[account] = true
	}
	var ids []string
	add := func(id string) {
		id = strings.TrimSpace(id)
		if exclude[id] || len(ids) >= maximumMentionedSubjects {
			return
		}
		exclude[id] = true
		ids = append(ids, id)
	}
	var unresolvedHandles []string
	for _, mention := range event.MentionTargets {
		if mention.Target == "self" {
			continue
		}
		if mention.UserID != "" {
			add(mention.UserID)
		} else if mention.Username != "" {
			unresolvedHandles = append(unresolvedHandles, strings.ToLower(strings.TrimPrefix(mention.Username, "@")))
		}
	}
	for _, segment := range event.Segments {
		if segment.Type == "at" {
			add(segment.Data["qq"])
		}
	}
	if event.Quoted != nil {
		add(event.Quoted.UserID)
	}
	lowered := strings.ToLower(text)
	if len(ids) >= maximumMentionedSubjects || (strings.TrimSpace(lowered) == "" && len(unresolvedHandles) == 0) {
		return ids
	}
	for _, speaker := range r.sessionSpeakers(ctx, event, speakerDirectoryRecallLimit) {
		if exclude[speaker.UserID] {
			continue
		}
		if speakerNamedIn(speaker, lowered, unresolvedHandles) {
			add(speaker.UserID)
		}
	}
	return ids
}

func speakerNamedIn(speaker MessageSpeaker, lowered string, handles []string) bool {
	if handle := strings.ToLower(speaker.Username); handle != "" {
		for _, candidate := range handles {
			if candidate == handle {
				return true
			}
		}
		if strings.Contains(lowered, "@"+handle) {
			return true
		}
	}
	for _, name := range speaker.Names {
		if speakerNameDistinctive(name) && strings.Contains(lowered, strings.ToLower(name)) {
			return true
		}
	}
	return false
}

// speakerNameDistinctive 判断昵称是否够长、够特别，能拿来在正文里做子串比对。
// 单字、纯数字、过短的拉丁字母名（「a」「Li」）误伤太多。
func speakerNameDistinctive(name string) bool {
	name = strings.TrimSpace(name)
	runes := []rune(name)
	if len(runes) < 2 || len(runes) > 32 {
		return false
	}
	wide, digits := 0, 0
	for _, r := range runes {
		switch {
		case unicode.IsDigit(r):
			digits++
		case r > unicode.MaxASCII:
			wide++
		}
	}
	if digits == len(runes) {
		return false
	}
	return wide >= 2 || len(runes) >= 3
}
