// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
)

// 内置预设默认就在 MCP 列表里占一行（「有这个服务，只是还没配」）。用不上的那几条
// 可以从列表里删掉——删的是这一行，不是服务本身：名单存在这里，随时能放回来。
func hiddenPresetPath(root string) string {
	return mcpPresetsHiddenState.path(root)
}

func loadHiddenPresets(root string) (map[string]bool, error) {
	data, err := mcpPresetsHiddenState.read(root)
	if os.IsNotExist(err) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	if err := json.Unmarshal(data, &ids); err != nil {
		return nil, err
	}
	hidden := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			hidden[id] = true
		}
	}
	return hidden, nil
}

func saveHiddenPreset(root, id string, hidden bool) error {
	lock := extensionPathLock(hiddenPresetPath(root))
	lock.Lock()
	defer lock.Unlock()
	current, err := loadHiddenPresets(root)
	if err != nil {
		return err
	}
	if hidden {
		current[id] = true
	} else {
		delete(current, id)
	}
	ids := make([]string, 0, len(current))
	for key := range current {
		ids = append(ids, key)
	}
	sort.Strings(ids)
	data, err := json.MarshalIndent(ids, "", "  ")
	if err != nil {
		return err
	}
	return mcpPresetsHiddenState.save(root, data)
}
