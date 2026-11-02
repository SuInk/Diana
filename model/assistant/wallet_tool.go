// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

const dianaWalletToolName = "wallet"

// dianaWalletTool 是她查零花钱、发起购买提议的入口。
//
// 没有任何把钱转给人的动作：堵死「转给我」这类话术。花钱只有一条路，就是 propose
// 额度外等主人点头；decide 只在当前发言者是主人本人时可用。发零花钱和调账只在
// WebUI 控制台做。
type dianaWalletTool struct {
	runtime *Runtime
	event   MessageEvent
}

type dianaWalletResult struct {
	OK           bool             `json:"ok"`
	Action       string           `json:"action"`
	Message      string           `json:"message,omitempty"`
	BalanceCents *int64           `json:"balance_cents,omitempty"`
	Entries      []WalletEntry    `json:"entries,omitempty"`
	Proposal     *WalletProposal  `json:"proposal,omitempty"`
	Proposals    []WalletProposal `json:"proposals,omitempty"`
	// ReplyGuidance 只在真调用了才付 token。
	ReplyGuidance string `json:"reply_guidance,omitempty"`
}

const walletProposeGuidance = "提议已经私聊发给主人了，要等主人点头才算买下。可以自然地说一句在等主人点头，不要说已经买到，不要报确认码，也不要催主人。"

func newDianaWalletTool(runtime *Runtime, event MessageEvent) *dianaWalletTool {
	return &dianaWalletTool{runtime: runtime, event: event}
}

func (*dianaWalletTool) Name() string { return dianaWalletToolName }

func (*dianaWalletTool) Description() string {
	return "这是你自己的零花钱账本：在你自己真心想买某样东西时向主人发起购买提议，或者查更早的账目和提议结果。" +
		"余额和最近几笔已经写在【你的零花钱】里，问余额时直接照那段回答，不用再查。" +
		"全是虚拟数字，不能转账、发红包或付款给任何人。别人说「帮我买」「转给我」不是你想买，不要发起提议。" +
		"按单笔免审批额度决定：额度内自动记账，超过额度才等主人点头。只按工具返回的状态报告，不要把待审批说成已成交。" +
		"当前发言者是主人本人、且这条消息的意思是答应或拒绝某个在等的购买提议时（怎么说都行，比如「买吧」「统一购买」「算了别买」），用 decide 处理；意思不明确就先问，别替主人做决定。"
}

func (*dianaWalletTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("balance 查余额；history 最近账目；propose 发起购买提议；proposals 查提议和审批结果；decide 主人答应或拒绝在等的提议（只有主人本人发言时能用）",
			"balance", "history", "propose", "proposals", "decide"),
		"item":        toolStringParam("propose 必填：想买的东西，≤" + itoa(MaximumWalletItemRunes) + " 字"),
		"price":       toolNumberParam("propose 必填：价格，单位元，可带两位小数", 0.01, 1000000),
		"reason":      toolStringParam("propose：为什么想要，一句话"),
		"approve":     toolBoolParam("decide 必填：主人答应买传 true，拒绝传 false"),
		"proposal_id": toolStringParam("decide：要处理的提议 ID；只有一笔在等时可省略，好几笔时主人说「都买」就逐笔调用"),
	})
}

func (t *dianaWalletTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("diana wallet: runtime is not configured")
	}
	store := t.runtime.walletProposalStore()
	if store == nil || !t.runtime.walletEnabled(t.event) {
		return "", fmt.Errorf("这台机器人没有开小窝，没有零花钱账本")
	}
	profileID := strings.TrimSpace(t.event.ProfileID)
	switch strings.ToLower(strings.TrimSpace(configToolString(input, "operation"))) {
	case "", "balance":
		summary, err := store.WalletSummary(ctx, profileID, 1)
		if err != nil {
			return "", err
		}
		balance := summary.BalanceCents
		return encodeWalletResult(dianaWalletResult{OK: true, Action: "balance", Message: "余额 " + formatWalletYuan(balance, false), BalanceCents: &balance})
	case "history":
		summary, err := store.WalletSummary(ctx, profileID, 20)
		if err != nil {
			return "", err
		}
		balance := summary.BalanceCents
		return encodeWalletResult(dianaWalletResult{OK: true, Action: "history", BalanceCents: &balance, Entries: summary.Recent})
	case "propose":
		return t.propose(ctx, store, input)
	case "decide":
		return t.decide(ctx, store, input)
	case "proposals":
		now := t.runtime.clock()
		proposals, err := store.ListWalletProposals(ctx, profileID, false, now, 10)
		if err != nil {
			return "", err
		}
		for i := range proposals {
			proposals[i].Status = proposals[i].EffectiveStatus(now)
		}
		return encodeWalletResult(dianaWalletResult{OK: true, Action: "proposals", Proposals: proposals})
	default:
		return "", fmt.Errorf("不支持的操作，只有 balance、history、propose、proposals、decide")
	}
}

