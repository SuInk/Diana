// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
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
//   - 主人在控制台生成 6 位验证码，把「绑定主人 123456」发给机器人（私聊或群里 @）；
//   - 验证码对上就把发送者的 openid 存成主人，之后不再提醒。
//
// 验证码只在控制台显示，聊天里谁先发都没用，除非他看得到控制台。

const (
	ownerBindCodeTTL = 10 * time.Minute
	// ownerBindReminderLimit 是记「已提醒过」的会话数上限，满了整份清空重来：
	// 多提醒一次无害，不值得为它上 LRU。
	ownerBindReminderLimit = 4096
	ownerBindReminder      = "提醒：我还没有绑定主人。QQ 官方机器人看不到 QQ 号，主人需要绑定一次：在 Diana 控制台打开这台机器人的设置，点「主人用户 ID」旁的「生成绑定验证码」，再把「绑定主人 验证码」发给我。"
)

var ownerBindCommandPattern = regexp.MustCompile(`^/?绑定主人\s*(\d{6})$`)

// OwnerIDSaver 只落盘一台机器人的主人字段，绑定时用。
type OwnerIDSaver interface {
	SaveOwnerID(profileID, ownerID string) error
}

type ownerBindCode struct {
	hash      string
	expiresAt time.Time
}

type ownerBindState struct {
	mu       sync.Mutex
	codes    map[string]ownerBindCode
	reminded map[string]bool
}

// OwnerBindStatus 是控制台轮询绑定进度时看到的状态。
type OwnerBindStatus struct {
	OwnerID          string `json:"owner_id"`
	Pending          bool   `json:"pending"`
	ExpiresInSeconds int    `json:"expires_in_seconds,omitempty"`
}

func ownerBindSupported(cfg BotConfig) bool {
	return NormalizePlatformID(cfg.Platform) == PlatformQQOfficial
}

func hashOwnerBindCode(profileID, code string) string {
	sum := sha256.Sum256([]byte("owner-bind:" + profileID + "\x00" + code))
	return hex.EncodeToString(sum[:])
}

// CreateOwnerBindCode 给一台 QQ 官方机器人生成绑定验证码，旧的同时作废。
func (r *Runtime) CreateOwnerBindCode(profileID string) (string, time.Duration, error) {
	profileID = strings.TrimSpace(profileID)
	r.mu.RLock()
	cfg, ok := r.profileConfigs[profileID]
	r.mu.RUnlock()
	if !ok {
		return "", 0, fmt.Errorf("目标机器人不存在")
	}
	if !ownerBindSupported(cfg) {
		return "", 0, fmt.Errorf("只有 QQ 官方机器人需要绑定主人，其他平台直接填主人账号")
	}
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", 0, err
	}
	code := fmt.Sprintf("%06d", n.Int64())
	r.ownerBind.mu.Lock()
	defer r.ownerBind.mu.Unlock()
	if r.ownerBind.codes == nil {
		r.ownerBind.codes = map[string]ownerBindCode{}
	}
	r.ownerBind.codes[profileID] = ownerBindCode{hash: hashOwnerBindCode(profileID, code), expiresAt: r.clock().Add(ownerBindCodeTTL)}
	return code, ownerBindCodeTTL, nil
}

// OwnerBindStatus 返回这台机器人当前的主人和验证码是否还在等待使用。
func (r *Runtime) OwnerBindStatus(profileID string) OwnerBindStatus {
	profileID = strings.TrimSpace(profileID)
	r.mu.RLock()
	cfg := r.profileConfigs[profileID]
	r.mu.RUnlock()
	status := OwnerBindStatus{OwnerID: strings.TrimSpace(cfg.OwnerID)}
	r.ownerBind.mu.Lock()
	defer r.ownerBind.mu.Unlock()
	if code, ok := r.ownerBind.codes[profileID]; ok {
		if remaining := code.expiresAt.Sub(r.clock()); remaining > 0 {
			status.Pending = true
			status.ExpiresInSeconds = int(remaining.Seconds())
		} else {
			delete(r.ownerBind.codes, profileID)
		}
	}
	return status
}

// handleOwnerBind 处理还没主人的 QQ 官方机器人收到的消息：绑定指令就地消费并返回
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
	if match := ownerBindCommandPattern.FindStringSubmatch(text); len(match) == 2 {
		reply := r.bindOwner(profileID, event.UserID, match[1])
		record := r.decisionEventRecord(event, "[绑定主人]", "replied")
		record.Reason = "QQ 官方机器人的主人绑定指令"
		r.record(record)
		_ = r.sendOutgoing(ctx, event, OutgoingMessage{Text: reply, LiteralText: true})
		return true
	}
	if r.markOwnerBindReminded(profileID, event) {
		_ = r.sendOutgoing(ctx, event, OutgoingMessage{Text: ownerBindReminder, LiteralText: true})
	}
	return false
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
	if r.ownerBind.reminded == nil || len(r.ownerBind.reminded) >= ownerBindReminderLimit {
		r.ownerBind.reminded = map[string]bool{}
	}
	r.ownerBind.reminded[key] = true
	return true
}

// bindOwner 校验验证码并把发送者存成主人，返回回给发送者的话。
func (r *Runtime) bindOwner(profileID, userID, code string) string {
	r.ownerBind.mu.Lock()
	pending, ok := r.ownerBind.codes[profileID]
	valid := ok && r.clock().Before(pending.expiresAt) &&
		subtle.ConstantTimeCompare([]byte(pending.hash), []byte(hashOwnerBindCode(profileID, code))) == 1
	if valid {
		// 先作废再落盘：同一个码不能被两个人先后用上。
		delete(r.ownerBind.codes, profileID)
	}
	r.ownerBind.mu.Unlock()
	if !valid {
		return "验证码不对或已过期，请在控制台重新生成后再发。"
	}
	r.mu.RLock()
	saver, _ := r.configSaver.(OwnerIDSaver)
	r.mu.RUnlock()
	if saver == nil {
		return "绑定失败：当前没有接入可保存配置的存储。"
	}
	_, err := r.commitProfileChange(profileID, func(profile *BotConfig) error {
		if strings.TrimSpace(profile.OwnerID) != "" {
			return fmt.Errorf("这台机器人已经有主人了")
		}
		profile.OwnerID = userID
		return nil
	}, func(BotConfig) error {
		return saver.SaveOwnerID(profileID, userID)
	})
	if err != nil {
		return "绑定失败：" + err.Error()
	}
	return "绑定成功，以后我就认你是主人了。"
}
