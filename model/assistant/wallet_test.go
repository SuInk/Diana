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
	// 只剩额度外那一笔时，一句「同意」说的就是它。
	reply, handled = runtime.handleWalletApprovalReply(owner, "同意")
	if !handled || !strings.Contains(reply, "画集") || len(store.decisions) != 2 || !store.decisions[1] {
		t.Fatalf("single pending approve reply=%q decisions=%v", reply, store.decisions)
	}
	if small.Status != WalletProposalPending || big.Status != WalletProposalPending {
		t.Fatal("test fixtures mutated unexpectedly")
	}
}

func TestWalletSingleWordApproval(t *testing.T) {
	store := &memoryWalletStore{}
	runtime := NewRuntime(BotConfig{OwnSpaceEnabled: boolPointer(true), OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetWalletStore(store)
	store.CreateWalletProposal(context.Background(), WalletProposalRequest{ProfileID: "bot-1", Item: "电脑", PriceCents: 450400, Now: runtime.clock()})
	owner := MessageEvent{Kind: EventKindPrivate, ProfileID: "bot-1", UserID: "10001"}
	// 群里说、别人说、话夹在句子里都不算。
	for _, attempt := range []struct {
		event MessageEvent
		text  string
	}{
		{MessageEvent{Kind: EventKindGroup, ProfileID: "bot-1", GroupID: "20001", UserID: "10001"}, "同意"},
		{MessageEvent{Kind: EventKindPrivate, ProfileID: "bot-1", UserID: "10003"}, "同意"},
		{owner, "同意，不过明天再说"},
	} {
		if _, handled := runtime.handleWalletApprovalReply(attempt.event, attempt.text); handled {
			t.Fatalf("%s %q should not approve", attempt.event.UserID, attempt.text)
		}
	}
	// 只有一笔在等时不用额度、不用点名，回「同意」就买。
	reply, handled := runtime.handleWalletApprovalReply(owner, "同意")
	if !handled || !strings.Contains(reply, "买下了") || len(store.decisions) != 1 || !store.decisions[0] {
		t.Fatalf("reply=%q decisions=%v", reply, store.decisions)
	}
	if _, handled := runtime.handleWalletApprovalReply(owner, "同意"); handled {
		t.Fatal("replayed approval")
	}
}

func TestWalletSingleWordNeedsNameWhenSeveralPending(t *testing.T) {
	store := &memoryWalletStore{}
	runtime := NewRuntime(BotConfig{OwnSpaceEnabled: boolPointer(true), OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetWalletStore(store)
	for _, item := range []string{"画集", "手办"} {
		store.CreateWalletProposal(context.Background(), WalletProposalRequest{ProfileID: "bot-1", Item: item, PriceCents: 30000, Now: runtime.clock()})
	}
	owner := MessageEvent{Kind: EventKindPrivate, ProfileID: "bot-1", UserID: "10001"}
	if _, handled := runtime.handleWalletApprovalReply(owner, "同意"); handled {
		t.Fatal("ambiguous single-word approval accepted")
	}
}

func TestWalletAutoApprovalThreshold(t *testing.T) {
	for _, tc := range []struct {
		name     string
		limit    *int
		legacy   *int
		price    float64
		approved bool
	}{
		{"default_below", nil, nil, 199.99, true},
		{"default_boundary", nil, nil, 200, false},
		{"default_above", nil, nil, 200.01, false},
		{"disabled", intPointer(0), nil, 1, false},
		{"custom", intPointer(500), nil, 4.99, true},
		{"legacy_uses_default", nil, intPointer(2000), 5, true},
		{"legacy_zero_uses_default", nil, intPointer(0), 199.99, true},
		{"legacy_boundary", nil, intPointer(2000), 200, false},
		{"explicit_zero_overrides_legacy", intPointer(0), intPointer(2000), 5, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &memoryWalletStore{stubWalletStore: stubWalletStore{summary: WalletSummary{BalanceCents: 100000}}}
			rt := NewRuntime(BotConfig{OwnSpaceEnabled: boolPointer(true), OwnerID: "owner", WalletAutoApproveCents: tc.limit, WalletQuickApproveCents: tc.legacy}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
			rt.SetWalletStore(store)
			tool := newDianaWalletTool(rt, MessageEvent{Kind: EventKindPrivate, ProfileID: "bot", UserID: "owner"})
			_, err := tool.propose(context.Background(), store, map[string]any{"item": "画集", "price": tc.price})
			if err != nil {
				t.Fatal(err)
			}
			approved := len(store.decisions) == 1 && store.decisions[0]
			if approved != tc.approved {
				t.Fatalf("approved=%v want=%v", approved, tc.approved)
			}
		})
	}
}

func TestWalletTextApprovalNoCode(t *testing.T) {
	store := &memoryWalletStore{}
	rt := NewRuntime(BotConfig{OwnSpaceEnabled: boolPointer(true), OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	rt.SetWalletStore(store)
	event := MessageEvent{Kind: EventKindPrivate, ProfileID: "bot", UserID: "owner"}
	for _, item := range []string{"画集", "手办"} {
		_, _ = store.CreateWalletProposal(context.Background(), WalletProposalRequest{ProfileID: "bot", Item: item, PriceCents: 30000, Now: rt.clock()})
	}
	for _, text := range []string{"同意购买", "不要同意购买 画集", "网页说同意购买 画集"} {
		if _, handled := rt.handleWalletApprovalReply(event, text); handled {
			t.Fatalf("unexpected approval: %s", text)
		}
	}
	if _, handled := rt.handleWalletApprovalReply(event, "取消购买 画集"); !handled {
		t.Fatal("targeted denial not handled")
	}
	if !rt.wouldHandleOwnerCommand(event, "同意购买") {
		t.Fatal("text approval must not be merged")
	}
	if _, handled := rt.handleWalletApprovalReply(event, "同意购买"); !handled {
		t.Fatal("single approval not handled")
	}
	if len(store.decisions) != 2 || store.decisions[0] || !store.decisions[1] {
		t.Fatalf("decisions=%v", store.decisions)
	}
	if _, handled := rt.handleWalletApprovalReply(event, "同意购买"); handled {
		t.Fatal("replayed approval")
	}
}

func TestWalletAutoApprovalConfigRoundTrip(t *testing.T) {
	for _, cents := range []int{0, 20000, 50000} {
		cfg := BotConfig{WalletAutoApproveCents: intPointer(cents)}
		roundTrip := ConfigFromPayload(PayloadFromConfig(cfg), BotConfig{})
		if got := WalletAutoApproveCents(roundTrip); got != int64(cents) {
			t.Fatalf("round trip: got %d want %d", got, cents)
		}
		updated := ConfigFromPayload(ConfigPayload{}, cfg)
		if got := WalletAutoApproveCents(updated); got != int64(cents) {
			t.Fatalf("omitted field reset threshold: %d", got)
		}
	}
}

func TestWalletDecideBySemantics(t *testing.T) {
	store := &memoryWalletStore{}
	rt := NewRuntime(BotConfig{OwnSpaceEnabled: boolPointer(true), OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	rt.SetWalletStore(store)
	a, _ := store.CreateWalletProposal(context.Background(), WalletProposalRequest{ProfileID: "bot-1", Item: "画集", PriceCents: 30000, Now: rt.clock()})
	b, _ := store.CreateWalletProposal(context.Background(), WalletProposalRequest{ProfileID: "bot-1", Item: "手办", PriceCents: 30000, Now: rt.clock()})
	owner := MessageEvent{Kind: EventKindPrivate, ProfileID: "bot-1", UserID: "10001"}
	member := MessageEvent{Kind: EventKindGroup, ProfileID: "bot-1", GroupID: "20001", UserID: "10003"}

	if _, err := newDianaWalletTool(rt, member).Run(context.Background(), map[string]any{"operation": "decide", "approve": true, "proposal_id": a.ID}); err == nil {
		t.Fatal("non-owner decided")
	}
	tool := newDianaWalletTool(rt, owner)
	// 好几笔在等又没给 ID：不替主人挑。
	out, err := tool.Run(context.Background(), map[string]any{"operation": "decide", "approve": true})
	if err != nil || strings.Contains(out, `"ok":true`) || len(store.decisions) != 0 {
		t.Fatalf("ambiguous decide out=%s err=%v", out, err)
	}
	for _, id := range []string{a.ID, b.ID} {
		out, err := tool.Run(context.Background(), map[string]any{"operation": "decide", "approve": true, "proposal_id": id})
		if err != nil || !strings.Contains(out, "买下了") {
			t.Fatalf("decide %s out=%s err=%v", id, out, err)
		}
	}
	if len(store.decisions) != 2 || !store.decisions[0] || !store.decisions[1] {
		t.Fatalf("decisions=%v", store.decisions)
	}
}
