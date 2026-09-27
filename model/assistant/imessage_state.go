// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	// imessageRecentGUIDLimit 覆盖轮询一页（100 条）再加上 webhook 同期送来的量，
	// 足够让重启后的第一轮轮询认出已经处理过的消息。
	imessageRecentGUIDLimit  = 512
	imessageDirectChatsLimit = 4096
)

// imessageState 是要跨重启保留的通道状态：轮询游标、最近处理过的消息 guid，
// 以及私聊对象对应的真实会话 guid。
//
// 只放内存的话，重启后轮询要么从本机时间重新开始（漏掉停机期间的消息），要么把
// webhook 已经回答过的消息再拉一遍；私聊也会退回拼出来的 iMessage 会话，只能收
// 短信的联系人就发不出去了。
type imessageState struct {
	mu   sync.Mutex
	path string

	CursorMS    int64             `json:"cursor_ms,omitempty"`
	RecentGUIDs []string          `json:"recent_guids,omitempty"`
	DirectChats map[string]string `json:"direct_chats,omitempty"`

	recent map[string]bool
}

func imessageStateDir() string {
	if value := strings.TrimSpace(os.Getenv("DIANA_IMESSAGE_STATE_DIR")); value != "" {
		return value
	}
	if dbPath := strings.TrimSpace(os.Getenv("APP_DB_PATH")); dbPath != "" {
		return filepath.Join(filepath.Dir(dbPath), "imessage")
	}
	if cacheDir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(cacheDir, "diana", "imessage")
	}
	return "imessage"
}

// loadIMessageState 读取某个配置档连某台服务器的状态。换了服务器地址就是另一份：
// 两台 Mac 的消息 guid 和会话互不相干。
func loadIMessageState(profileID, serverBase string) *imessageState {
	sum := sha256.Sum256([]byte(strings.TrimSpace(profileID) + "\x00" + serverBase))
	state := &imessageState{path: filepath.Join(imessageStateDir(), hex.EncodeToString(sum[:8])+".json")}
	if raw, err := os.ReadFile(state.path); err == nil {
		if err := json.Unmarshal(raw, state); err != nil {
			log.Printf("imessage: 状态文件损坏，按空状态处理: %v", err)
		}
	}
	if state.DirectChats == nil {
		state.DirectChats = map[string]string{}
	}
	state.recent = make(map[string]bool, len(state.RecentGUIDs))
	for _, guid := range state.RecentGUIDs {
		state.recent[guid] = true
	}
	return state
}

// saveLocked 原子写回；失败只记日志，状态文件写不进去不该让收发停下来。
func (s *imessageState) saveLocked() {
	if s.path == "" {
		return
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		log.Printf("imessage: 写状态失败: %v", err)
		return
	}
	temp := s.path + ".tmp"
	if err := os.WriteFile(temp, raw, 0o600); err != nil {
		log.Printf("imessage: 写状态失败: %v", err)
		return
	}
	if err := os.Rename(temp, s.path); err != nil {
		log.Printf("imessage: 写状态失败: %v", err)
	}
}

// accept 返回这条消息是不是第一次见到，并记下来。
func (s *imessageState) accept(guid string) bool {
	guid = strings.TrimSpace(guid)
	if guid == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recent[guid] {
		return false
	}
	s.recent[guid] = true
	s.RecentGUIDs = append(s.RecentGUIDs, guid)
	if overflow := len(s.RecentGUIDs) - imessageRecentGUIDLimit; overflow > 0 {
		for _, old := range s.RecentGUIDs[:overflow] {
			delete(s.recent, old)
		}
		s.RecentGUIDs = append([]string(nil), s.RecentGUIDs[overflow:]...)
	}
	s.saveLocked()
	return true
}

func (s *imessageState) cursor() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.CursorMS
}

// advanceCursor 只往前走。BlueBubbles 的 after 是 >=，游标停在已见过的最新时间上，
// 同一毫秒的其他消息下一轮还能拉到，已处理的靠 guid 去重。
func (s *imessageState) advanceCursor(ms int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ms <= s.CursorMS {
		return
	}
	s.CursorMS = ms
	s.saveLocked()
}

func (s *imessageState) directChat(handle string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.DirectChats[handle]
}

func (s *imessageState) rememberDirectChat(handle, guid string) {
	handle, guid = strings.TrimSpace(handle), strings.TrimSpace(guid)
	if handle == "" || guid == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.DirectChats[handle] == guid {
		return
	}
	if _, known := s.DirectChats[handle]; !known && len(s.DirectChats) >= imessageDirectChatsLimit {
		// 满了就不再记新联系人；查不到时发送前还会去服务端查一次会话。
		return
	}
	s.DirectChats[handle] = guid
	s.saveLocked()
}
