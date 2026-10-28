// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"

	"github.com/google/uuid"
)

// 购买提议。状态只从 pending 走到 approved/rejected 一次；过期不落库，读的时候按
// expires_at 判断。批准和扣款在同一个写事务里：扣款失败（余额不够）时提议保持待审批。
const walletProposalSchema = `
CREATE TABLE IF NOT EXISTS wallet_proposals (
  id TEXT PRIMARY KEY,
  profile_id TEXT NOT NULL DEFAULT '',
  item TEXT NOT NULL,
  price_cents INTEGER NOT NULL CHECK (price_cents > 0),
  reason TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'rejected')),
  source_group_id TEXT NOT NULL DEFAULT '',
  source_user_id TEXT NOT NULL DEFAULT '',
  source_user_name TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  decided_at INTEGER NOT NULL DEFAULT 0,
  spend_entry_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_wallet_proposals_profile ON wallet_proposals (profile_id, created_at DESC);
`

const walletProposalColumns = `id, profile_id, item, price_cents, reason, status, source_group_id,
	source_user_id, source_user_name, created_at, expires_at, decided_at, spend_entry_id`

func (s *SQLiteStore) migrateWalletProposals() error {
	if _, err := s.db.Exec(walletProposalSchema); err != nil {
		return fmt.Errorf("create wallet proposal schema: %w", err)
	}
	return nil
}

