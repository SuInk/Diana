// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package browserctl

import (
	"testing"
)

// 同一个扩展重连时，控制面必须只留最新那条连接。
//
// 真机上扩展大约每分半重连一次，而控制面要等心跳超时（90 秒）才会发现旧连接
// 是半开的。不顶掉旧连接的话，稳态就是同一个浏览器挂着两条连接，
// pickConnection 于是要求调用方点名——两条在模型眼里长得一模一样，
// 结果是 browser_ext_* 在这段时间里全都用不了。
func TestRegisterReplacesPreviousConnectionFromSameExtension(t *testing.T) {
	hub, first, firstConn := newTestHub(t, readWritePolicy(), nil)

	secondConn := &fakeConn{}
	second, _, err := hub.Register(secondConn, Hello{ProtocolVersion: ProtocolVersion, ExtensionID: "abc"}, TokenInfo{ID: "t1"})
	if err != nil {
		t.Fatalf("重连握手失败：%v", err)
	}

	if got := len(hub.Connections()); got != 1 {
		t.Fatalf("同一个扩展重连后应只剩一条连接，实际 %d 条", got)
	}
	if !firstConn.isClosed() {
		t.Fatal("旧连接应被关闭")
	}
	picked, err := hub.pickConnection("")
	if err != nil {
		t.Fatalf("重连后不点名也应能选出连接：%v", err)
	}
	if picked.ID() != second.ID() {
		t.Fatalf("应选中新连接 %s，实际 %s", second.ID(), picked.ID())
	}
	if _, err := hub.pickConnection(first.ID()); err == nil {
		t.Fatal("旧连接 ID 应已不可用")
	}
}

// 不同扩展（用户真的连了两个浏览器）仍然各自保留，点名规则不变。
func TestRegisterKeepsConnectionsFromDifferentExtensions(t *testing.T) {
	hub, _, _ := newTestHub(t, readWritePolicy(), nil)
	if _, _, err := hub.Register(&fakeConn{}, Hello{ProtocolVersion: ProtocolVersion, ExtensionID: "def"}, TokenInfo{ID: "t2"}); err != nil {
		t.Fatalf("第二个扩展握手失败：%v", err)
	}
	if got := len(hub.Connections()); got != 2 {
		t.Fatalf("两个扩展应各留一条连接，实际 %d 条", got)
	}
	if _, err := hub.pickConnection(""); err == nil {
		t.Fatal("连着两个浏览器时应要求点名")
	}
}
