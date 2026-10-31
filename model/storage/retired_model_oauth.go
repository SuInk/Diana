// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import "fmt"

// removeLLMOAuthState removes the retired model login credentials and bindings.
// Only the model OAuth document and its two configuration fields are affected;
// console sessions, trusted applications and API keys remain intact.
func (s *SQLiteStore) removeLLMOAuthState() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range []string{
		`DELETE FROM app_state WHERE key = 'llm_oauth'`,
		`UPDATE app_state AS state
SET value = json_set(value, '$.profiles', (
  SELECT json_group_array(json(CASE profile.type
    WHEN 'object' THEN json_remove(profile.value, '$.config.oauth_provider')
    WHEN 'array' THEN profile.value
    WHEN 'true' THEN 'true'
    WHEN 'false' THEN 'false'
    ELSE json_quote(profile.value)
  END))
  FROM json_each(state.value, '$.profiles') AS profile
))
WHERE key = 'llm_profiles' AND json_valid(value)
  AND json_type(value, '$.profiles') = 'array'
  AND EXISTS (
    SELECT 1 FROM json_each(state.value, '$.profiles') AS profile
    WHERE CASE WHEN profile.type = 'object'
      THEN json_type(profile.value, '$.config.oauth_provider') END IS NOT NULL
  )`,
		`UPDATE app_state AS state
SET value = json_set(value, '$.providers', (
  SELECT json_group_array(json(CASE provider.type
    WHEN 'object' THEN json_remove(provider.value, '$.oauthProvider')
    WHEN 'array' THEN provider.value
    WHEN 'true' THEN 'true'
    WHEN 'false' THEN 'false'
    ELSE json_quote(provider.value)
  END))
  FROM json_each(state.value, '$.providers') AS provider
))
WHERE key = 'llm_provider_registry' AND json_valid(value)
  AND json_type(value, '$.providers') = 'array'
  AND EXISTS (
    SELECT 1 FROM json_each(state.value, '$.providers') AS provider
    WHERE CASE WHEN provider.type = 'object'
      THEN json_type(provider.value, '$.oauthProvider') END IS NOT NULL
  )`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("remove retired model oauth state: %w", err)
		}
	}
	return tx.Commit()
}
