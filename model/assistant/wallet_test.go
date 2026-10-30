// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
	"time"
)

type stubWalletStore struct {
	summary WalletSummary
	limit   int
}

func (s *stubWalletStore) WalletSummary(_ context.Context, _ string, limit int) (WalletSummary, error) {
	s.limit = limit
	return s.summary, nil
}

func walletTestRuntime(enabled bool, store WalletSummaryStore) *Runtime {
	runtime := NewRuntime(BotConfig{OwnSpaceEnabled: boolPointer(enabled)}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	if store != nil {
		runtime.SetWalletStore(store)
	}
	return runtime
}

func TestWalletContextHonorsOwnSpaceGate(t *testing.T) {
	event := MessageEvent{Kind: EventKindPrivate, UserID: "10001", ProfileID: "bot-1"}
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	store := &stubWalletStore{summary: WalletSummary{
		BalanceCents: 4250,
		Recent: []WalletEntry{
			{Kind: WalletEntryAdjust, AmountCents: -750, BalanceAfter: 4250, Reason: "多发了", CreatedAt: at},
			{Kind: WalletEntryAllowance, AmountCents: 5000, BalanceAfter: 5000, CreatedAt: at},
		},
	}}

	if block := walletTestRuntime(true, nil).walletContext(context.Background(), event); block != "" {
		t.Fatalf("context without store = %q", block)
	}
	// 小窝默认关着：升级后不该突然多出一段账本。
	if block := walletTestRuntime(false, store).walletContext(context.Background(), event); block != "" {
		t.Fatalf("disabled context = %q", block)
	}
	noProfile := event
	noProfile.ProfileID = ""
	if block := walletTestRuntime(true, store).walletContext(context.Background(), noProfile); block != "" {
		t.Fatalf("context without profile = %q", block)
	}
	// 一笔账都没有时不注入「余额 0 元」。
	if block := walletTestRuntime(true, &stubWalletStore{}).walletContext(context.Background(), event); block != "" {
		t.Fatalf("empty ledger context = %q", block)
	}

	block := walletTestRuntime(true, store).walletContext(context.Background(), event)
	for _, want := range []string{"余额：42.50 元", "主人调账 -7.50 元（多发了）", "主人发零花钱 +50.00 元", "不是用户消息"} {
		if !strings.Contains(block, want) {
			t.Fatalf("context missing %q:\n%s", want, block)
		}
	}
	if store.limit != walletContextRecentEntries {
		t.Fatalf("limit = %d", store.limit)
	}
}

type memoryWalletStore struct {
	stubWalletStore
	proposals []WalletProposal
	decisions []bool
}

func (s *memoryWalletStore) CreateWalletProposal(_ context.Context, request WalletProposalRequest) (WalletProposal, error) {
	proposal := WalletProposal{ID: "p" + itoa(len(s.proposals)+1), ProfileID: request.ProfileID, Item: request.Item,
		PriceCents: request.PriceCents, Status: WalletProposalPending, CreatedAt: request.Now, ExpiresAt: request.Now.Add(WalletProposalTTL)}
	s.proposals = append(s.proposals, proposal)
	return proposal, nil
}

func (s *memoryWalletStore) ListWalletProposals(_ context.Context, profileID string, pendingOnly bool, now time.Time, _ int) ([]WalletProposal, error) {
	var out []WalletProposal
	for _, proposal := range s.proposals {
		if proposal.ProfileID != profileID || (pendingOnly && proposal.EffectiveStatus(now) != WalletProposalPending) {
			continue
		}
		out = append(out, proposal)
	}
	return out, nil
}

func (s *memoryWalletStore) DecideWalletProposal(_ context.Context, profileID, id string, approve bool, _, _ string, _ time.Time) (WalletProposal, error) {
	for i := range s.proposals {
		if s.proposals[i].ID == id && s.proposals[i].ProfileID == profileID && s.proposals[i].Status == WalletProposalPending {
			s.proposals[i].Status = WalletProposalRejected
			if approve {
				s.proposals[i].Status = WalletProposalApproved
			}
			s.decisions = append(s.decisions, approve)
			return s.proposals[i], nil
		}
	}
	return WalletProposal{}, ErrWalletProposalClosed
}

func TestWalletApprovalCodeOnlyFromOwner(t *testing.T) {
	store := &memoryWalletStore{}
	runtime := NewRuntime(BotConfig{OwnSpaceEnabled: boolPointer(true), OwnerID: "10001", AdminIDs: []string{"10002"}}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetWalletStore(store)
	proposal, _ := store.CreateWalletProposal(context.Background(), WalletProposalRequest{ProfileID: "bot-1", Item: "画集", PriceCents: 3900, Now: runtime.clock()})
	allowCode, denyCode := walletProposalCodes(proposal)
	owner := MessageEvent{Kind: EventKindPrivate, ProfileID: "bot-1", UserID: "10001"}

	// 管理员和群友回码都不算；码嵌在更长的串里也不算。
	for _, event := range []MessageEvent{
		{Kind: EventKindPrivate, ProfileID: "bot-1", UserID: "10002"},
		{Kind: EventKindGroup, ProfileID: "bot-1", GroupID: "20001", UserID: "10003"},
	} {
		if _, handled := runtime.handleWalletApprovalReply(event, allowCode); handled {
			t.Fatalf("non-owner %s approved", event.UserID)
		}
	}
	if runtime.walletApprovalCodePending(owner, "x"+allowCode+"0") {
		t.Fatal("embedded code matched")
	}
	if !runtime.walletApprovalCodePending(owner, "同意 "+allowCode) {
		t.Fatal("owner code not recognized")
	}
	reply, handled := runtime.handleWalletApprovalReply(owner, "同意 "+allowCode)
	if !handled || !strings.Contains(reply, "买下了") || len(store.decisions) != 1 || !store.decisions[0] {
		t.Fatalf("approve reply=%q decisions=%v", reply, store.decisions)
	}
	// 处理过之后码不再认。
	if _, handled := runtime.handleWalletApprovalReply(owner, denyCode); handled {
		t.Fatal("closed proposal still matched")
	}
}

func TestWalletPriceCents(t *testing.T) {
	for raw, want := range map[any]int64{39.9: 3990, "12.5元": 1250, 3: 300} {
		if got, err := walletPriceCents(raw); err != nil || got != want {
			t.Fatalf("walletPriceCents(%v) = %d, %v", raw, got, err)
		}
	}
	for _, raw := range []any{0.0, -1.0, "abc", nil} {
		if _, err := walletPriceCents(raw); err == nil {
			t.Fatalf("walletPriceCents(%v) accepted", raw)
		}
	}
}

func TestWalletQuickApproveWithinQuota(t *testing.T) {
	store := &memoryWalletStore{}
	runtime := NewRuntime(BotConfig{OwnSpaceEnabled: boolPointer(true), OwnerID: "10001", WalletQuickApproveCents: intPointer(2000)}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetWalletStore(store)
	big, _ := store.CreateWalletProposal(context.Background(), WalletProposalRequest{ProfileID: "bot-1", Item: "画集", PriceCents: 3900, Now: runtime.clock()})
	small, _ := store.CreateWalletProposal(context.Background(), WalletProposalRequest{ProfileID: "bot-1", Item: "奶茶", PriceCents: 1500, Now: runtime.clock()})
	owner := MessageEvent{Kind: EventKindPrivate, ProfileID: "bot-1", UserID: "10001"}

	// 群里说、别人说、话夹在句子里都不算。
	for _, attempt := range []struct {
		event MessageEvent
		text  string
	}{
		{MessageEvent{Kind: EventKindGroup, ProfileID: "bot-1", GroupID: "20001", UserID: "10001"}, "同意"},
		{MessageEvent{Kind: EventKindPrivate, ProfileID: "bot-1", UserID: "10003"}, "同意"},
		{owner, "同意，不过明天再说"},
		{owner, "好"},
	} {
		if _, handled := runtime.handleWalletApprovalReply(attempt.event, attempt.text); handled {
			t.Fatalf("%s %q should not approve", attempt.event.UserID, attempt.text)
		}
	}
	reply, handled := runtime.handleWalletApprovalReply(owner, "同意！")
	if !handled || !strings.Contains(reply, "奶茶") || len(store.decisions) != 1 || !store.decisions[0] {
		t.Fatalf("quick approve reply=%q decisions=%v", reply, store.decisions)
	}
	// 额度外的那份不能被一句「同意」买下，只认码。
	if _, handled := runtime.handleWalletApprovalReply(owner, "同意"); handled {
		t.Fatal("over-quota proposal approved without code")
	}
	allowCode, _ := walletProposalCodes(big)
	if _, handled := runtime.handleWalletApprovalReply(owner, allowCode); !handled {
		t.Fatal("over-quota code not recognized")
	}
	if small.Status != WalletProposalPending || big.Status != WalletProposalPending {
		t.Fatal("test fixtures mutated unexpectedly")
	}
}

func TestWalletQuickApproveOffByDefault(t *testing.T) {
	store := &memoryWalletStore{}
	runtime := NewRuntime(BotConfig{OwnSpaceEnabled: boolPointer(true), OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetWalletStore(store)
	store.CreateWalletProposal(context.Background(), WalletProposalRequest{ProfileID: "bot-1", Item: "奶茶", PriceCents: 100, Now: runtime.clock()})
	owner := MessageEvent{Kind: EventKindPrivate, ProfileID: "bot-1", UserID: "10001"}
	if _, handled := runtime.handleWalletApprovalReply(owner, "同意"); handled {
		t.Fatal("quick approve active without quota")
	}
}
