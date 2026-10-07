// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"

	"github.com/SuInk/diana/model/agent"
)

// BrowserScreenshotAccess 单独控制 browser_screenshot，不授予其他浏览器或文件工具。
type BrowserScreenshotAccess struct {
	Mode          string   `json:"mode"`
	AllowedUsers  []string `json:"allowed_users,omitempty"`
	AllowedHosts  []string `json:"allowed_hosts,omitempty"`
	AllowedGroups []string `json:"allowed_groups,omitempty"`
}

const (
	BrowserScreenshotDisabled  = "disabled"
	BrowserScreenshotOwnerOnly = "owner_only"
	BrowserScreenshotWhitelist = "whitelist"
)

func (access BrowserScreenshotAccess) WithDefaults() BrowserScreenshotAccess {
	access.Mode = strings.ToLower(strings.TrimSpace(access.Mode))
	switch access.Mode {
	case BrowserScreenshotDisabled, BrowserScreenshotWhitelist:
	default:
		// 旧配置及未知值沿用仅主人，不能因配置拼错而公开浏览器画面。
		access.Mode = BrowserScreenshotOwnerOnly
	}
	access.AllowedUsers = cleanStrings(access.AllowedUsers)
	access.AllowedHosts = agent.NormalizeBrowserScreenshotHosts(access.AllowedHosts)
	access.AllowedGroups = cleanStrings(access.AllowedGroups)
	return access
}

func (access BrowserScreenshotAccess) Allows(owner bool, userID string) bool {
	access = access.WithDefaults()
	if access.Mode == BrowserScreenshotDisabled {
		return false
	}
	if owner {
		return true
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false
	}
	if access.Mode == BrowserScreenshotWhitelist && len(access.AllowedHosts) > 0 {
		for _, allowed := range access.AllowedUsers {
			if userID == allowed {
				return true
			}
		}
	}
	return false
}

// Members can use their authorized sites in private; groups require a separate grant.
func (access BrowserScreenshotAccess) AllowsEvent(owner bool, event MessageEvent) bool {
	if !access.Allows(owner, event.UserID) {
		return false
	}
	if owner || (event.Kind != EventKindGroup && event.GroupID == "") {
		return true
	}
	if strings.TrimSpace(event.GroupID) == "" {
		return false
	}
	for _, group := range access.WithDefaults().AllowedGroups {
		if group == strings.TrimSpace(event.GroupID) {
			return true
		}
	}
	return false
}

// Logged member screenshots must always use that sender's isolated session.
// Legacy group grants never allow access to the shared owner browser.
func (access BrowserScreenshotAccess) AllowsBrowserEvent(operations BrowserOperationAccess, owner bool, event MessageEvent) bool {
	return access.AllowsEvent(owner, event) && (owner || operations.AllowsEvent(false, event))
}
