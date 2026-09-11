// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// 跨语言一致性只能从 Go 这边钉。前端的测试跑在 Docker 的 frontend-next 构建阶段里，
// 那一层只 COPY 了 frontend-next/ 一个目录，读不到 model/assistant 下的 .go；同样的
// 断言写在 .test.mjs 里本机全绿、进了镜像必红（npm 的 prebuild 会跑 npm test）。
// Go 测试跑在完整仓库上，两边的源码都读得到，所以对照放在这边。

// repoRoot 从当前工作目录往上找 go.mod，而不是写死 "../.."：测试的工作目录是包目录，
// 但包将来可能挪位置，写死的相对路径会悄悄指到别处去。
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("从 %q 一路往上都没找到 go.mod，定位不到仓库根目录", dir)
		}
		dir = parent
	}
}

// readRepoFile 读仓库里的文件。文件不在就直接失败，不 skip——这些测试存在的唯一理由
// 就是「两边不许各改各的」，静悄悄跳过等于把这条保证删了。
func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	path := filepath.Join(repoRoot(t), filepath.FromSlash(rel))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到 %s：%v（这份对照不允许跳过：前后端各改一边就是这条测试要拦的事）", rel, err)
	}
	return string(data)
}

var participationThresholdEntryPattern = regexp.MustCompile(`(?m)^\s*(\w+)\s*:\s*(null|[0-9.]+)\s*,?\s*$`)

// frontendParticipationThresholds 解析 frontend-next/src/participation.ts 里的
// participationLevelThresholds，返回档位到门槛的映射；null 用 nil 表示。
func frontendParticipationThresholds(t *testing.T, source string) map[string]*float64 {
	t.Helper()
	const marker = "export const participationLevelThresholds"
	start := strings.Index(source, marker)
	if start < 0 {
		t.Fatalf("frontend-next/src/participation.ts 里应当有 %s", marker)
	}
	open := strings.Index(source[start:], "{")
	if open < 0 {
		t.Fatalf("%s 后面没有找到 { 开头的门槛表", marker)
	}
	open += start
	end := strings.Index(source[open:], "}")
	if end < 0 {
		t.Fatalf("%s 的门槛表没有闭合的 }", marker)
	}
	body := source[open+1 : open+end]
	entries := map[string]*float64{}
	for _, m := range participationThresholdEntryPattern.FindAllStringSubmatch(body, -1) {
		if m[2] == "null" {
			entries[m[1]] = nil
			continue
		}
		v, err := strconv.ParseFloat(m[2], 64)
		if err != nil {
			t.Fatalf("门槛 %s: %s 不是数字：%v", m[1], m[2], err)
		}
		entries[m[1]] = &v
	}
	if len(entries) == 0 {
		t.Fatalf("%s 的门槛表解析出来是空的：%q", marker, body)
	}
	return entries
}

// 前端界面把「极低（≥0.90，最严）」这种门槛数字直接写给用户看，那串数字是后端
// ratingPasses 判断口径的复述。改了后端忘了前端，界面就开始骗人：用户照着「≥0.50」
// 调档位，实际过闸的是另一个分数。这条逐档位对照，顺带钉住两边的档位集合一致。
func TestFrontendParticipationThresholdsMatchRatingPasses(t *testing.T) {
	// 档位集合以 validParticipationLevel 为准，后端加了新档位而前端没跟上也会红。
	levels := []string{"off", "minimal", "low", "medium", "high", "extreme", "always"}
	for _, level := range levels {
		if !validParticipationLevel(level) {
			t.Fatalf("%q 不是后端认的档位，这份档位清单本身过期了", level)
		}
	}
	frontend := frontendParticipationThresholds(t, readRepoFile(t, "frontend-next/src/participation.ts"))

	for _, level := range levels {
		threshold, ok := frontend[level]
		if !ok {
			t.Fatalf("前端 participationLevelThresholds 缺少档位 %q：这张表是 ratingPasses 的复述，少一档界面上就没有门槛可显示", level)
		}
		switch level {
		case "off", "always":
			// off 和 always 在 ratingPasses 里提前返回，不走门槛，前端用 null 表示「不看分数」。
			if threshold != nil {
				t.Fatalf("前端给 %q 写了门槛 %v，但 ratingPasses 里 %q 提前返回、根本不比分数，这里必须是 null", level, *threshold, level)
			}
			if got := ratingPasses(1, level); got != (level == "always") {
				t.Fatalf("ratingPasses(1, %q) = %v，和「%s 不看分数」的口径对不上", level, got, level)
			}
			if got := ratingPasses(0, level); got != (level == "always") {
				t.Fatalf("ratingPasses(0, %q) = %v，和「%s 不看分数」的口径对不上", level, got, level)
			}
		default:
			if threshold == nil {
				t.Fatalf("前端把 %q 写成了 null，但 ratingPasses 里它有实打实的门槛", level)
			}
			// 门槛是「>=」：正好等于要过，比它小一丝就不过。这样钉住的是那个具体数字，
			// 而不只是「差不多在这一档」。
			if !ratingPasses(*threshold, level) {
				t.Fatalf("前端标 %q 的门槛是 ≥%.2f，但 ratingPasses(%v, %q) 没过——数字改了必须两边同步，否则界面会骗人", level, *threshold, *threshold, level)
			}
			if below := math.Nextafter(*threshold, 0); ratingPasses(below, level) {
				t.Fatalf("前端标 %q 的门槛是 ≥%.2f，但 ratingPasses 在 %v 就放行了：后端门槛比前端写的低，界面会骗人", level, *threshold, below)
			}
		}
	}

	for level := range frontend {
		if !validParticipationLevel(level) {
			t.Fatalf("前端 participationLevelThresholds 里的 %q 后端不认：ratingPasses 会把它当未知档位一律拦下，界面上却显示成一个能用的档", level)
		}
	}
}

