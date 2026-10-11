package assistant

import (
	"strconv"
	"strings"
)

// resolveTelegramMentionUsernames resolves display handles only, never authenticated identities.
func resolveTelegramMentionUsernames(current MessageEvent, history []MessageEvent) map[string]string {
	if NormalizePlatformID(current.Platform) != PlatformTelegram {
		return nil
	}
	type observation struct {
		username string
		time     int64
		current  bool
	}
	direct := make(map[string]observation)
	indirect := make(map[string]observation)
	add := func(dst map[string]observation, id, username string, event MessageEvent, isCurrent bool) {
		id = strings.TrimSpace(id)
		if number, err := strconv.ParseInt(id, 10, 64); err != nil || number <= 0 {
			return
		}
		candidate := observation{username: normalizeTelegramMentionUsername(username), time: event.Time, current: isCurrent}
		previous, found := dst[id]
		if !found || candidate.time > previous.time || (candidate.time == previous.time && candidate.current && !previous.current) {
			dst[id] = candidate
		} else if candidate.time == previous.time && candidate.current == previous.current && !strings.EqualFold(candidate.username, previous.username) {
			// Equally recent, conflicting history has no reliable winner.
			previous.username = ""
			dst[id] = previous
		}
	}
	observe := func(event MessageEvent, isCurrent bool) {
		if !sameTelegramMentionScope(current, event) || event.Outbound {
			return
		}
		directEvent := event
		directEvent.Time = max(event.Time, event.EditTime)
		if event.SenderMentionUsername != nil {
			add(direct, event.UserID, *event.SenderMentionUsername, directEvent, isCurrent)
		} else if strings.TrimSpace(event.SenderUsername) != "" {
			add(direct, event.UserID, event.SenderUsername, directEvent, isCurrent)
		}
		if quoted := event.Quoted; quoted != nil && !quoted.Semantic && quoted.SenderMentionUsername != nil && (quoted.GroupID == "" || quoted.GroupID == current.GroupID) {
			add(indirect, quoted.UserID, *quoted.SenderMentionUsername, event, isCurrent)
		}
		for _, mention := range event.MentionTargets {
			if mention.UsernameKnown {
				add(indirect, mention.UserID, mention.Username, event, isCurrent)
			}
		}
	}
	for _, event := range history {
		observe(event, false)
	}
	observe(current, true)
	for id, candidate := range indirect {
		if _, found := direct[id]; !found {
			direct[id] = candidate
		}
	}

	owners := make(map[string]string)
	conflicts := make(map[string]bool)
	for id, candidate := range direct {
		if candidate.username == "" {
			continue
		}
		key := strings.ToLower(candidate.username)
		if owner, found := owners[key]; found && owner != id {
			conflicts[key] = true
		}
		owners[key] = id
	}
	resolved := make(map[string]string)
	for id, candidate := range direct {
		if candidate.username != "" && !conflicts[strings.ToLower(candidate.username)] {
			resolved[id] = candidate.username
		}
	}
	return resolved
}

func sameTelegramMentionScope(current, event MessageEvent) bool {
	return !event.crossGroupContext &&
		NormalizePlatformID(event.Platform) == PlatformTelegram &&
		sessionKey(event) == sessionKey(current) &&
		strings.TrimSpace(event.ProfileID) == strings.TrimSpace(current.ProfileID) &&
		strings.TrimSpace(event.SelfID) == strings.TrimSpace(current.SelfID) &&
		strings.TrimSpace(event.PlatformScope) == strings.TrimSpace(current.PlatformScope) &&
		strings.TrimSpace(event.GuildID) == strings.TrimSpace(current.GuildID)
}

func normalizeTelegramMentionUsername(username string) string {
	if username == "" || len(username) > 32 {
		return ""
	}
	for _, char := range username {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_') {
			return ""
		}
	}
	return username
}
