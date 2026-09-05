// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

const (
	defaultUserFavorability = 0
	// ownerUserFavorability 是主人的起始分，不是下限：主人一上来就是满信任，
	// 但之后照样按互动记录涨落。等级由身份决定（见 RelationshipPolicyFor），
	// 所以分数掉下来也不会把主人降级，只是如实反映最近处得怎么样。
	ownerUserFavorability  = 100
	minUserFavorability    = -100
	maxUserFavorability    = 200
	maxUserMemoryItems     = 20
	maxUserMemoryTextRunes = 180
)

// UpdateUserMemory updates one user's long-term profile without calling the LLM.
func (s *SQLiteStore) UpdateUserMemory(ctx context.Context, event assistant.MessageEvent, update assistant.UserMemoryUpdate) (assistant.UserMemoryProfile, error) {
	var profile assistant.UserMemoryProfile
	if s == nil || s.db == nil {
		return profile, nil
	}
	s.userMemoryMu.Lock()
	defer s.userMemoryMu.Unlock()
	userID := strings.TrimSpace(event.UserID)
	if userID == "" {
		return profile, nil
	}

	botProfileID := strings.TrimSpace(event.ProfileID)
	profile, ok, err := s.GetUserMemory(ctx, botProfileID, userID)
	if err != nil {
		return assistant.UserMemoryProfile{}, err
	}
	ownerID := strings.TrimSpace(update.OwnerID)
	if !ok {
		profile = assistant.UserMemoryProfile{
			UserID:       userID,
			Favorability: initialUserFavorability(ownerID, userID),
			Memories:     []assistant.UserMemoryItem{},
		}
	}
	previousFavorability := profile.Favorability
	profile.BotProfileID = botProfileID
	if name := strings.TrimSpace(event.SenderName); name != "" {
		profile.DisplayName = name
	}
	if update.SetFavorability != nil {
		profile.Favorability = clampUserFavorability(*update.SetFavorability)
	} else {
		profile.Favorability = clampUserFavorability(profile.Favorability + clampUserFavorabilityDelta(update.FavorabilityDelta))
	}
	if len(update.PortraitRemovals) > 0 || len(update.PortraitTraits) > 0 {
		for _, field := range update.PortraitRemovals {
			profile.Portrait, _ = assistant.RemovePortraitField(profile.Portrait, field)
		}
		profile.Portrait = assistant.MergePortraitTraits(profile.Portrait, update.PortraitTraits, time.Now())
	}
	if update.SetRomance != nil {
		if update.SetRomance.Active {
			state := *update.SetRomance
			if state.Since.IsZero() {
				state.Since = time.Now().UTC()
			}
			profile.Romance = &state
		} else {
			// 分手就清掉整条状态：关系结束了，不留一条「曾经在一起」的记录挂在
			// 档案上被反复注入。相处的事实仍在好感度和记忆里。
			profile.Romance = nil
		}
	}
	if !update.Administrative {
		profile.MessageCount++
		profile.LastSeenAt = userMemoryEventTime(event)
		if item, ok := userMemoryItemFromEvent(event, s.userMemoryNameResolver(ctx, botProfileID)); ok {
			profile.Memories = appendUserMemory(profile.Memories, item)
		}
	}
	profile.UpdatedAt = time.Now().UTC()

	memories, err := json.Marshal(profile.Memories)
	if err != nil {
		return assistant.UserMemoryProfile{}, err
	}
	portrait, err := marshalUserPortrait(profile.Portrait)
	if err != nil {
		return assistant.UserMemoryProfile{}, err
	}
	romance, err := marshalUserRomance(profile.Romance)
	if err != nil {
		return assistant.UserMemoryProfile{}, err
	}
	lastSeen := ""
	if !profile.LastSeenAt.IsZero() {
		lastSeen = profile.LastSeenAt.UTC().Format(time.RFC3339Nano)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return assistant.UserMemoryProfile{}, err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `
INSERT INTO user_profiles (bot_profile_id, user_id, display_name, favorability, message_count, memories, portrait, romance, last_seen_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(bot_profile_id, user_id) DO UPDATE SET
  display_name=excluded.display_name,
  favorability=excluded.favorability,
  message_count=excluded.message_count,
  memories=excluded.memories,
  portrait=excluded.portrait,
  romance=excluded.romance,
  last_seen_at=excluded.last_seen_at,
  updated_at=excluded.updated_at
`, botProfileID, profile.UserID, profile.DisplayName, profile.Favorability, profile.MessageCount, string(memories), portrait, romance, lastSeen, profile.UpdatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return assistant.UserMemoryProfile{}, err
	}
	if profile.Favorability != previousFavorability && favorabilityChangeRequested(update) {
		_, err = tx.ExecContext(ctx, `
INSERT INTO user_favorability_changes (
  bot_profile_id, user_id, delta, before_score, after_score, source, reason, operator_id, group_id, message_id, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, botProfileID, profile.UserID, profile.Favorability-previousFavorability, previousFavorability, profile.Favorability,
			favorabilityChangeSource(update), strings.TrimSpace(update.FavorabilityChangeReason),
			strings.TrimSpace(update.FavorabilityChangeOperator), strings.TrimSpace(event.GroupID),
			strings.TrimSpace(event.MessageID), profile.UpdatedAt.Format(time.RFC3339Nano))
		if err != nil {
			return assistant.UserMemoryProfile{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return assistant.UserMemoryProfile{}, err
	}
	return profile, nil
}

// ListUserFavorabilityChanges returns the newest real score changes first.
func (s *SQLiteStore) ListUserFavorabilityChanges(ctx context.Context, botProfileID, userID string, limit int) ([]assistant.UserFavorabilityChange, error) {
	return s.listUserFavorabilityChanges(ctx, botProfileID, userID, limit, false)
}

func (s *SQLiteStore) ListUserFavorabilityChangesExact(ctx context.Context, botProfileID, userID string, limit int) ([]assistant.UserFavorabilityChange, error) {
	return s.listUserFavorabilityChanges(ctx, botProfileID, userID, limit, true)
}

func (s *SQLiteStore) listUserFavorabilityChanges(ctx context.Context, botProfileID, userID string, limit int, exact bool) ([]assistant.UserFavorabilityChange, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	userID = strings.TrimSpace(userID)
	if userID == "" || limit <= 0 {
		return []assistant.UserFavorabilityChange{}, nil
	}
	if limit > 100 {
		limit = 100
	}
	scopeCondition, scopeArgs := favorabilityScopeCondition(botProfileID)
	if exact {
		scopeCondition, scopeArgs = " AND bot_profile_id = ?", []any{botProfileID}
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, user_id, delta, before_score, after_score, source, reason, operator_id, group_id, message_id, created_at
FROM user_favorability_changes
WHERE user_id = ?`+scopeCondition+`
ORDER BY id DESC
LIMIT ?
`, append(append([]any{userID}, scopeArgs...), limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	changes := make([]assistant.UserFavorabilityChange, 0, limit)
	for rows.Next() {
		var change assistant.UserFavorabilityChange
		var reason, operatorID, groupID, messageID sql.NullString
		var createdAt string
		if err := rows.Scan(&change.ID, &change.UserID, &change.Delta, &change.Before, &change.After, &change.Source,
			&reason, &operatorID, &groupID, &messageID, &createdAt); err != nil {
			return nil, err
		}
		change.Reason = reason.String
		change.OperatorID = operatorID.String
		change.GroupID = groupID.String
		change.MessageID = messageID.String
		change.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		changes = append(changes, change)
	}
	return changes, rows.Err()
}

func favorabilityChangeSource(update assistant.UserMemoryUpdate) string {
	if source := strings.TrimSpace(update.FavorabilityChangeSource); source != "" {
		return source
	}
	if update.SetFavorability != nil {
		return "manual"
	}
	return "interaction"
}

func favorabilityChangeRequested(update assistant.UserMemoryUpdate) bool {
	return update.SetFavorability != nil || update.FavorabilityDelta != 0
}

// userMemorySortColumns 是人员列表允许的排序键，值直接拼进 SQL，所以只认这张表
// 里的写法，控制台传别的一律回落到默认排序。
var userMemorySortColumns = map[string]string{
	"updated":      "updated_at",
	"last_seen":    "NULLIF(last_seen_at, '')",
	"favorability": "favorability",
	"messages":     "message_count",
}

// NormalizeUserMemorySort 把控制台传来的排序参数收敛到受支持的取值，非法值回落
// 到「最近更新 · 倒序」。
func NormalizeUserMemorySort(sort, order string) (string, string) {
	sort = strings.ToLower(strings.TrimSpace(sort))
	if _, ok := userMemorySortColumns[sort]; !ok {
		sort = "updated"
	}
	if strings.EqualFold(strings.TrimSpace(order), "asc") {
		return sort, "asc"
	}
	return sort, "desc"
}

// ListUserMemories returns long-term user profiles ordered by most recently
// updated. query filters by user ID or display name; the second return value
// is the total row count matching the same filter.
func (s *SQLiteStore) ListUserMemories(ctx context.Context, botProfileID, query string, limit int, offset int) ([]assistant.UserMemoryProfile, int, error) {
	return s.ListUserMemoriesSorted(ctx, botProfileID, query, "", "", limit, offset)
}

// ListUserMemoriesSorted 是带排序的列表查询：sort 取 NormalizeUserMemorySort 认
// 的键，order 取 asc/desc，两者留空等于「最近更新 · 倒序」。
func (s *SQLiteStore) ListUserMemoriesSorted(ctx context.Context, botProfileID, query, sort, order string, limit int, offset int) ([]assistant.UserMemoryProfile, int, error) {
	if s == nil || s.db == nil {
		return []assistant.UserMemoryProfile{}, 0, nil
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	where := ""
	args := []any{}
	query = strings.TrimSpace(query)
	if botProfileID = strings.TrimSpace(botProfileID); botProfileID != "" {
		where = " WHERE bot_profile_id = ?"
		args = append(args, botProfileID)
	}
	if query != "" {
		if where == "" {
			where = " WHERE ("
		} else {
			where += " AND ("
		}
		where += `user_id LIKE ? ESCAPE '\' OR display_name LIKE ? ESCAPE '\')`
		pattern := "%" + escapeUserMemoryLike(query) + "%"
		args = append(args, pattern, pattern)
	}
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM user_profiles"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT bot_profile_id, user_id, display_name, favorability, message_count, memories, portrait, romance, last_seen_at, updated_at
FROM user_profiles`+where+`
ORDER BY `+userMemoryOrderBy(sort, order)+`
LIMIT ? OFFSET ?
`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	profiles := make([]assistant.UserMemoryProfile, 0, limit)
	for rows.Next() {
		var profile assistant.UserMemoryProfile
		var displayName sql.NullString
		var memoriesRaw string
		var portraitRaw, romanceRaw sql.NullString
		var lastSeenRaw, updatedRaw sql.NullString
		if err := rows.Scan(&profile.BotProfileID, &profile.UserID, &displayName, &profile.Favorability, &profile.MessageCount, &memoriesRaw, &portraitRaw, &romanceRaw, &lastSeenRaw, &updatedRaw); err != nil {
			return nil, 0, err
		}
		profile.DisplayName = displayName.String
		if strings.TrimSpace(memoriesRaw) != "" {
			if err := json.Unmarshal([]byte(memoriesRaw), &profile.Memories); err != nil {
				return nil, 0, err
			}
		}
		if profile.Portrait, err = unmarshalUserPortrait(portraitRaw); err != nil {
			return nil, 0, err
		}
		if profile.Romance, err = unmarshalUserRomance(romanceRaw); err != nil {
			return nil, 0, err
		}
		profile.LastSeenAt = parseUserProfileTime(lastSeenRaw)
		profile.UpdatedAt = parseUserProfileTime(updatedRaw)
		profiles = append(profiles, profile)
	}
	return profiles, total, rows.Err()
}

// userMemoryOrderBy 拼出 ORDER BY 子句：没值的活跃时间永远排在最后，正序时也不
// 该顶到最前；末尾按 user_id 兜底，分数相同的人翻页时顺序才不会抖。
func userMemoryOrderBy(sort, order string) string {
	sort, order = NormalizeUserMemorySort(sort, order)
	column := userMemorySortColumns[sort]
	direction := "DESC"
	if order == "asc" {
		direction = "ASC"
	}
	return column + " IS NULL, " + column + " " + direction + ", user_id ASC"
}

func escapeUserMemoryLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

// GetUserMemory loads one user's long-term profile for one bot profile.
//
// botProfileID 留空表示「不限机器人」：控制台在「全部机器人」视图下查一个人时用
// 它，取最近更新的那一份，好过报「查不到」。
func (s *SQLiteStore) GetUserMemory(ctx context.Context, botProfileID, userID string) (assistant.UserMemoryProfile, bool, error) {
	return s.getUserMemory(ctx, botProfileID, userID, false)
}

func (s *SQLiteStore) GetUserMemoryExact(ctx context.Context, botProfileID, userID string) (assistant.UserMemoryProfile, bool, error) {
	return s.getUserMemory(ctx, botProfileID, userID, true)
}

func (s *SQLiteStore) getUserMemory(ctx context.Context, botProfileID, userID string, exact bool) (assistant.UserMemoryProfile, bool, error) {
	var profile assistant.UserMemoryProfile
	if s == nil || s.db == nil {
		return profile, false, nil
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return profile, false, nil
	}
	var displayName sql.NullString
	var memoriesRaw string
	var portraitRaw, romanceRaw sql.NullString
	var lastSeenRaw sql.NullString
	var updatedRaw sql.NullString
	scopeCondition, scopeArgs := userProfileScopeCondition(botProfileID)
	if exact {
		scopeCondition, scopeArgs = " AND bot_profile_id = ?", []any{botProfileID}
	}
	err := s.db.QueryRowContext(ctx, `
SELECT bot_profile_id, user_id, display_name, favorability, message_count, memories, portrait, romance, last_seen_at, updated_at
FROM user_profiles
WHERE user_id = ?`+scopeCondition+`
ORDER BY updated_at DESC
LIMIT 1
`, append([]any{userID}, scopeArgs...)...).Scan(&profile.BotProfileID, &profile.UserID, &displayName, &profile.Favorability, &profile.MessageCount, &memoriesRaw, &portraitRaw, &romanceRaw, &lastSeenRaw, &updatedRaw)
	if err == sql.ErrNoRows {
		return assistant.UserMemoryProfile{}, false, nil
	}
	if err != nil {
		return assistant.UserMemoryProfile{}, false, err
	}
	profile.DisplayName = displayName.String
	if strings.TrimSpace(memoriesRaw) != "" {
		if err := json.Unmarshal([]byte(memoriesRaw), &profile.Memories); err != nil {
			return assistant.UserMemoryProfile{}, false, err
		}
	}
	if profile.Portrait, err = unmarshalUserPortrait(portraitRaw); err != nil {
		return assistant.UserMemoryProfile{}, false, err
	}
	if profile.Romance, err = unmarshalUserRomance(romanceRaw); err != nil {
		return assistant.UserMemoryProfile{}, false, err
	}
	profile.LastSeenAt = parseUserProfileTime(lastSeenRaw)
	profile.UpdatedAt = parseUserProfileTime(updatedRaw)
	return profile, true, nil
}

// marshalUserPortrait 把画像写成 JSON。空画像存成空串而不是 "null"，让控制台和
// 老库里那些还没攒出画像的行长得一样。
func marshalUserPortrait(traits []assistant.UserPortraitTrait) (string, error) {
	if len(traits) == 0 {
		return "", nil
	}
	body, err := json.Marshal(traits)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func unmarshalUserPortrait(raw sql.NullString) ([]assistant.UserPortraitTrait, error) {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil, nil
	}
	var traits []assistant.UserPortraitTrait
	if err := json.Unmarshal([]byte(raw.String), &traits); err != nil {
		return nil, err
	}
	return traits, nil
}

// marshalUserRomance 把恋爱状态写成 JSON。没谈过恋爱存空串，和画像一个道理。
func marshalUserRomance(state *assistant.UserRomanceState) (string, error) {
	if state == nil || !state.Active {
		return "", nil
	}
	body, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func unmarshalUserRomance(raw sql.NullString) (*assistant.UserRomanceState, error) {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil, nil
	}
	var state assistant.UserRomanceState
	if err := json.Unmarshal([]byte(raw.String), &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func userMemoryEventTime(event assistant.MessageEvent) time.Time {
	if event.Time > 0 {
		return time.Unix(event.Time, 0).UTC()
	}
	return time.Now().UTC()
}

func userMemoryItemFromEvent(event assistant.MessageEvent, resolve assistant.AtMentionNameResolver) (assistant.UserMemoryItem, bool) {
	text := assistant.DisplayEventText(event, resolve)
	if !usefulUserMemoryText(text) {
		return assistant.UserMemoryItem{}, false
	}
	return assistant.UserMemoryItem{
		Text:      truncateUserMemoryText(text),
		Source:    string(event.Kind),
		GroupID:   event.GroupID,
		MessageID: event.MessageID,
		At:        userMemoryEventTime(event),
	}, true
}

// userMemoryNameResolver 从已有档案里查昵称，给「最近发言」里光秃秃的 @号码 补上
// 名字。只查本地 user_profiles：这是给人看的展示文本，为它去平台拉一次昵称不值当，
// 查不到就照旧显示号码。
//
// 一条消息里 at 通常只有一两个，但同一个号可能被 at 多次，所以带一层记忆化。返回的
// 闭包只在这一次写入里用，不跨消息缓存——昵称会改。
func (s *SQLiteStore) userMemoryNameResolver(ctx context.Context, botProfileID string) assistant.AtMentionNameResolver {
	if s == nil || s.db == nil {
		return nil
	}
	cache := map[string]string{}
	scopeCondition, scopeArgs := userProfileScopeCondition(botProfileID)
	return func(userID string) string {
		userID = strings.TrimSpace(userID)
		if userID == "" {
			return ""
		}
		if name, known := cache[userID]; known {
			return name
		}
		var displayName sql.NullString
		err := s.db.QueryRowContext(ctx, `
SELECT display_name
FROM user_profiles
WHERE user_id = ?`+scopeCondition+`
ORDER BY updated_at DESC
LIMIT 1
`, append([]any{userID}, scopeArgs...)...).Scan(&displayName)
		name := strings.TrimSpace(displayName.String)
		if err != nil {
			name = ""
		}
		// 没拿到昵称的档案会把 DisplayName 退化成账号本身，照这个渲染会写出
		// 「@10002（10002）」这种重复。当成查不到处理。
		if name == userID {
			name = ""
		}
		cache[userID] = name
		return name
	}
}

func usefulUserMemoryText(text string) bool {
	text = strings.TrimSpace(text)
	if len([]rune(text)) < 2 {
		return false
	}
	lower := strings.ToLower(text)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return strings.Contains(lower, " ")
	}
	return true
}

func appendUserMemory(memories []assistant.UserMemoryItem, item assistant.UserMemoryItem) []assistant.UserMemoryItem {
	for _, existing := range memories {
		if existing.Text == item.Text && existing.GroupID == item.GroupID {
			return memories
		}
	}
	memories = append(memories, item)
	if len(memories) > maxUserMemoryItems {
		memories = memories[len(memories)-maxUserMemoryItems:]
	}
	return memories
}

func clampUserFavorabilityDelta(delta int) int {
	if delta < -3 {
		return -3
	}
	if delta > 3 {
		return 3
	}
	return delta
}

// initialUserFavorability 给新建的档案定起始分。主人从满信任起步，其余人从零
// 开始；这只发生在建档那一次，之后主人和别人走同一套涨落规则。
func initialUserFavorability(ownerID, userID string) int {
	if ownerID != "" && ownerID == userID {
		return ownerUserFavorability
	}
	return defaultUserFavorability
}

// clampUserFavorability 把分数夹进可写区间。主人没有专属下限——他的等级由身份
// 决定，分数只是「最近处得怎么样」的如实记录，托底反而会把真实的疏远抹平。
func clampUserFavorability(value int) int {
	if value < minUserFavorability {
		return minUserFavorability
	}
	if value > maxUserFavorability {
		return maxUserFavorability
	}
	return value
}

func truncateUserMemoryText(text string) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= maxUserMemoryTextRunes {
		return string(runes)
	}
	return string(runes[:maxUserMemoryTextRunes]) + "..."
}

func parseUserProfileTime(value sql.NullString) time.Time {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

// userProfileScopeCondition 拼出机器人作用域条件。留空表示不限，交给调用方按
// updated_at 取最近的一份。
func userProfileScopeCondition(botProfileID string) (string, []any) {
	if botProfileID = strings.TrimSpace(botProfileID); botProfileID != "" {
		return " AND bot_profile_id = ?", []any{botProfileID}
	}
	return "", nil
}

// favorabilityScopeCondition 与 userProfileScopeCondition 同义，只是作用在
// 好感度变更表上。
func favorabilityScopeCondition(botProfileID string) (string, []any) {
	if botProfileID = strings.TrimSpace(botProfileID); botProfileID != "" {
		return " AND bot_profile_id = ?", []any{botProfileID}
	}
	return "", nil
}