var jsStringLiteralPattern = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)

// frontendDefaultSystemPrompt 从 frontend-next/src/builtin-personas.ts 里还原
// defaultSystemPrompt 的正文。前端写成数组 .join("\n")，这里按同样的规则拼回去。
func frontendDefaultSystemPrompt(t *testing.T, source string) string {
	t.Helper()
	const marker = "export const defaultSystemPrompt"
	start := strings.Index(source, marker)
	if start < 0 {
		t.Fatalf("frontend-next/src/builtin-personas.ts 里应当导出 defaultSystemPrompt")
	}
	eq := strings.Index(source[start:], "=")
	if eq < 0 {
		t.Fatalf("%s 后面没有赋值", marker)
	}
	rest := source[start+eq+1:]
	end := strings.Index(rest, ";")
	if end < 0 {
		t.Fatalf("%s 的声明没有以 ; 结束", marker)
	}
	decl := rest[:end]

	sep := ""
	if join := strings.Index(decl, ".join("); join >= 0 {
		args := jsStringLiteralPattern.FindStringSubmatch(decl[join:])
		if args == nil {
			t.Fatalf("defaultSystemPrompt 的 .join() 参数不是字符串字面量：%q", decl[join:])
		}
		sep = unquoteJS(t, args[1])
		decl = decl[:join]
	}

	matches := jsStringLiteralPattern.FindAllStringSubmatch(decl, -1)
	if len(matches) == 0 {
		t.Fatalf("defaultSystemPrompt 里没解析出任何字符串字面量：%q", decl)
	}
	parts := make([]string, 0, len(matches))
	for _, m := range matches {
		parts = append(parts, unquoteJS(t, m[1]))
	}
	return strings.Join(parts, sep)
}

func unquoteJS(t *testing.T, raw string) string {
	t.Helper()
	var out string
	if err := json.Unmarshal([]byte(fmt.Sprintf(`"%s"`, raw)), &out); err != nil {
		t.Fatalf("解析字符串字面量 %q 失败：%v", raw, err)
	}
	return out
}

// 控制台的「恢复内置提示词」曾经带着一份自己的默认人设，停在重写之前的旧文案上：
// 点一次就把新的兜底人设覆盖成旧的 60 字。两份文案没有任何东西对着比，所以没人发现。
// 现在前端只留这一份，并且逐字节钉在 Go 常量上——改了一边忘了另一边，这条直接红。
func TestFrontendDefaultSystemPromptMatchesBackend(t *testing.T) {
	frontend := frontendDefaultSystemPrompt(t, readRepoFile(t, "frontend-next/src/builtin-personas.ts"))
	if frontend != defaultSystemPrompt {
		t.Fatalf("前端那份默认人设和后端 defaultSystemPrompt 不一致，「恢复内置提示词」会把用户的人设覆盖成过期文案。\n前端 (%d 字节):\n%s\n后端 (%d 字节):\n%s", len(frontend), frontend, len(defaultSystemPrompt), defaultSystemPrompt)
	}
}
