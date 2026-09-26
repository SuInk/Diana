package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Persist the initial extension locations once so choosing another robot cannot
// silently switch to another skill tree or MCP configuration file.
func GlobalExtensionPaths(cfg Config) (Config, error) {
	cfg = cfg.WithDefaults()
	// 扩展位置记在 .diana/extension-paths.json，和其余运行时配置一起对工具关闭，
	// 见 agentProtectedFiles。
	lock := extensionPathLock(extensionPathsState.path(cfg.WorkDir))
	lock.Lock()
	defer lock.Unlock()
	var locations struct {
		SkillRoots    []string `json:"skill_roots"`
		MCPConfigPath string   `json:"mcp_config_path"`
	}
	data, err := extensionPathsState.read(cfg.WorkDir)
	if os.IsNotExist(err) {
		locations.SkillRoots = cfg.SkillRoots
		locations.MCPConfigPath = cfg.MCPConfigPath
		data, err = json.MarshalIndent(locations, "", "  ")
		if err != nil {
			return cfg, err
		}
		err = extensionPathsState.save(cfg.WorkDir, data)
	} else if err == nil {
		err = json.Unmarshal(data, &locations)
	}
	if err != nil {
		return cfg, err
	}
	moved, err := migrateMCPConfigOutOfWorkspace(cfg.WorkDir, locations.MCPConfigPath)
	if err != nil {
		return cfg, err
	}
	if moved != locations.MCPConfigPath {
		locations.MCPConfigPath = moved
		data, err := json.MarshalIndent(locations, "", "  ")
		if err != nil {
			return cfg, err
		}
		if err := extensionPathsState.save(cfg.WorkDir, data); err != nil {
			return cfg, err
		}
	}
	cfg.SkillRoots = locations.SkillRoots
	cfg.MCPConfigPath = locations.MCPConfigPath
	return cfg, nil
}

// migrateMCPConfigOutOfWorkspace 把工作目录里的 MCP 配置挪到目录外面，返回之后该用的
// 路径。老版本默认把它写在工作目录里，那正好是文件工具够得着的范围。
//
// 目标位置已经有文件时不动：那多半是用户自己放的，覆盖掉等于弄丢一份真配置；这种情况
// 留在原处，由 agentProtectedFiles 那道黑名单继续挡着。
func migrateMCPConfigOutOfWorkspace(workDir, current string) (string, error) {
	current = strings.TrimSpace(current)
	if current == "" || workDir == "" {
		return current, nil
	}
	absWork, err := filepath.Abs(workDir)
	if err != nil {
		return current, nil
	}
	absCurrent := current
	if !filepath.IsAbs(absCurrent) {
		absCurrent = filepath.Join(absWork, absCurrent)
	}
	absCurrent = filepath.Clean(absCurrent)
	relation, err := filepath.Rel(absWork, absCurrent)
	if err != nil || relation == ".." || strings.HasPrefix(relation, ".."+string(filepath.Separator)) {
		// 已经在工作目录外面，没什么可搬的。
		return current, nil
	}
	target := defaultMCPConfigPath(absWork)
	if filepath.Clean(target) == absCurrent {
		return current, nil
	}
	if _, err := os.Stat(target); err == nil {
		return current, nil
	} else if !os.IsNotExist(err) {
		return current, err
	}
	if _, err := os.Stat(absCurrent); os.IsNotExist(err) {
		// 还没建过配置：只把位置改到外面，下次保存就写在新地方。
		return target, nil
	} else if err != nil {
		return current, err
	}
	if err := os.Rename(absCurrent, target); err != nil {
		return current, err
	}
	return target, nil
}
