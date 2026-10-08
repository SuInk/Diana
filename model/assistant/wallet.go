// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

// 小窝里的零花钱账本。全是虚拟数字，不接任何真实支付；设计见 docs/wallet.md。
//
// 账本只追加不修改：每一笔都留一行，余额以最新一行的 balance_after 为准。
// 金额一律以「分」存整数，浮点在加加减减之后会出现 0.30000000000000004 元。

// WalletEntryKind 是一笔账的来源。
type WalletEntryKind string

const (
	// WalletEntryAllowance 是主人发的零花钱。
	WalletEntryAllowance WalletEntryKind = "allowance"
	// WalletEntryAdjust 是主人手动调账，可正可负。
	WalletEntryAdjust WalletEntryKind = "adjust"
	// WalletEntrySpend 是她自己花掉的。
	WalletEntrySpend WalletEntryKind = "spend"
	// WalletEntryRefund 是退回的花费。
	WalletEntryRefund WalletEntryKind = "refund"
)

// MaximumWalletAmountCents 是单笔金额的绝对值上限（一百万元）。账本是虚拟的，
// 这个上限只防手滑多打几个零把余额推到 int64 溢出附近。
const MaximumWalletAmountCents int64 = 100_000_000

// MaximumWalletReasonRunes 是备注的长度上限。
const MaximumWalletReasonRunes = 120

// ErrWalletInsufficient 表示扣款后余额会变成负数。
var ErrWalletInsufficient = errors.New("零花钱不够")

// ErrWalletAmount 表示金额为零或超过单笔上限。
var ErrWalletAmount = errors.New("金额必须非零，且单笔不超过一百万元")

// WalletEntry 是账本里的一行。
type WalletEntry struct {
	ID           string          `json:"id"`
	ProfileID    string          `json:"profile_id,omitempty"`
	Kind         WalletEntryKind `json:"kind"`
	AmountCents  int64           `json:"amount_cents"`
	BalanceAfter int64           `json:"balance_after_cents"`
	Reason       string          `json:"reason,omitempty"`
	// Actor* 记下这笔是谁记的：控制台操作是「控制台」，以后对话里的花费是触发的会话。
	ActorUserID string    `json:"actor_user_id,omitempty"`
	ActorName   string    `json:"actor_name,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// WalletWriteRequest 是记一笔账。
type WalletWriteRequest struct {
	ProfileID   string
	Kind        WalletEntryKind
	AmountCents int64
	Reason      string
	ActorUserID string
	ActorName   string
	Now         time.Time
}

// WalletSummary 是小窝页面顶部要的那几个数。
type WalletSummary struct {
	BalanceCents int64         `json:"balance_cents"`
	Recent       []WalletEntry `json:"recent"`
}

// ValidWalletEntryKind 报告 kind 是否是已知的一种。
func ValidWalletEntryKind(kind WalletEntryKind) bool {
	switch kind {
	case WalletEntryAllowance, WalletEntryAdjust, WalletEntrySpend, WalletEntryRefund:
		return true
	}
	return false
}

// NormalizeWalletReason 把备注压成一行并截到上限。
func NormalizeWalletReason(reason string) string {
	reason = strings.Join(strings.Fields(reason), " ")
	if utf8.RuneCountInString(reason) <= MaximumWalletReasonRunes {
		return reason
	}
	return string([]rune(reason)[:MaximumWalletReasonRunes])
}

// OwnSpaceEnabled 报告这份配置有没有开小窝。
func OwnSpaceEnabled(cfg BotConfig) bool {
	return boolValue(cfg.OwnSpaceEnabled, false)
}
