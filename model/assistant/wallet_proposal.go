// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"
)

// 购买提议：她想买某样东西时记一份提议，私聊主人要确认码，主人回码才扣款。
// 设计见 docs/wallet.md「购买提议」一节。
//
// 确认码由提议内容哈希得出，只在主人本人的消息里认：别人说「同意」、网页正文里
// 写着码都放行不了。认码沿用编码任务审批的 containsStandaloneCode。
// 账本是虚拟的：成交只是记一笔 spend，不触发任何真实付款。

// WalletProposalStatus 是提议的状态。
type WalletProposalStatus string

const (
	WalletProposalPending  WalletProposalStatus = "pending"
	WalletProposalApproved WalletProposalStatus = "approved"
	WalletProposalRejected WalletProposalStatus = "rejected"
	// WalletProposalExpired 不落库：待审批的提议过了 ExpiresAt 读出来就按过期算，
	// 不需要后台任务去扫。
	WalletProposalExpired WalletProposalStatus = "expired"
)

const (
	// WalletProposalTTL 是等主人回码的时限，过了就作废，余额不动。
	WalletProposalTTL = 24 * time.Hour
	// MaximumPendingWalletProposals 限制同时待审批的提议数。主人一次被问三件就够了，
	// 再多就是在刷屏。
	MaximumPendingWalletProposals = 3
	// MaximumWalletItemRunes 是物品名的长度上限。
	MaximumWalletItemRunes   = 40
	walletProposalCodeLength = 6
)

// ErrWalletProposalLimit 表示待审批的提议已经满了。
var ErrWalletProposalLimit = errors.New("待主人审批的提议已经有 3 个了")

// ErrWalletProposalClosed 表示提议已经处理过或过期。
var ErrWalletProposalClosed = errors.New("这个提议已经处理过或过期了")

