// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	maxExtensionAudienceEntries = 200
	maxExtensionAudienceIDChars = 64
)

// ExtensionAudience 限定一个已放开给群成员的扩展具体开放给谁。两个名单都是过滤器：
// 留空表示这一维不限制，两个都留空就是「所有群成员」。同时填写时要同时满足，
// 也就是「名单里的这些人，只在这些群里能用」。主人不受它影响。
type ExtensionAudience struct {
	// MinRole 为 "admin" 时只有群主和群管理员能用，空表示所有群成员。身份由调用方
	// 按平台核验后传进来；平台给不出身份就当普通成员，不放行。
	MinRole string   `json:"min_role,omitempty"`
	Users   []string `json:"users,omitempty"`
	Groups  []string `json:"groups,omitempty"`
}

// MemberRoleAdmin 是目前唯一的身份门槛：群主或群管理员。
const MemberRoleAdmin = "admin"

// 一个扩展对谁开放，统一用这四档表述。机器人给默认档，群配置可以按群覆盖。
const (
	ExtensionTierOff     = "off"
	ExtensionTierOwner   = "owner"
	ExtensionTierAdmins  = "admins"
	ExtensionTierMembers = "members"
)

// ExtensionTierRank 越大越宽松，用来判断某次改动是收紧还是放宽。
func ExtensionTierRank(tier string) int {
	switch tier {
	case ExtensionTierOff:
		return 0
	case ExtensionTierAdmins:
		return 2
	case ExtensionTierMembers:
		return 3
	default:
		return 1 // owner
	}
}

// NormalizeExtensionTier 收敛档位取值，空串表示「跟随机器人」由调用方处理。
func NormalizeExtensionTier(tier string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "":
		return "", nil
	case ExtensionTierOff:
		return ExtensionTierOff, nil
	case ExtensionTierOwner:
		return ExtensionTierOwner, nil
	case ExtensionTierAdmins:
		return ExtensionTierAdmins, nil
	case ExtensionTierMembers:
		return ExtensionTierMembers, nil
	}
	return "", fmt.Errorf("不支持的开放档位 %q", tier)
}

// BotExtensionTier 把「机器人级启用开关 + 成员开关 + 身份门槛」折算成一个档位。
func BotExtensionTier(overrides map[string]bool, audiences map[string]ExtensionAudience, id string) string {
	if enabled, ok := overrides[id]; ok && !enabled {
		return ExtensionTierOff
	}
	if !overrides[MemberOverrideKey(id)] {
		return ExtensionTierOwner
	}
	if audiences[id].RequiresGroupAdmin() {
		return ExtensionTierAdmins
	}
	return ExtensionTierMembers
}

func (a ExtensionAudience) Empty() bool {
	return a.MinRole == "" && len(a.Users) == 0 && len(a.Groups) == 0
}

// RequiresGroupAdmin 表示这一项只开放给群主和管理员。
func (a ExtensionAudience) RequiresGroupAdmin() bool { return a.MinRole == MemberRoleAdmin }

// AllowsRole 判断核验出来的身份是否达到门槛。role 取值由调用方归一化，
// 空串表示平台没给出身份。
func (a ExtensionAudience) AllowsRole(role string) bool {
	if !a.RequiresGroupAdmin() {
		return true
	}
	role = strings.ToLower(strings.TrimSpace(role))
	return role == "admin" || role == "owner"
}

// Allows 判断这条消息的发言人和来源群是否在名单内。
func (a ExtensionAudience) Allows(userID, groupID string) bool {
	if len(a.Users) > 0 && !containsTrimmed(a.Users, userID) {
		return false
	}
	// 限定了群就只在这些群里生效：私聊没有群号，落不进名单。
	if len(a.Groups) > 0 && !containsTrimmed(a.Groups, groupID) {
		return false
	}
	return true
}

func containsTrimmed(values []string, value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, item := range values {
		if strings.TrimSpace(item) == value {
			return true
		}
	}
	return false
}

