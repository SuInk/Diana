package webui

import (
	"fmt"
	"strings"

	"github.com/SuInk/diana/model/assistant"
)

func (h *BotHandler) subscriptionTargets(values []repositoryWatchTargetPayload, fallback assistant.BotConfig) ([]assistant.ReminderDeliveryTarget, error) {
	if len(values) > 50 {
		return nil, fmt.Errorf("一条订阅最多配置 50 个通知目标")
	}
	targets := make([]assistant.ReminderDeliveryTarget, 0, len(values))
	for _, value := range values {
		profile := fallback
		if id := strings.TrimSpace(value.ProfileID); id != "" {
			var err error
			profile, err = h.repositoryWatchProfile(id)
			if err != nil {
				return nil, err
			}
		}
		if profile.ID == "" {
			return nil, fmt.Errorf("每个通知目标必须选择机器人")
		}
		if value.Destination != "group" && value.Destination != "private" {
			return nil, fmt.Errorf("通知目标类型必须是 group 或 private")
		}
		if (value.Destination == "group" && strings.TrimSpace(value.GroupID) == "") || (value.Destination == "private" && strings.TrimSpace(value.UserID) == "") {
			return nil, fmt.Errorf("通知目标必须填写群聊或私聊 ID")
		}
		targets = append(targets, repositoryWatchTargetsFromPayload([]repositoryWatchTargetPayload{value}, profile)...)
	}
	return targets, nil
}
