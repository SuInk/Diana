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

// 零花钱账本的持久化。只追加不修改：
//
//   - 每笔一行，带上记账后的余额。余额就是最新一行的 balance_after，不另存一份
//     「当前余额」——两份数一旦对不上，没人说得清该信哪个。
//   - 读余额和插入在同一个写事务里，两个会话同时扣款不会双双通过余额检查。
//
// 时间存 UnixNano，同一秒内连记两笔时排序才稳定。
const walletSchema = `
CREATE TABLE IF NOT EXISTS wallet_ledger (
  id TEXT PRIMARY KEY,
  profile_id TEXT NOT NULL DEFAULT '',
  seq INTEGER NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('allowance', 'adjust', 'spend', 'refund')),
  amount_cents INTEGER NOT NULL,
  balance_after_cents INTEGER NOT NULL CHECK (balance_after_cents >= 0),
  reason TEXT NOT NULL DEFAULT '',
  actor_user_id TEXT NOT NULL DEFAULT '',
  actor_name TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  UNIQUE (profile_id, seq)
);
`

const walletColumns = `id, profile_id, kind, amount_cents, balance_after_cents, reason,
	actor_user_id, actor_name, created_at`

func (s *SQLiteStore) migrateWallet() error {
	if _, err := s.db.Exec(walletSchema); err != nil {
		return fmt.Errorf("create wallet schema: %w", err)
	}
	return nil
}

// RecordWalletEntry 记一笔账，返回记下的那一行。扣到负数时返回
// assistant.ErrWalletInsufficient，账本不动。
func (s *SQLiteStore) RecordWalletEntry(ctx context.Context, request assistant.WalletWriteRequest) (assistant.WalletEntry, error) {
	defer s.observeStorage(ctx, "RecordWalletEntry", "write")()
	request.ProfileID = strings.TrimSpace(request.ProfileID)
	request.Reason = assistant.NormalizeWalletReason(request.Reason)
	if !assistant.ValidWalletEntryKind(request.Kind) {
		return assistant.WalletEntry{}, fmt.Errorf("unknown wallet entry kind %q", request.Kind)
	}
	if request.AmountCents == 0 || request.AmountCents > assistant.MaximumWalletAmountCents ||
		request.AmountCents < -assistant.MaximumWalletAmountCents {
		return assistant.WalletEntry{}, assistant.ErrWalletAmount
	}
	if request.Now.IsZero() {
		request.Now = time.Now()
	}
	tx, err := s.beginWriteTx(ctx, "RecordWalletEntry")
	if err != nil {
		return assistant.WalletEntry{}, err
	}
	defer observeTransaction("RecordWalletEntry")()
	defer func() { _ = tx.Rollback() }()

	var seq, balance int64
	err = tx.QueryRowContext(ctx, `
SELECT seq, balance_after_cents FROM wallet_ledger WHERE profile_id = ? ORDER BY seq DESC LIMIT 1
`, request.ProfileID).Scan(&seq, &balance)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return assistant.WalletEntry{}, err
	}
	next := balance + request.AmountCents
	if next < 0 {
		return assistant.WalletEntry{}, assistant.ErrWalletInsufficient
	}
	entry := assistant.WalletEntry{
		ID:           uuid.NewString(),
		ProfileID:    request.ProfileID,
		Kind:         request.Kind,
		AmountCents:  request.AmountCents,
		BalanceAfter: next,
		Reason:       request.Reason,
		ActorUserID:  strings.TrimSpace(request.ActorUserID),
		ActorName:    strings.TrimSpace(request.ActorName),
		CreatedAt:    request.Now,
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO wallet_ledger (
  id, profile_id, seq, kind, amount_cents, balance_after_cents, reason, actor_user_id, actor_name, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, entry.ID, entry.ProfileID, seq+1, string(entry.Kind), entry.AmountCents, entry.BalanceAfter,
		entry.Reason, entry.ActorUserID, entry.ActorName, request.Now.UnixNano()); err != nil {
		return assistant.WalletEntry{}, err
	}
	if err := tx.Commit(); err != nil {
		return assistant.WalletEntry{}, err
	}
	return entry, nil
}

// WalletSummary 返回当前余额和最近 limit 笔账，新的在前。
func (s *SQLiteStore) WalletSummary(ctx context.Context, profileID string, limit int) (assistant.WalletSummary, error) {
	defer s.observeStorage(ctx, "WalletSummary", "read")()
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.eventReader().QueryContext(ctx, `SELECT `+walletColumns+`
FROM wallet_ledger WHERE profile_id = ? ORDER BY seq DESC LIMIT ?`, strings.TrimSpace(profileID), limit)
	if err != nil {
		return assistant.WalletSummary{}, err
	}
	defer func() { _ = rows.Close() }()
	summary := assistant.WalletSummary{Recent: make([]assistant.WalletEntry, 0, limit)}
	for rows.Next() {
		var entry assistant.WalletEntry
		var kind string
		var createdNS int64
		if err := rows.Scan(&entry.ID, &entry.ProfileID, &kind, &entry.AmountCents, &entry.BalanceAfter,
			&entry.Reason, &entry.ActorUserID, &entry.ActorName, &createdNS); err != nil {
			return assistant.WalletSummary{}, err
		}
		entry.Kind = assistant.WalletEntryKind(kind)
		entry.CreatedAt = time.Unix(0, createdNS)
		summary.Recent = append(summary.Recent, entry)
	}
	if err := rows.Err(); err != nil {
		return assistant.WalletSummary{}, err
	}
	if len(summary.Recent) > 0 {
		summary.BalanceCents = summary.Recent[0].BalanceAfter
	}
	return summary, nil
}
