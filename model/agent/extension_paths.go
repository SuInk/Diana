package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Persist the initial extension locations once so choosing another robot cannot
// silently switch to another skill tree or MCP configuration file.
func GlobalExtensionPaths(cfg Config) (Config, error) {
	cfg = cfg.WithDefaults()
	path := filepath.Join(cfg.WorkDir, ".extension-paths.json")
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