func normalizeAudienceList(values []string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		if len([]rune(value)) > maxExtensionAudienceIDChars {
			return nil, errors.New("名单里的单个 ID 过长")
		}
		seen[value] = true
		out = append(out, value)
	}
	if len(out) > maxExtensionAudienceEntries {
		return nil, errors.New("名单条目过多")
	}
	sort.Strings(out)
	return out, nil
}

// NormalizeExtensionAudience 去掉空白与重复项并做长度限制。
func NormalizeExtensionAudience(audience ExtensionAudience) (ExtensionAudience, error) {
	users, err := normalizeAudienceList(audience.Users)
	if err != nil {
		return ExtensionAudience{}, err
	}
	groups, err := normalizeAudienceList(audience.Groups)
	if err != nil {
		return ExtensionAudience{}, err
	}
	role := strings.ToLower(strings.TrimSpace(audience.MinRole))
	if role != "" && role != MemberRoleAdmin {
		return ExtensionAudience{}, errors.New("身份门槛只支持 admin")
	}
	return ExtensionAudience{MinRole: role, Users: users, Groups: groups}, nil
}

func extensionAudiencePath(root string) string {
	return filepath.Join(root, ".extension-audience.json")
}

func loadExtensionAudienceFile(root string) (map[string]map[string]ExtensionAudience, error) {
	data, err := os.ReadFile(extensionAudiencePath(root))
	if os.IsNotExist(err) {
		return map[string]map[string]ExtensionAudience{}, nil
	}
	if err != nil {
		return nil, err
	}
	var values map[string]map[string]ExtensionAudience
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, err
	}
	if values == nil {
		values = map[string]map[string]ExtensionAudience{}
	}
	return values, nil
}

// LoadExtensionAudiences 读取某台机器人的对象名单，键是扩展 ID。
func LoadExtensionAudiences(root, profile string) (map[string]ExtensionAudience, error) {
	values, err := loadExtensionAudienceFile(root)
	if err != nil {
		return nil, err
	}
	return values[profile], nil
}

func saveExtensionAudience(root, profile, id string, audience ExtensionAudience) error {
	audience, err := NormalizeExtensionAudience(audience)
	if err != nil {
		return err
	}
	lock := extensionPathLock(extensionAudiencePath(root))
	lock.Lock()
	defer lock.Unlock()
	values, err := loadExtensionAudienceFile(root)
	if err != nil {
		return err
	}
	if values[profile] == nil {
		values[profile] = map[string]ExtensionAudience{}
	}
	if audience.Empty() {
		// 空名单就是不限制，不留下一条空记录。
		delete(values[profile], id)
		if len(values[profile]) == 0 {
			delete(values, profile)
		}
	} else {
		values[profile][id] = audience
	}
	data, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return err
	}
	return saveExtensionFile(extensionAudiencePath(root), data)
}

// MemberAllowedExtensionIDsFor 在成员开关的基础上再按对象名单过滤。身份门槛不在
// 这里判断：核验群身份可能要访问平台接口，调用方先看 AnyRequiresGroupAdmin，
// 需要时再查一次身份并交给 FilterByGroupRole。
func MemberAllowedExtensionIDsFor(values map[string]bool, audiences map[string]ExtensionAudience, userID, groupID string) []string {
	ids := MemberAllowedExtensionIDs(values)
	if len(audiences) == 0 {
		return ids
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		audience, ok := audiences[id]
		if ok && !audience.Allows(userID, groupID) {
			continue
		}
		out = append(out, id)
	}
	return out
}

// AnyRequiresGroupAdmin 回答「这批扩展里还有没有需要核验群身份的」。
func AnyRequiresGroupAdmin(audiences map[string]ExtensionAudience, ids []string) bool {
	for _, id := range ids {
		if audiences[id].RequiresGroupAdmin() {
			return true
		}
	}
	return false
}

// FilterByGroupRole 按核验到的群身份去掉够不着门槛的扩展。
func FilterByGroupRole(audiences map[string]ExtensionAudience, ids []string, role string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !audiences[id].AllowsRole(role) {
			continue
		}
		out = append(out, id)
	}
	return out
}
