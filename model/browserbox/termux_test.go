// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package browserbox

import "testing"

func TestTermuxHeadlessFallsBackWithoutDisplay(t *testing.T) {
	t.Setenv("PREFIX", "/data/data/com.termux/files/usr")
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	if !systemDisplayAvailable() {
		if got := termuxHeadless(Settings{Headful: true}); got.Headful {
			t.Fatal("Termux 没有屏幕时还要有头，启动会直接失败")
		}
	}

	// 开了 termux-x11 之类的图形会话，就照用户的设置开有头。
	t.Setenv("DISPLAY", ":0")
	if got := termuxHeadless(Settings{Headful: true}); !got.Headful {
		t.Fatal("有图形会话时不该改成无头")
	}
}

func TestTermuxHeadlessLeavesOtherSystemsAlone(t *testing.T) {
	t.Setenv("PREFIX", "/usr/local")
	t.Setenv("TERMUX_VERSION", "")
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	if got := termuxHeadless(Settings{Headful: true}); !got.Headful {
		t.Fatal("不在 Termux 时有头设置被改掉了，应该照旧报没有屏幕")
	}
}
