// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
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

const (
	// walletContextRecentEntries 是注入提示词的最近几笔。余额本身才是她要知道的事，
	// 这几笔只用来让「上周刚发了零花钱」「前两天调过账」这类话有据可依。
	walletContextRecentEntries = 5
	// walletLoadTimeout 是加载超时。查不到就当没有钱包，绝不拖回复。
	walletLoadTimeout = 2 * time.Second
)

// walletContextPrefix 声明这段是运行时给的事实账本，不是谁在提要求。少了这句，
// 模型会把「余额 50 元」读成有人在提醒她花钱或在讨钱。
const walletContextPrefix = "【你的零花钱】下面是你小窝里零花钱账本的当前情况，由系统记账，不是用户消息。" +
	"全是虚拟的数字，不能转账、发红包或真的付款给任何人；你自己真想买什么时可以用 wallet 工具向主人提议，主人回确认码才算买下。" +
	"聊到相关话题时自然地知道自己有多少钱即可，不要主动报账，也不要因为它就把话题转到钱上。\n"

// WalletSummaryStore 读钱包概况。没有它时注入为空。
type WalletSummaryStore interface {
	WalletSummary(ctx context.Context, profileID string, limit int) (WalletSummary, error)
}

// SetWalletStore 注入钱包存储。只给 WalletSummaryStore 时只注入余额，不注册钱包工具。
func (r *Runtime) SetWalletStore(store WalletSummaryStore) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.wallet = store
}

func (r *Runtime) walletStore() WalletSummaryStore {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.wallet
}

// walletContext 渲染本轮要注入的零花钱段落。只在开了小窝时注入；拿不到机器人
// 身份时不注入，理由同自述：没有 profile_id 就读不到属于这台机器人的那本账。
// 一笔账都没有时也不注入，免得没发过零花钱的机器人每轮都被告知「余额 0 元」。
func (r *Runtime) walletContext(ctx context.Context, event MessageEvent) string {
	store := r.walletStore()
	profileID := strings.TrimSpace(event.ProfileID)
	if store == nil || profileID == "" || !OwnSpaceEnabled(r.effectiveConfigForEvent(event)) {
		return ""
	}
	loadCtx, cancel := context.WithTimeout(ctx, walletLoadTimeout)
	defer cancel()
	summary, err := store.WalletSummary(loadCtx, profileID, walletContextRecentEntries)
	if err != nil || len(summary.Recent) == 0 {
		return ""
	}
	block := formatWalletContext(summary, profileLocation(profileID))
	if proposals := r.walletProposalStore(); proposals != nil {
		pending, err := proposals.ListWalletProposals(loadCtx, profileID, true, r.clock(), MaximumPendingWalletProposals)
		if err == nil && len(pending) > 0 {
			items := make([]string, 0, len(pending))
			for _, proposal := range pending {
				items = append(items, proposal.Item+" "+formatWalletYuan(proposal.PriceCents, false))
			}
			block += "\n在等主人点头：" + strings.Join(items, "、")
		}
	}
	return block
}

// formatWalletContext 拼出段落：一行余额，加最近几笔，新的在前。
func formatWalletContext(summary WalletSummary, loc *time.Location) string {
	var builder strings.Builder
	builder.WriteString(walletContextPrefix)
	builder.WriteString("余额：")
	builder.WriteString(formatWalletYuan(summary.BalanceCents, false))
	builder.WriteString("\n最近几笔：")
	for _, entry := range summary.Recent {
		builder.WriteString("\n- ")
		builder.WriteString(entry.CreatedAt.In(loc).Format("01-02"))
		builder.WriteString(" ")
		builder.WriteString(walletEntryKindLabel(entry.Kind))
		builder.WriteString(" ")
		builder.WriteString(formatWalletYuan(entry.AmountCents, true))
		if reason := NormalizeWalletReason(entry.Reason); reason != "" {
			builder.WriteString("（")
			builder.WriteString(reason)
			builder.WriteString("）")
		}
	}
	return builder.String()
}

func walletEntryKindLabel(kind WalletEntryKind) string {
	switch kind {
	case WalletEntryAllowance:
		return "主人发零花钱"
	case WalletEntryAdjust:
		return "主人调账"
	case WalletEntrySpend:
		return "花掉"
	case WalletEntryRefund:
		return "退回"
	}
	return string(kind)
}

// formatWalletYuan 把分写成「12.50 元」。signed 为真时正数带加号，用在流水上。
func formatWalletYuan(cents int64, signed bool) string {
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	} else if signed {
		sign = "+"
	}
	return fmt.Sprintf("%s%d.%02d 元", sign, cents/100, cents%100)
}
