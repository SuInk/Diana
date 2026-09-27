// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
)

// Telegram 的全员禁言是改群默认权限，解除时必须还原成禁言前的样子：写死一套「全开」
// 会把群里原本关掉的链接、媒体、投票、邀请统统放开。所以 mute_all 前先读当前权限
// 存一份快照，unmute_all 按快照还原。
//
// 快照先放内存；这个群有群配置时再写进群配置，重启后照样能还原。没有群配置的群不
// 为此新建一份——新建会改变「新群跟随机器人默认」的判定，代价比丢一份快照大。

const wholeMuteDefaultRestoreNote = "没有找到全员禁言前的权限记录，已按默认成员权限恢复（发言、媒体、投票、链接预览、邀请放开，改群资料、置顶、话题关闭）；如果和原来的设置不同，请在群设置里调整"

type wholeMuteSnapshotStore struct {
	mu      sync.Mutex
	entries map[string]map[string]bool
}

func wholeMuteSnapshotKey(profileID, groupID string) string {
	return strings.TrimSpace(profileID) + "|" + strings.TrimSpace(groupID)
}

// telegramChatPermissions 读群当前的默认成员权限。
func (r *Runtime) telegramChatPermissions(ctx context.Context, event MessageEvent, groupID string) (map[string]bool, error) {
	data, err := r.callPlatformAPIForEvent(ctx, event, "getChat", map[string]any{"chat_id": groupID})
	if err != nil {
		return nil, err
	}
	raw, ok := data["permissions"].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil, fmt.Errorf("getChat 没有返回群权限")
	}
	perms := make(map[string]bool, len(raw))
	for key, value := range raw {
		if flag, ok := value.(bool); ok {
			perms[key] = flag
		}
	}
	return perms, nil
}

// rememberWholeMuteSnapshot 记下全员禁言前的权限。已经有快照时不覆盖：连按两次
// mute_all，第二次读到的是已禁言的权限，覆盖掉就再也还原不回去了。
func (r *Runtime) rememberWholeMuteSnapshot(event MessageEvent, groupID string, perms map[string]bool) {
	if _, ok := r.wholeMuteSnapshot(event, groupID); ok {
		return
	}
	key := wholeMuteSnapshotKey(event.ProfileID, groupID)
	r.wholeMuteSnapshots.mu.Lock()
	if r.wholeMuteSnapshots.entries == nil {
		r.wholeMuteSnapshots.entries = map[string]map[string]bool{}
	}
	r.wholeMuteSnapshots.entries[key] = perms
	r.wholeMuteSnapshots.mu.Unlock()
	r.persistWholeMuteSnapshot(event, groupID, perms)
}

func (r *Runtime) wholeMuteSnapshot(event MessageEvent, groupID string) (map[string]bool, bool) {
	key := wholeMuteSnapshotKey(event.ProfileID, groupID)
	r.wholeMuteSnapshots.mu.Lock()
	perms, ok := r.wholeMuteSnapshots.entries[key]
	r.wholeMuteSnapshots.mu.Unlock()
	if ok {
		return perms, true
	}
	if cfg, found := r.storedGroupConfig(event.ProfileID, groupID); found && len(cfg.WholeMuteRestorePermissions) > 0 {
		return cfg.WholeMuteRestorePermissions, true
	}
	return nil, false
}

func (r *Runtime) forgetWholeMuteSnapshot(event MessageEvent, groupID string) {
	r.wholeMuteSnapshots.mu.Lock()
	delete(r.wholeMuteSnapshots.entries, wholeMuteSnapshotKey(event.ProfileID, groupID))
	r.wholeMuteSnapshots.mu.Unlock()
	if cfg, found := r.storedGroupConfig(event.ProfileID, groupID); found && len(cfg.WholeMuteRestorePermissions) > 0 {
		r.persistWholeMuteSnapshot(event, groupID, nil)
	}
}

func (r *Runtime) persistWholeMuteSnapshot(event MessageEvent, groupID string, perms map[string]bool) {
	cfg, found := r.storedGroupConfig(event.ProfileID, groupID)
	if !found {
		return
	}
	cfg.WholeMuteRestorePermissions = perms
	if _, err := r.saveGroupConfig(cfg); err != nil {
		log.Printf("diana whole mute snapshot persist failed: group=%s: %v", groupID, err)
	}
}

func (r *Runtime) storedGroupConfig(profileID, groupID string) (GroupConfig, bool) {
	r.mu.RLock()
	store := r.groupConfigs
	r.mu.RUnlock()
	if store == nil {
		return GroupConfig{}, false
	}
	return store.ConfigForGroup(strings.TrimSpace(profileID), strings.TrimSpace(groupID))
}

// telegramMuteAll 先读权限存快照，再全员禁言。读不到权限也照样禁言——禁言是
// 当下要紧的事——只是在结果里说明解除时只能按默认恢复。
func (t *dianaPlatformTool) telegramMuteAll(ctx context.Context, groupID string) (map[string]any, error) {
	perms, readErr := t.runtime.telegramChatPermissions(ctx, t.event, groupID)
	data, err := t.runtime.callPlatformAPIForEvent(ctx, t.event, "setChatPermissions", map[string]any{"chat_id": groupID, "permissions": telegramMutedPermissions()})
	if err != nil {
		return nil, err
	}
	if data == nil {
		data = map[string]any{}
	}
	if readErr != nil {
		data["note"] = "没能读到群原来的权限（" + readErr.Error() + "），解除全员禁言时只能按默认成员权限恢复"
		return data, nil
	}
	t.runtime.rememberWholeMuteSnapshot(t.event, groupID, perms)
	return data, nil
}

// telegramUnmuteAll 按快照还原群权限，没有快照才退回默认并说明。
func (t *dianaPlatformTool) telegramUnmuteAll(ctx context.Context, groupID string) (map[string]any, error) {
	permissions := map[string]any{}
	snapshot, ok := t.runtime.wholeMuteSnapshot(t.event, groupID)
	if ok {
		for key, value := range snapshot {
			permissions[key] = value
		}
	} else {
		permissions = telegramMemberDefaultPermissions()
	}
	data, err := t.runtime.callPlatformAPIForEvent(ctx, t.event, "setChatPermissions", map[string]any{"chat_id": groupID, "permissions": permissions})
	if err != nil {
		return nil, err
	}
	if data == nil {
		data = map[string]any{}
	}
	if ok {
		t.runtime.forgetWholeMuteSnapshot(t.event, groupID)
		data["restored_from_snapshot"] = true
	} else {
		data["note"] = wholeMuteDefaultRestoreNote
	}
	return data, nil
}