// CreateWalletProposal 新建一份待审批提议。
func (s *SQLiteStore) CreateWalletProposal(ctx context.Context, request assistant.WalletProposalRequest) (assistant.WalletProposal, error) {
	defer s.observeStorage(ctx, "CreateWalletProposal", "write")()
	request.ProfileID = strings.TrimSpace(request.ProfileID)
	request.Item = assistant.NormalizeWalletItem(request.Item)
	if request.Item == "" {
		return assistant.WalletProposal{}, fmt.Errorf("要买的东西不能为空")
	}
	if request.PriceCents <= 0 || request.PriceCents > assistant.MaximumWalletAmountCents {
		return assistant.WalletProposal{}, assistant.ErrWalletAmount
	}
	if request.Now.IsZero() {
		request.Now = time.Now()
	}
	tx, err := s.beginWriteTx(ctx, "CreateWalletProposal")
	if err != nil {
		return assistant.WalletProposal{}, err
	}
	defer observeTransaction("CreateWalletProposal")()
	defer func() { _ = tx.Rollback() }()
	var pending int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM wallet_proposals WHERE profile_id = ? AND status = 'pending' AND expires_at > ?
`, request.ProfileID, request.Now.UnixNano()).Scan(&pending); err != nil {
		return assistant.WalletProposal{}, err
	}
	if pending >= assistant.MaximumPendingWalletProposals {
		return assistant.WalletProposal{}, assistant.ErrWalletProposalLimit
	}
	proposal := assistant.WalletProposal{
		ID:             uuid.NewString(),
		ProfileID:      request.ProfileID,
		Item:           request.Item,
		PriceCents:     request.PriceCents,
		Reason:         assistant.NormalizeWalletReason(request.Reason),
		Status:         assistant.WalletProposalPending,
		SourceGroupID:  strings.TrimSpace(request.SourceGroupID),
		SourceUserID:   strings.TrimSpace(request.SourceUserID),
		SourceUserName: strings.TrimSpace(request.SourceUserName),
		CreatedAt:      request.Now,
		ExpiresAt:      request.Now.Add(assistant.WalletProposalTTL),
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO wallet_proposals (
  id, profile_id, item, price_cents, reason, status, source_group_id, source_user_id, source_user_name, created_at, expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, proposal.ID, proposal.ProfileID, proposal.Item, proposal.PriceCents, proposal.Reason, string(proposal.Status),
		proposal.SourceGroupID, proposal.SourceUserID, proposal.SourceUserName,
		proposal.CreatedAt.UnixNano(), proposal.ExpiresAt.UnixNano()); err != nil {
		return assistant.WalletProposal{}, err
	}
	if err := tx.Commit(); err != nil {
		return assistant.WalletProposal{}, err
	}
	return proposal, nil
}

// ListWalletProposals 列出最近的提议，新的在前。
func (s *SQLiteStore) ListWalletProposals(ctx context.Context, profileID string, pendingOnly bool, now time.Time, limit int) ([]assistant.WalletProposal, error) {
	defer s.observeStorage(ctx, "ListWalletProposals", "read")()
	if limit <= 0 {
		limit = 20
	}
	query := `SELECT ` + walletProposalColumns + ` FROM wallet_proposals WHERE profile_id = ?`
	args := []any{strings.TrimSpace(profileID)}
	if pendingOnly {
		query += ` AND status = 'pending' AND expires_at > ?`
		args = append(args, now.UnixNano())
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.eventReader().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	proposals := make([]assistant.WalletProposal, 0)
	for rows.Next() {
		proposal, err := scanWalletProposal(rows)
		if err != nil {
			return nil, err
		}
		proposals = append(proposals, proposal)
	}
	return proposals, rows.Err()
}

// DecideWalletProposal 审批一份提议，批准时同一事务里记一笔 spend。
func (s *SQLiteStore) DecideWalletProposal(ctx context.Context, profileID, id string, approve bool, actorUserID, actorName string, now time.Time) (assistant.WalletProposal, error) {
	defer s.observeStorage(ctx, "DecideWalletProposal", "write")()
	if now.IsZero() {
		now = time.Now()
	}
	tx, err := s.beginWriteTx(ctx, "DecideWalletProposal")
	if err != nil {
		return assistant.WalletProposal{}, err
	}
	defer observeTransaction("DecideWalletProposal")()
	defer func() { _ = tx.Rollback() }()
	proposal, err := scanWalletProposal(tx.QueryRowContext(ctx, `SELECT `+walletProposalColumns+`
FROM wallet_proposals WHERE profile_id = ? AND id = ?`, strings.TrimSpace(profileID), strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return assistant.WalletProposal{}, assistant.ErrWalletProposalClosed
	}
	if err != nil {
		return assistant.WalletProposal{}, err
	}
	if proposal.EffectiveStatus(now) != assistant.WalletProposalPending {
		return assistant.WalletProposal{}, assistant.ErrWalletProposalClosed
	}
	proposal.Status = assistant.WalletProposalRejected
	if approve {
		entry, err := appendWalletEntryTx(ctx, tx, assistant.WalletWriteRequest{
			ProfileID:   proposal.ProfileID,
			Kind:        assistant.WalletEntrySpend,
			AmountCents: -proposal.PriceCents,
			Reason:      assistant.NormalizeWalletReason("买了" + proposal.Item),
			ActorUserID: strings.TrimSpace(actorUserID),
			ActorName:   strings.TrimSpace(actorName),
			Now:         now,
		})
		if err != nil {
			return assistant.WalletProposal{}, err
		}
		proposal.Status = assistant.WalletProposalApproved
		proposal.SpendEntryID = entry.ID
	}
	proposal.DecidedAt = now
	if _, err := tx.ExecContext(ctx, `
UPDATE wallet_proposals SET status = ?, decided_at = ?, spend_entry_id = ? WHERE id = ? AND status = 'pending'
`, string(proposal.Status), now.UnixNano(), proposal.SpendEntryID, proposal.ID); err != nil {
		return assistant.WalletProposal{}, err
	}
	if err := tx.Commit(); err != nil {
		return assistant.WalletProposal{}, err
	}
	return proposal, nil
}

type walletProposalScanner interface {
	Scan(dest ...any) error
}

func scanWalletProposal(row walletProposalScanner) (assistant.WalletProposal, error) {
	var proposal assistant.WalletProposal
	var status string
	var createdNS, expiresNS, decidedNS int64
	if err := row.Scan(&proposal.ID, &proposal.ProfileID, &proposal.Item, &proposal.PriceCents, &proposal.Reason,
		&status, &proposal.SourceGroupID, &proposal.SourceUserID, &proposal.SourceUserName,
		&createdNS, &expiresNS, &decidedNS, &proposal.SpendEntryID); err != nil {
		return assistant.WalletProposal{}, err
	}
	proposal.Status = assistant.WalletProposalStatus(status)
	proposal.CreatedAt = time.Unix(0, createdNS)
	proposal.ExpiresAt = time.Unix(0, expiresNS)
	if decidedNS > 0 {
		proposal.DecidedAt = time.Unix(0, decidedNS)
	}
	return proposal, nil
}
