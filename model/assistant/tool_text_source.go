package assistant

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/SuInk/diana/model/agent"
)

// Read files in the runtime, not by asking the model to copy paginated tool output.
// Members can consume only bytes explicitly saved in the current group.
func toolTextSource(runtime *Runtime, event MessageEvent, owner bool, input map[string]any, limit int64) (string, error) {
	content := configToolString(input, "content")
	path := strings.TrimSpace(configToolString(input, "path"))
	if path != "" {
		if strings.TrimSpace(content) != "" {
			return "", fmt.Errorf("content 和 path 只能提供一个；已有文件直接用 path，无需 read_file 后复制正文")
		}
		cfg := agent.Config{WorkDir: AgentWorkspaceDir()}
		if runtime != nil {
			cfg.MCPConfigPath = runtime.effectiveConfigForEvent(event).AgentMCPConfigPath
		}
		rel, err := agent.NormalizeWorkspacePath(cfg.WorkDir, path)
		if err != nil {
			return "", err
		}
		if agent.WorkspaceFileProtected(cfg, rel) {
			return "", fmt.Errorf("%s 是受保护的运行时配置，不能渲染或发送", path)
		}
		var grant string
		if !owner {
			grant, err = runtime.workspaceGroupFileGrant(event, rel)
			if err != nil {
				return "", err
			}
		}
		data, err := agent.ReadWorkspaceFile(cfg.WorkDir, rel, limit)
		if err != nil {
			return "", err
		}
		if !owner && workspaceAccessHash(data) != grant {
			return "", fmt.Errorf("文件内容已改变，本群原有访问记录不再适用；请从本群原消息重新保存")
		}
		content = string(data)
	}
	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("content 是空的；请提供正文，或用 path 指定有权访问的工作目录文件")
	}
	if !utf8.ValidString(content) {
		return "", fmt.Errorf("content 不是合法的 UTF-8 文本")
	}
	if int64(len(content)) > limit {
		return "", fmt.Errorf("内容有 %d 字节，超过上限 %d 字节", len(content), limit)
	}
	return content, nil
}