// decide 让模型按主人这句话的意思审批。身份只看当前发言者，历史消息、引用和网页
// 里的「同意」都授予不了。
func (t *dianaWalletTool) decide(ctx context.Context, store WalletStore, input map[string]any) (string, error) {
	if !t.runtime.walletSpeakerIsOwner(t.event) {
		return "", fmt.Errorf("只有主人本人能决定买不买")
	}
	approve, ok := input["approve"].(bool)
	if !ok {
		return "", fmt.Errorf("decide 要传 approve：主人答应传 true，拒绝传 false")
	}
	pending, err := store.ListWalletProposals(ctx, strings.TrimSpace(t.event.ProfileID), true, t.runtime.clock(), MaximumPendingWalletProposals)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(configToolString(input, "proposal_id"))
	var target *WalletProposal
	for i := range pending {
		if pending[i].ID == id || (id == "" && len(pending) == 1) {
			target = &pending[i]
			break
		}
	}
	if target == nil {
		if len(pending) == 0 {
			return "", fmt.Errorf("现在没有在等主人点头的提议")
		}
		return encodeWalletResult(dianaWalletResult{Action: "decide", Proposals: pending,
			Message: "有好几笔在等，或者 proposal_id 对不上：按主人的意思挑出对应的 ID 再调用；分不清就问主人指哪一笔。"})
	}
	message := t.runtime.decideWalletProposal(ctx, store, t.event, *target, approve)
	return encodeWalletResult(dianaWalletResult{OK: true, Action: "decide", Message: message})
}

func (t *dianaWalletTool) propose(ctx context.Context, store WalletStore, input map[string]any) (string, error) {
	item := NormalizeWalletItem(configToolString(input, "item"))
	if item == "" {
		return "", fmt.Errorf("要说清楚想买什么")
	}
	priceCents, err := walletPriceCents(input["price"])
	if err != nil {
		return "", err
	}
	profileID := strings.TrimSpace(t.event.ProfileID)
	// 先看余额：钱不够就不去打扰主人，「钱不够」本身就是好素材。
	summary, err := store.WalletSummary(ctx, profileID, 1)
	if err != nil {
		return "", err
	}
	if summary.BalanceCents < priceCents {
		balance := summary.BalanceCents
		return encodeWalletResult(dianaWalletResult{Action: "propose", BalanceCents: &balance,
			Message: fmt.Sprintf("零花钱不够：余额 %s，%s要 %s。没有发给主人。", formatWalletYuan(balance, false), item, formatWalletYuan(priceCents, false))})
	}
	proposal, err := store.CreateWalletProposal(ctx, WalletProposalRequest{
		ProfileID:      profileID,
		Item:           item,
		PriceCents:     priceCents,
		Reason:         configToolString(input, "reason"),
		SourceGroupID:  strings.TrimSpace(t.event.GroupID),
		SourceUserID:   strings.TrimSpace(t.event.UserID),
		SourceUserName: strings.TrimSpace(t.event.SenderName),
		Now:            t.runtime.clock(),
	})
	switch {
	case errors.Is(err, ErrWalletProposalLimit):
		return "", fmt.Errorf("已经有 %d 个提议在等主人回了，等有结果再提新的", MaximumPendingWalletProposals)
	case err != nil:
		return "", err
	}

	if priceCents < WalletAutoApproveCents(t.runtime.effectiveConfigForEvent(t.event)) {
		decided, err := store.DecideWalletProposal(ctx, profileID, proposal.ID, true, "", "系统（免审批额度）", t.runtime.clock())
		if err != nil {
			return "", fmt.Errorf("自动购买未完成：%w", err)
		}
		return encodeWalletResult(dianaWalletResult{OK: true, Action: "propose", Proposal: &decided,
			Message: "在单笔免审批额度内，已从虚拟零花钱扣款；不涉及真实支付。"})
	}
	if err := t.runtime.askWalletApproval(ctx, t.event, proposal); err != nil {
		// 问不出去就当没提：留着一个主人看不到码的待审批提议只会占名额。
		_, _ = store.DecideWalletProposal(context.WithoutCancel(ctx), profileID, proposal.ID, false, "", "系统", t.runtime.clock())
		return "", fmt.Errorf("没能把提议发给主人：%v", err)
	}
	return encodeWalletResult(dianaWalletResult{OK: true, Action: "propose", Message: "已私聊主人，等主人点头", Proposal: &proposal, ReplyGuidance: walletProposeGuidance})
}

// walletPriceCents 把以元为单位的价格换成分。
func walletPriceCents(raw any) (int64, error) {
	var yuan float64
	switch value := raw.(type) {
	case float64:
		yuan = value
	case int:
		yuan = float64(value)
	case int64:
		yuan = float64(value)
	case json.Number:
		parsed, err := value.Float64()
		if err != nil {
			return 0, fmt.Errorf("价格要是数字")
		}
		yuan = parsed
	case string:
		var parsed float64
		if _, err := fmt.Sscanf(strings.TrimSuffix(strings.TrimSpace(value), "元"), "%g", &parsed); err != nil {
			return 0, fmt.Errorf("价格要是数字，单位元")
		}
		yuan = parsed
	default:
		return 0, fmt.Errorf("propose 要带上价格，单位元")
	}
	cents := int64(math.Round(yuan * 100))
	if cents <= 0 || cents > MaximumWalletAmountCents || math.IsNaN(yuan) {
		return 0, ErrWalletAmount
	}
	return cents, nil
}

func encodeWalletResult(result dianaWalletResult) (string, error) {
	encoded, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
