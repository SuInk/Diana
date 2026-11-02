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
	data, err := toolFileSource(runtime, event, owner, input, limit)
	if err != nil {
		return "", err
	}
	content := string(data)
	if !utf8.ValidString(content) {
		return "", fmt.Errorf("content 不是合法的 UTF-8 文本")
	}
	return content, nil
}

// toolFileSource 和 toolTextSource 同一套来源规则，但 path 读到的文件不要求是文本，
// send_file 用它交付 .mid 之类的二进制文件。content 参数本身仍是模型写的文本。
func toolFileSource(runtime *Runtime, event MessageEvent, owner bool, input map[string]any, limit int64) ([]byte, error) {
	content := configToolString(input, "content")
	path := strings.TrimSpace(configToolString(input, "path"))
	if path != "" {
		if strings.TrimSpace(content) != "" {
			return nil, fmt.Errorf("content 和 path 只能提供一个；已有文件直接用 path，无需 read_file 后复制正文")
		}
		cfg := agent.Config{WorkDir: AgentWorkspaceDir()}
		if runtime != nil {
			cfg.MCPConfigPath = runtime.effectiveConfigForEvent(event).AgentMCPConfigPath
		}
		rel, err := agent.NormalizeWorkspacePath(cfg.WorkDir, path)
		if err != nil {
			return nil, err
		}
		if agent.WorkspaceFileProtected(cfg, rel) {
			return nil, fmt.Errorf("%s 是受保护的运行时配置，不能渲染或发送", path)
		}
		var grant string
		if !owner {
			grant, err = runtime.workspaceGroupFileGrant(event, rel)
			if err != nil {
				return nil, err
			}
		}
		data, err := agent.ReadWorkspaceFile(cfg.WorkDir, rel, limit)
		if err != nil {
			return nil, err
		}
		if !owner && workspaceAccessHash(data) != grant {
			return nil, fmt.Errorf("文件内容已改变，本群原有访问记录不再适用；请从本群原消息重新保存")
		}
		content = string(data)
	}
	if strings.TrimSpace(content) == "" {
		return nil, fmt.Errorf("content 是空的；请提供正文，或用 path 指定有权访问的工作目录文件")
	}
	if int64(len(content)) > limit {
		return nil, fmt.Errorf("内容有 %d 字节，超过上限 %d 字节", len(content), limit)
	}
	return []byte(content), nil
}
