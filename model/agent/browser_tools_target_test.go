// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import "testing"

// 常驻浏览器里开机那个 about:blank 会一直在列表里，选页时必须跳过它，
// 否则 browser_text 永远读到空白页。
func TestIsBlankBrowserTarget(t *testing.T) {
	blank := []string{"", "about:blank", "chrome://newtab/", "CHROME://settings", "devtools://devtools/bundled/x.html"}
	for _, value := range blank {
		if !isBlankBrowserTarget(value) {
			t.Fatalf("%q 应被当成空白页", value)
		}
	}
	for _, value := range []string{"https://example.com/", "http://127.0.0.1:8080/x"} {
		if isBlankBrowserTarget(value) {
			t.Fatalf("%q 是正经网页，不该被跳过", value)
		}
	}
}
