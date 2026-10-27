// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"sync"
	"time"
)

// QQ 官方机器人收不到 QQ 号，发送者只有 openid，主人配置里填 QQ 号永远对不上人。
// openid 对同一台机器人是固定的（私聊和各个群都一样），所以只要认一次就够了：
//
//   - 还没配主人时，每个会话里第一条发给机器人的消息会收到一次绑定提醒；
//   - 主人发「绑定主人」，机器人回一个只属于他的 6 位验证码；
//   - 主人把验证码填进控制台，控制台就把这个验证码对应的 openid 存成主人，之后不再提醒。
//
// 验证码谁都能拿到，但只有进得了控制台的人能填，所以认的是「能登录后台的那个人在聊天里的身份」。

const (
	ownerBindCodeTTL = 10 * time.Minute
	// ownerBindStateLimit 是验证码和「已提醒过」会话各自的条数上限，满了整份清空重来：
	// 多提醒一次、旧码失效都无害，不值得为它上 LRU。
	ownerBindStateLimit = 4096
)

var ownerBindCommandPattern = regexp.MustCompile(`^/?绑定主人$`)

// OwnerIDSaver 只落盘一台机器人的主人字段，绑定时用。
type OwnerIDSaver interface {
	SaveOwnerID(profileID, ownerID string) error
}

type ownerBindCode struct {
	userID    string
	expiresAt time.Time
}

type ownerBindState struct {
	mu sync.Mutex
	// codes 按 profile+验证码 索引；同一个人重新取码时旧码作废。
	codes    map[string]ownerBindCode
	reminded map[string]bool
}

func ownerBindSupported(cfg BotConfig) bool {
	return NormalizePlatformID(cfg.Platform) == PlatformQQOfficial
}

func ownerBindCodeKey(profileID, code string) string {
	return profileID + "\x00" + code
}

const ownerBindReminder = "提醒：我还没有绑定主人。QQ 官方机器人看不到 QQ 号，如果你是主人，请发「绑定主人」给我获取验证码，再填进 Diana 控制台。"

func ownerBindCodeReply(code string) string {
	return fmt.Sprintf("绑定验证码：%s（%d 分钟内有效）。请在 Diana 控制台打开这台机器人的设置，在「主人用户 ID」下填入它。", code, int(ownerBindCodeTTL.Minutes()))
}

// handleOwnerBind 处理还没主人的 QQ 官方机器人收到的消息：「绑定主人」就地回一个验证码并返回
// true；其他消息在这个会话第一次出现时补一条绑定提醒，然后照常往下走。
func (r *Runtime) handleOwnerBind(ctx context.Context, event MessageEvent) bool {
	if NormalizePlatformID(event.Platform) != PlatformQQOfficial || event.SenderIsBot || event.Outbound || strings.TrimSpace(event.UserID) == "" {
		return false
	}
	// 群里只认对机器人说的话；没 @ 机器人的全量群消息不提醒也不认指令。
	if event.Kind == EventKindGroup && !event.ToMe {
		return false
	}
	if event.Kind != EventKindGroup && event.Kind != EventKindPrivate {
		return false
	}
	profileID := r.eventProfileID(event)
	cfg := r.profileConfig(profileID)
	if !ownerBindSupported(cfg) || strings.TrimSpace(cfg.OwnerID) != "" {
		return false
	}
	text := strings.TrimSpace(PlainText(event.Segments))
	if text == "" {
		text = strings.TrimSpace(event.RawMessage)
	}
	if !ownerBindCommandPattern.MatchString(text) {
		if r.markOwnerBindReminded(profileID, event) {
			_ = r.sendOutgoing(ctx, event, OutgoingMessage{Text: ownerBindReminder, LiteralText: true})
		}
		return false
	}
	reply := "生成验证码失败，请稍后再试。"
	if code, err := r.issueOwnerBindCode(profileID, event.UserID); err == nil {
		reply = ownerBindCodeReply(code)
	}
	record := r.decisionEventRecord(event, "[绑定主人]", "replied")
	record.Reason = "QQ 官方机器人的主人绑定验证码"
	r.record(record)
	_ = r.sendOutgoing(ctx, event, OutgoingMessage{Text: reply, LiteralText: true})
	return true
}

