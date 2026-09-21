package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// extensionPathsFileName 记着 skill 目录和 MCP 配置的位置，和其余运行时配置一起
// 对工具关闭，见 agentProtectedFiles。
const extensionPathsFileName = ".extension-paths.json"

// Persist the initial extension locations once so choosing another robot cannot
// silently switch to another skill tree or MCP configuration file.
func GlobalExtensionPaths(cfg Config) (Config, error) {
	cfg = cfg.WithDefaults()
	path := filepath.Join(cfg.WorkDir, extensionPathsFileName)
	lock := extensionPathLock(path)
	lock.Lock()
	defer lock.Unlock()
	var locations struct {
		SkillRoots    []string `json:"skill_roots"`
		MCPConfigPath string   `json:"mcp_config_path"`
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		locations.SkillRoots = cfg.SkillRoots
		locations.MCPConfigPath = cfg.MCPConfigPath
		data, err = json.MarshalIndent(locations, "", "  ")
		if err != nil {
			return cfg, err
		}
		err = saveExtensionFile(path, data)
	} else if err == nil {
		err = json.Unmarshal(data, &locations)
	}
	if err != nil {
		return cfg, err
	}
	cfg.SkillRoots = locations.SkillRoots
	cfg.MCPConfigPath = locations.MCPConfigPath
	return cfg, nil
}
