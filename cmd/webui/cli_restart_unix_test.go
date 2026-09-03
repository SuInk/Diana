//go:build !windows

package main

import "testing"

func TestSudoNeedsPassword(t *testing.T) {
	// 需要密码：值得换成交互式再来一次。
	for _, output := range []string{
		"sudo: a password is required",
		"sudo: no tty present and no askpass program specified",
		"SUDO: A PASSWORD IS REQUIRED",
		// 非交互调用拿不到终端；接上真实终端重试是能成功的，不该当硬失败。
		"sudo: a terminal is required to read the password; either use the -S option to read from standard input or configure an askpass helper",
	} {
		if !sudoNeedsPassword(output) {
			t.Fatalf("sudoNeedsPassword(%q) = false, want true", output)
		}
	}
	// 真的失败了：重试没有意义，应把原始错误报给用户。
	for _, output := range []string{
		"Failed to restart diana.service: Unit diana.service not found.",
		"sudo: user miku is not allowed to execute '/usr/bin/systemctl restart diana.service' as root",
		"",
	} {
		if sudoNeedsPassword(output) {
			t.Fatalf("sudoNeedsPassword(%q) = true, want false", output)
		}
	}
}