// markOwnerBindReminded 报告这个会话是不是第一次需要提醒，并记下已提醒。
func (r *Runtime) markOwnerBindReminded(profileID string, event MessageEvent) bool {
	key := profileID + "\x00private\x00" + event.UserID
	if event.Kind == EventKindGroup {
		key = profileID + "\x00group\x00" + event.GroupID
	}
	r.ownerBind.mu.Lock()
	defer r.ownerBind.mu.Unlock()
	if r.ownerBind.reminded[key] {
		return false
	}
	if r.ownerBind.reminded == nil || len(r.ownerBind.reminded) >= ownerBindStateLimit {
		r.ownerBind.reminded = map[string]bool{}
	}
	r.ownerBind.reminded[key] = true
	return true
}

// issueOwnerBindCode 给发送者生成验证码，他之前拿到的码同时作废。
func (r *Runtime) issueOwnerBindCode(profileID, userID string) (string, error) {
	r.ownerBind.mu.Lock()
	defer r.ownerBind.mu.Unlock()
	if r.ownerBind.codes == nil || len(r.ownerBind.codes) >= ownerBindStateLimit {
		r.ownerBind.codes = map[string]ownerBindCode{}
	}
	prefix := profileID + "\x00"
	for key, pending := range r.ownerBind.codes {
		if strings.HasPrefix(key, prefix) && pending.userID == userID {
			delete(r.ownerBind.codes, key)
		}
	}
	for {
		n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
		if err != nil {
			return "", err
		}
		code := fmt.Sprintf("%06d", n.Int64())
		key := ownerBindCodeKey(profileID, code)
		if _, taken := r.ownerBind.codes[key]; taken {
			continue
		}
		r.ownerBind.codes[key] = ownerBindCode{userID: userID, expiresAt: r.clock().Add(ownerBindCodeTTL)}
		return code, nil
	}
}

// BindOwnerByCode 把控制台填入的验证码对应的发送者存成主人，返回主人 ID。
func (r *Runtime) BindOwnerByCode(profileID, code string) (string, error) {
	profileID = strings.TrimSpace(profileID)
	code = strings.TrimSpace(code)
	r.mu.RLock()
	cfg, ok := r.profileConfigs[profileID]
	saver, _ := r.configSaver.(OwnerIDSaver)
	r.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("目标机器人不存在")
	}
	if !ownerBindSupported(cfg) {
		return "", fmt.Errorf("只有 QQ 官方机器人需要绑定主人，其他平台直接填主人账号")
	}
	if strings.TrimSpace(cfg.OwnerID) != "" {
		return "", fmt.Errorf("这台机器人已经有主人了，要换人先清空主人并保存")
	}
	if saver == nil {
		return "", fmt.Errorf("当前没有接入可保存配置的存储")
	}
	key := ownerBindCodeKey(profileID, code)
	r.ownerBind.mu.Lock()
	pending, ok := r.ownerBind.codes[key]
	if ok {
		delete(r.ownerBind.codes, key)
	}
	r.ownerBind.mu.Unlock()
	if !ok || !r.clock().Before(pending.expiresAt) {
		return "", fmt.Errorf("验证码不对或已过期，请给机器人发「绑定主人」重新获取")
	}
	_, err := r.commitProfileChange(profileID, func(profile *BotConfig) error {
		if strings.TrimSpace(profile.OwnerID) != "" {
			return fmt.Errorf("这台机器人已经有主人了")
		}
		profile.OwnerID = pending.userID
		return nil
	}, func(BotConfig) error {
		return saver.SaveOwnerID(profileID, pending.userID)
	})
	if err != nil {
		return "", err
	}
	r.ownerBind.mu.Lock()
	prefix := profileID + "\x00"
	for key := range r.ownerBind.codes {
		if strings.HasPrefix(key, prefix) {
			delete(r.ownerBind.codes, key)
		}
	}
	r.ownerBind.mu.Unlock()
	return pending.userID, nil
}
