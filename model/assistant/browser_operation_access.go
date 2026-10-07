// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

// BrowserOperationAccess grants private, isolated browsing to listed users.
// The owner retains their configured browser; member accounts never use it.
type BrowserOperationAccess struct {
	Mode         string   `json:"mode"`
	AllowedUsers []string `json:"allowed_users,omitempty"`
	AllowedHosts []string `json:"allowed_hosts,omitempty"`
}

func (access BrowserOperationAccess) WithDefaults() BrowserOperationAccess {
	normalized := (BrowserScreenshotAccess{Mode: access.Mode, AllowedUsers: access.AllowedUsers, AllowedHosts: access.AllowedHosts}).WithDefaults()
	return BrowserOperationAccess{Mode: normalized.Mode, AllowedUsers: normalized.AllowedUsers, AllowedHosts: normalized.AllowedHosts}
}

func (access BrowserOperationAccess) AllowsEvent(owner bool, event MessageEvent) bool {
	access = access.WithDefaults()
	if owner {
		return access.Mode != BrowserScreenshotDisabled
	}
	if event.Kind != EventKindPrivate || event.GroupID != "" {
		return false
	}
	return (BrowserScreenshotAccess{Mode: access.Mode, AllowedUsers: access.AllowedUsers, AllowedHosts: access.AllowedHosts}).Allows(false, event.UserID)
}
