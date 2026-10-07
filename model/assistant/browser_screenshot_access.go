// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"errors"
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

// 截图和操作权限共用同一组模式。
const (
	BrowserAccessDisabled  = "disabled"
	BrowserAccessOwnerOnly = "owner_only"
	BrowserAccessWhitelist = "whitelist"

	BrowserScreenshotDisabled  = BrowserAccessDisabled
	BrowserScreenshotOwnerOnly = BrowserAccessOwnerOnly
	BrowserScreenshotWhitelist = BrowserAccessWhitelist
)

// validateBrowserAccess 拒绝未知模式和非精确域名；WithDefaults 会把它们静默改掉，
// 所以界面提交时要先在这里报错。label 区分「截图」「操作」两套设置的报错文案。
func validateBrowserAccess(label, mode string, hosts []string) error {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", BrowserAccessDisabled, BrowserAccessOwnerOnly, BrowserAccessWhitelist:
	default:
		return errors.New("不支持的" + label + "权限模式")
	}
	for _, host := range hosts {
		if !agent.ValidBrowserScreenshotHost(host) {
			return errors.New(label + "网站必须是精确的域名或域名:端口，不支持协议、路径或通配符")
		}
	}
	return nil
}

func (access BrowserScreenshotAccess) Validate() error {
	return validateBrowserAccess("截图", access.Mode, access.AllowedHosts)
}

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
