package assistant

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/SuInk/diana/model/agent"
)

func workspaceAccessHash(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func (r *Runtime) workspaceGroupScope(event MessageEvent) string {
	if r == nil || event.Kind != EventKindGroup || strings.TrimSpace(event.GroupID) == "" {
		return ""
	}
	cfg := r.effectiveConfigForEvent(event)
	bot := firstNonEmpty(event.ProfileID, cfg.ID, cfg.BotAccount)
	if bot == "" {
		return ""
	}
	key, _ := json.Marshal([]string{bot, r.currentPlatform(event), event.GroupID})
	return workspaceAccessHash(key)
}

func workspaceGroupGrantPath(scope, rel string) string {
	return filepath.Join(agent.DianaStateDir(AgentWorkspaceDir()), "group-file-access", scope, workspaceAccessHash([]byte(rel))+".sha256")
}

// Grants bind the exact bytes saved in this group, not just a reusable filename.
// An overwrite by another session therefore cannot inherit an old group's access.
func (r *Runtime) grantWorkspaceGroupFile(event MessageEvent, rel string, data []byte) error {
	scope := r.workspaceGroupScope(event)
	if scope == "" {
		return nil
	}
	return writeAtomicMediaFile(workspaceGroupGrantPath(scope, rel), []byte(workspaceAccessHash(data)))
}

func (r *Runtime) workspaceGroupFileGrant(event MessageEvent, rel string) (string, error) {
	scope := r.workspaceGroupScope(event)
	if scope == "" {
		return "", fmt.Errorf("普通成员只能读取本群已保存的工作区文件；私聊或无群归属的文件不可用")
	}
	grant, err := os.ReadFile(workspaceGroupGrantPath(scope, rel))
	if err != nil || len(grant) != sha256.Size*2 {
		return "", fmt.Errorf("该文件没有本群的访问记录；请先用 save_to_workspace 从本群消息或公网链接保存，再使用返回的 path")
	}
	return string(grant), nil
}
