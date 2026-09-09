package assistant

import "strings"

func explicitlyRepliesToBot(event MessageEvent, cfg BotConfig) bool {
	if event.Quoted == nil || event.Quoted.Semantic || strings.TrimSpace(event.Quoted.MessageID) == "" {
		return false
	}
	if group := strings.TrimSpace(event.Quoted.GroupID); group != "" && group != strings.TrimSpace(event.GroupID) {
		return false
	}
	botID := strings.TrimSpace(firstNonEmpty(event.SelfID, cfg.BotAccount))
	return botID != "" && strings.TrimSpace(event.Quoted.UserID) == botID
}
