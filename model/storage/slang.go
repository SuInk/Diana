// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

// slangSchema 存黑话学习的候选词：记忆门控标出、还没查实的说法。按机器人和群
// 分开攒，同一个词在一个群里要多人说过几次才去查证。
const slangSchema = `
CREATE TABLE IF NOT EXISTS slang_candidates (
  profile_id TEXT NOT NULL,
  group_id TEXT NOT NULL,
  normalized_term TEXT NOT NULL,
  term TEXT NOT NULL,
  count INTEGER NOT NULL DEFAULT 0,
  speakers TEXT NOT NULL DEFAULT '[]',
  evidence TEXT NOT NULL DEFAULT '[]',
  status TEXT NOT NULL DEFAULT 'pending',
  next_threshold INTEGER NOT NULL DEFAULT 3,
  last_research_at INTEGER,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (profile_id, group_id, normalized_term)
);

CREATE INDEX IF NOT EXISTS idx_slang_candidates_research ON slang_candidates(profile_id, last_research_at);
`

// slangSpeakerLimit 只是为了「至少两个人说过」这个判断，记太多没有用。
const slangSpeakerLimit = 8

func (s *SQLiteStore) migrateSlang() error {
	if _, err := s.db.Exec(slangSchema); err != nil {
		return fmt.Errorf("create slang schema: %w", err)
	}
	return nil
}

// ObserveSlangCandidate 记一次出现：次数加一，发言人去重，原句只留最近几条。
func (s *SQLiteStore) ObserveSlangCandidate(ctx context.Context, observation assistant.SlangObservation) (assistant.SlangCandidate, error) {
	defer s.observeStorage(ctx, "ObserveSlangCandidate", "write")()
	if s == nil || s.db == nil {
		return assistant.SlangCandidate{}, errors.New("slang: store is not configured")
	}
	profile := strings.TrimSpace(observation.ProfileID)
	group := strings.TrimSpace(observation.GroupID)
	term := strings.TrimSpace(observation.Term)
	normalized := assistant.NormalizeNotebookTitle(term)
	if group == "" || normalized == "" {
		return assistant.SlangCandidate{}, errors.New("slang: group and term are required")
	}
	now := observation.Now
	if now.IsZero() {
		now = time.Now()
	}
	stamp := now.UTC().UnixNano()

	tx, err := s.beginWriteTx(ctx, "ObserveSlangCandidate")
	if err != nil {
		return assistant.SlangCandidate{}, err
	}
	defer observeTransaction("ObserveSlangCandidate")()
	defer func() { _ = tx.Rollback() }()

	candidate, found, err := slangCandidateByKey(ctx, tx, profile, group, normalized)
	if err != nil {
		return assistant.SlangCandidate{}, err
	}
	if !found {
		candidate = assistant.SlangCandidate{
			ProfileID: profile, GroupID: group, Term: term,
			Status: assistant.SlangCandidatePending, NextThreshold: 3,
		}
	}
	candidate.Count++
	if speaker := strings.TrimSpace(observation.SpeakerID); speaker != "" && !slices.Contains(candidate.Speakers, speaker) && len(candidate.Speakers) < slangSpeakerLimit {
		candidate.Speakers = append(candidate.Speakers, speaker)
	}
	if strings.TrimSpace(observation.Evidence.Text) != "" {
		candidate.Evidence = append(candidate.Evidence, observation.Evidence)
		if len(candidate.Evidence) > 3 {
			candidate.Evidence = candidate.Evidence[len(candidate.Evidence)-3:]
		}
	}
	speakers, err := json.Marshal(candidate.Speakers)
	if err != nil {
		return assistant.SlangCandidate{}, err
	}
	evidence, err := json.Marshal(candidate.Evidence)
	if err != nil {
		return assistant.SlangCandidate{}, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO slang_candidates (profile_id, group_id, normalized_term, term, count, speakers, evidence, status, next_threshold, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(profile_id, group_id, normalized_term) DO UPDATE SET
  count = excluded.count, speakers = excluded.speakers, evidence = excluded.evidence, updated_at = excluded.updated_at
`, profile, group, normalized, candidate.Term, candidate.Count, string(speakers), string(evidence),
		string(candidate.Status), candidate.NextThreshold, stamp, stamp); err != nil {
		return assistant.SlangCandidate{}, err
	}
	if err := tx.Commit(); err != nil {
		return assistant.SlangCandidate{}, err
	}
	return candidate, nil
}

// UpdateSlangCandidate 改状态和下次门槛；research 为真时记下这次查证时间。
func (s *SQLiteStore) UpdateSlangCandidate(ctx context.Context, profileID, groupID, term string, status assistant.SlangCandidateStatus, nextThreshold int, research bool, now time.Time) error {
	defer s.observeStorage(ctx, "UpdateSlangCandidate", "write")()
	if s == nil || s.db == nil {
		return errors.New("slang: store is not configured")
	}
	if now.IsZero() {
		now = time.Now()
	}
	stamp := now.UTC().UnixNano()
	query := `UPDATE slang_candidates SET status = ?, next_threshold = ?, updated_at = ? WHERE profile_id = ? AND group_id = ? AND normalized_term = ?`
	args := []any{string(status), nextThreshold, stamp, strings.TrimSpace(profileID), strings.TrimSpace(groupID), assistant.NormalizeNotebookTitle(term)}
	if research {
		query = `UPDATE slang_candidates SET status = ?, next_threshold = ?, updated_at = ?, last_research_at = ? WHERE profile_id = ? AND group_id = ? AND normalized_term = ?`
		args = []any{string(status), nextThreshold, stamp, stamp, strings.TrimSpace(profileID), strings.TrimSpace(groupID), assistant.NormalizeNotebookTitle(term)}
	}
	_, err := s.db.ExecContext(ctx, query, args...)
	return err
}

// CountSlangResearch 数某台机器人自 since 起查证过几个候选。
func (s *SQLiteStore) CountSlangResearch(ctx context.Context, profileID string, since time.Time) (int, error) {
	defer s.observeStorage(ctx, "CountSlangResearch", "read")()
	if s == nil || s.db == nil {
		return 0, errors.New("slang: store is not configured")
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM slang_candidates WHERE profile_id = ? AND last_research_at >= ?`,
		strings.TrimSpace(profileID), since.UTC().UnixNano()).Scan(&count)
	return count, err
}

func slangCandidateByKey(ctx context.Context, tx *sql.Tx, profile, group, normalized string) (assistant.SlangCandidate, bool, error) {
	var (
		candidate          assistant.SlangCandidate
		status             string
		speakers, evidence string
		lastResearch       sql.NullInt64
	)
	err := tx.QueryRowContext(ctx, `
SELECT term, count, speakers, evidence, status, next_threshold, last_research_at
FROM slang_candidates WHERE profile_id = ? AND group_id = ? AND normalized_term = ?
`, profile, group, normalized).Scan(&candidate.Term, &candidate.Count, &speakers, &evidence, &status, &candidate.NextThreshold, &lastResearch)
	if errors.Is(err, sql.ErrNoRows) {
		return assistant.SlangCandidate{}, false, nil
	}
	if err != nil {
		return assistant.SlangCandidate{}, false, err
	}
	candidate.ProfileID, candidate.GroupID = profile, group
	candidate.Status = assistant.SlangCandidateStatus(status)
	_ = json.Unmarshal([]byte(speakers), &candidate.Speakers)
	_ = json.Unmarshal([]byte(evidence), &candidate.Evidence)
	if lastResearch.Valid {
		candidate.LastResearchAt = time.Unix(0, lastResearch.Int64)
	}
	return candidate, true, nil
}