// WalletProposal 是一份购买提议。
type WalletProposal struct {
	ID         string               `json:"id"`
	ProfileID  string               `json:"profile_id,omitempty"`
	Item       string               `json:"item"`
	PriceCents int64                `json:"price_cents"`
	Reason     string               `json:"reason,omitempty"`
	Status     WalletProposalStatus `json:"status"`
	// Source* 记下是在哪个会话、谁在场时提的，主人审批时要看得到。
	SourceGroupID  string    `json:"source_group_id,omitempty"`
	SourceUserID   string    `json:"source_user_id,omitempty"`
	SourceUserName string    `json:"source_user_name,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	ExpiresAt      time.Time `json:"expires_at"`
	DecidedAt      time.Time `json:"decided_at,omitempty"`
	// SpendEntryID 是成交时记下的那笔账。
	SpendEntryID string `json:"spend_entry_id,omitempty"`
}

// EffectiveStatus 把过了时限的待审批提议算成过期。
func (p WalletProposal) EffectiveStatus(now time.Time) WalletProposalStatus {
	if p.Status == WalletProposalPending && !p.ExpiresAt.IsZero() && !now.Before(p.ExpiresAt) {
		return WalletProposalExpired
	}
	return p.Status
}

// WalletProposalRequest 是新建一份提议。
type WalletProposalRequest struct {
	ProfileID      string
	Item           string
	PriceCents     int64
	Reason         string
	SourceGroupID  string
	SourceUserID   string
	SourceUserName string
	Now            time.Time
}

// WalletStore 是钱包的完整存储。
type WalletStore interface {
	WalletSummaryStore
	// CreateWalletProposal 新建一份待审批提议。待审批的已满时返回 ErrWalletProposalLimit。
	CreateWalletProposal(context.Context, WalletProposalRequest) (WalletProposal, error)
	// ListWalletProposals 列出最近的提议，新的在前；pendingOnly 只要还没过期的待审批。
	ListWalletProposals(ctx context.Context, profileID string, pendingOnly bool, now time.Time, limit int) ([]WalletProposal, error)
	// DecideWalletProposal 审批一份提议。批准时在同一个事务里扣款，余额不够返回
	// ErrWalletInsufficient 且提议保持待审批；已处理或过期返回 ErrWalletProposalClosed。
	DecideWalletProposal(ctx context.Context, profileID, id string, approve bool, actorUserID, actorName string, now time.Time) (WalletProposal, error)
}

// NormalizeWalletItem 把物品名压成一行并截到上限。
func NormalizeWalletItem(item string) string {
	item = strings.Join(strings.Fields(item), " ")
	if utf8.RuneCountInString(item) <= MaximumWalletItemRunes {
		return item
	}
	return string([]rune(item)[:MaximumWalletItemRunes])
}

// walletProposalCodes 由提议内容算出同意码和拒绝码。
func walletProposalCodes(proposal WalletProposal) (string, string) {
	seed := strings.Join([]string{"wallet", proposal.ProfileID, proposal.ID, proposal.Item, fmt.Sprint(proposal.PriceCents)}, "\x00")
	allow := sha256.Sum256([]byte("allow\x00" + seed))
	deny := sha256.Sum256([]byte("deny\x00" + seed))
	return hex.EncodeToString(allow[:])[:walletProposalCodeLength], hex.EncodeToString(deny[:])[:walletProposalCodeLength]
}

// walletProposalStore 返回支持提议的钱包存储；只注入了只读概况时返回 nil。
func (r *Runtime) walletProposalStore() WalletStore {
	store, _ := r.walletStore().(WalletStore)
	return store
}

// walletEnabled 报告这一轮能不能用钱包：开了小窝、有机器人身份、配了存储。
func (r *Runtime) walletEnabled(event MessageEvent) bool {
	if r.walletStore() == nil || strings.TrimSpace(event.ProfileID) == "" {
		return false
	}
	return OwnSpaceEnabled(r.effectiveConfigForEvent(event))
}

// askWalletApproval 私聊主人要确认码。主人就在当前私聊里时也照发一条：码要出现在
// 一条独立的、由运行时写的消息里，不能指望模型把它原样转述。
func (r *Runtime) askWalletApproval(ctx context.Context, event MessageEvent, proposal WalletProposal) error {
	cfg := r.effectiveConfigForEvent(event)
	ownerID := strings.TrimSpace(cfg.OwnerID)
	if ownerID == "" {
		return fmt.Errorf("这台机器人没有设置主人，没人能批准")
	}
	allowCode, denyCode := walletProposalCodes(proposal)
	where := "私聊"
	if proposal.SourceGroupID != "" {
		where = "群 " + proposal.SourceGroupID
	}
	who := firstNonEmpty(proposal.SourceUserName, proposal.SourceUserID)
	if who != "" {
		where += "，和 " + who + " 聊天时"
	}
	answer := fmt.Sprintf("同意就回 %s，不同意回 %s", allowCode, denyCode)
	if walletQuickApprovable(cfg, proposal) {
		answer = "同意就回「同意」，不同意回「不要」"
	}
	message := fmt.Sprintf("我想用零花钱买：%s，%s。\n理由：%s\n（%s提的）\n\n%s。%d 小时内没回就作废，钱不动。",
		proposal.Item, formatWalletYuan(proposal.PriceCents, false), firstNonEmpty(proposal.Reason, "没写"),
		where, answer, int(WalletProposalTTL/time.Hour))
	target := MessageEvent{Kind: EventKindPrivate, ProfileID: event.ProfileID, Platform: event.Platform, SelfID: event.SelfID, UserID: ownerID}
	return r.sendSubscriberNotice(ctx, target, message)
}

// handleWalletApprovalReply 认出主人回过来的购买确认码，在进入模型那一轮之前处理。
func (r *Runtime) handleWalletApprovalReply(event MessageEvent, text string) (string, bool) {
	proposal, approve, ok := r.matchWalletApprovalCode(event, text)
	if !ok {
		return "", false
	}
	store := r.walletProposalStore()
	ctx, cancel := context.WithTimeout(context.Background(), walletLoadTimeout)
	defer cancel()
	decided, err := store.DecideWalletProposal(ctx, proposal.ProfileID, proposal.ID, approve,
		strings.TrimSpace(event.UserID), strings.TrimSpace(event.SenderName), r.clock())
	switch {
	case errors.Is(err, ErrWalletInsufficient):
		return fmt.Sprintf("零花钱不够买「%s」了，这次没扣。提议还留着，补了零花钱再回一次码就行。", proposal.Item), true
	case errors.Is(err, ErrWalletProposalClosed):
		return "这个购买提议已经处理过或过期了。", true
	case err != nil:
		log.Printf("diana wallet proposal decide failed: %v", err)
		return "处理购买提议失败了，请稍后再试或看服务日志。", true
	}
	if decided.Status == WalletProposalApproved {
		return fmt.Sprintf("好，「%s」买下了，扣了 %s。", decided.Item, formatWalletYuan(decided.PriceCents, false)), true
	}
	return fmt.Sprintf("好，「%s」不买了，钱没动。", decided.Item), true
}

// walletApprovalCodePending 报告消息里有没有待审批的购买确认码，只看不处理。
func (r *Runtime) walletApprovalCodePending(event MessageEvent, text string) bool {
	_, _, ok := r.matchWalletApprovalCode(event, text)
	return ok
}

func (r *Runtime) matchWalletApprovalCode(event MessageEvent, text string) (WalletProposal, bool, bool) {
	if r == nil || !r.walletEnabled(event) {
		return WalletProposal{}, false, false
	}
	// 只认主人本人。管理员也能走到主人命令这一层，但花钱只有主人能点头。
	ownerID := strings.TrimSpace(r.effectiveConfigForEvent(event).OwnerID)
	if ownerID == "" || strings.TrimSpace(event.UserID) != ownerID {
		return WalletProposal{}, false, false
	}
	store := r.walletProposalStore()
	if store == nil {
		return WalletProposal{}, false, false
	}
	cfg := r.effectiveConfigForEvent(event)
	if approve, ok := walletQuickReply(text); ok && event.Kind == EventKindPrivate && WalletQuickApproveCents(cfg) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), walletLoadTimeout)
		defer cancel()
		pending, err := store.ListWalletProposals(ctx, strings.TrimSpace(event.ProfileID), true, r.clock(), MaximumPendingWalletProposals)
		if err != nil {
			return WalletProposal{}, false, false
		}
		// 新的在前：主人回「同意」时说的是刚收到的那一条。额度外的提议跳过，
		// 那些只认确认码。
		for _, proposal := range pending {
			if walletQuickApprovable(cfg, proposal) {
				return proposal, approve, true
			}
		}
		return WalletProposal{}, false, false
	}
	lowered := strings.ToLower(text)
	// 码是 6 位十六进制，消息里连一段都没有就别去查库：主人的每条消息都会走到这里。
	if !containsHexRun(lowered, walletProposalCodeLength) {
		return WalletProposal{}, false, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), walletLoadTimeout)
	defer cancel()
	pending, err := store.ListWalletProposals(ctx, strings.TrimSpace(event.ProfileID), true, r.clock(), MaximumPendingWalletProposals)
	if err != nil {
		return WalletProposal{}, false, false
	}
	for _, proposal := range pending {
		allowCode, denyCode := walletProposalCodes(proposal)
		if containsStandaloneCode(lowered, allowCode) {
			return proposal, true, true
		}
		if containsStandaloneCode(lowered, denyCode) {
			return proposal, false, true
		}
	}
	return WalletProposal{}, false, false
}

// walletQuickApprovable 报告这份提议在不在「点一下就买」的额度内。
func walletQuickApprovable(cfg BotConfig, proposal WalletProposal) bool {
	quick := WalletQuickApproveCents(cfg)
	return quick > 0 && proposal.PriceCents > 0 && proposal.PriceCents <= quick
}

// walletQuickReply 认主人私聊里回的一句话。整条消息就是这几个词之一才算：
// 「好的，不过明天再说」这种夹在句子里的不认，免得聊着天顺手把钱花了。
func walletQuickReply(text string) (approve bool, ok bool) {
	word := strings.Trim(strings.TrimSpace(text), "。.!！~～ ")
	switch word {
	case "同意", "买", "买吧", "可以", "批准":
		return true, true
	case "不要", "不同意", "不买", "算了", "拒绝", "不行":
		return false, true
	}
	return false, false
}

func containsHexRun(lowered string, length int) bool {
	run := 0
	for i := 0; i < len(lowered); i++ {
		c := lowered[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			run++
			if run >= length {
				return true
			}
			continue
		}
		run = 0
	}
	return false
}
