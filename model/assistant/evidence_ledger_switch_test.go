// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "testing"

func TestEvidenceLedgerSwitchDefaultsToEnforcedAndCanBeDisabled(t *testing.T) {
	plugins := NewDefaultPluginManager()
	runtime := &Runtime{plugins: plugins}
	if runtime.evidenceLedgerAdvisory(MessageEvent{}) {
		t.Fatal("默认应保持证据账本强制校验")
	}
	if _, err := plugins.UpdateSettings(webSearchPluginID, map[string]any{webSearchSettingEvidenceLedger: false}); err != nil {
		t.Fatal(err)
	}
	if !runtime.evidenceLedgerAdvisory(MessageEvent{}) {
		t.Fatal("关闭开关后账本应只记录不拦截")
	}
}

func TestEvidenceLedgerSwitchFollowsSearchPluginAvailability(t *testing.T) {
	plugins := NewDefaultPluginManager()
	runtime := &Runtime{plugins: plugins}
	if _, err := plugins.UpdateSettings(webSearchPluginID, map[string]any{webSearchSettingEvidenceLedger: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := plugins.SetEnabled(webSearchPluginID, false); err != nil {
		t.Fatal(err)
	}
	// 搜索插件关掉时账本根本不会激活，这里不该再声称处于宽松模式。
	if runtime.evidenceLedgerAdvisory(MessageEvent{}) {
		t.Fatal("搜索插件停用时不应报告宽松模式")
	}
}
