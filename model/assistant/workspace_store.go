// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "github.com/SuInk/diana/model/agent"

// 工作区放在哪：这台机器能起工作区容器、这台机器人的容器没关（AgentBoxAccess 不是 off）
// 时，工作区就是容器里本会话的范围目录，文件工具、save_to_workspace、send_file 和
// run_command 看到的是同一处；否则是宿主工作目录。AgentBoxAccess 的 owner/all 只决定
// 谁能跑 run_command，群成员不能跑命令时存的文件也进本群的范围目录。

// boxWorkspaceStore 是工作区在容器里时本会话的范围目录；工作区在宿主上时返回 nil。
func (r *Runtime) boxWorkspaceStore(event MessageEvent) agent.FileStore {
	if r == nil {
		return nil
	}
	cfg := r.effectiveConfigForEvent(event)
	if NormalizeBoxAccess(cfg.AgentBoxAccess) == BoxAccessOff || !boxAvailable() {
		return nil
	}
	store, err := boxScopeStore(r.boxKey(event), int64(cfg.WithDefaults().AgentBoxDiskLimitMB)<<20)
	if err != nil {
		return nil
	}
	return store
}

// boxScopeStore 返回 key 这个会话范围的工作区。测试里替换它，免得用例真去起容器。
var boxScopeStore = func(key string, diskLimit int64) (agent.FileStore, error) {
	workBox, err := workBoxFor(key)
	if err != nil {
		return nil, err
	}
	workBox.SetDiskLimit(diskLimit)
	return agent.NewBoxFileStore(workBox), nil
}

// workspaceFiles 是这一轮的工作区：容器里本会话的范围目录，或者宿主工作目录。
func (r *Runtime) workspaceFiles(event MessageEvent) agent.FileStore {
	if store := r.boxWorkspaceStore(event); store != nil {
		return store
	}
	return agent.HostFileStore(r.agentWorkspaceConfig(event))
}
