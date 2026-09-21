// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import "sync"

// botProfileChangeNotifier 把「机器人配置被改过了」这件事播出去。
//
// 打开着的 WebUI 只在自己发过写请求之后才重新拉配置，所以从别处改的看不见：
// 主人在聊天里让机器人换模型，走的是同一份机器人配置（SaveModelRole），页面上
// 的「模型分配」却还停在打开时那一版，要手动刷新才对得上。屏蔽名单、禁用群、
// 回复设置这几个聊天指令也一样。
//
// 回调只负责通知，不带配置内容：调用点在存储的写锁边界上，回调如果回头读存储
// 就会自己锁死自己。通知到达后由前端重新拉一次配置。
type botProfileChangeNotifier struct {
	mu       sync.Mutex
	listener func()
}

// SetChangeListener 注册配置变更回调，传 nil 取消订阅。
func (n *botProfileChangeNotifier) SetChangeListener(listener func()) {
	n.mu.Lock()
	n.listener = listener
	n.mu.Unlock()
}

// notifyChanged 在配置写入成功后调用；调用点必须已经放开存储的锁。
func (n *botProfileChangeNotifier) notifyChanged() {
	n.mu.Lock()
	listener := n.listener
	n.mu.Unlock()
	if listener != nil {
		listener()
	}
}
