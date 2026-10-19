// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package taueval

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// CheckState 核对终态，返回不满足的项。
func CheckState(expect Expect, state map[string]any) []string {
	var failures []string
	keys := make([]string, 0, len(expect.State))
	for k := range expect.State {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		want := cloneJSON(expect.State[k])
		got := state[k]
		if !matches(want, got, contains(expect.Exact, k)) {
			failures = append(failures, describeMismatch("state."+k, want, got, contains(expect.Exact, k)))
		}
	}
	return failures
}

// describeMismatch 只报没配上的期望元素，免得整份终态刷屏；完整终态在 Trial.State 里。
func describeMismatch(path string, want, got any, exact bool) string {
	w, wok := want.([]any)
	g, gok := got.([]any)
	if !wok || !gok {
		return fmt.Sprintf("%s 期望 %s，实际 %s", path, marshal(want), marshal(got))
	}
	if exact && len(w) != len(g) {
		return fmt.Sprintf("%s 期望 %d 条，实际 %d 条", path, len(w), len(g))
	}
	var missing []string
	for _, item := range w {
		if !assign([]any{item}, g, make([]bool, len(g))) {
			missing = append(missing, marshal(item))
		}
	}
	if len(missing) == 0 {
		return path + " 的期望元素无法一一对应到不同记录"
	}
	return fmt.Sprintf("%s 没有匹配 %s 的记录", path, strings.Join(missing, "、"))
}

// CheckCalls 核对 Expect.Calls。
func CheckCalls(expect Expect, calls []Call) []string {
	var want, got []any
	for _, c := range expect.Calls {
		want = append(want, map[string]any{"tool": c.Tool, "input": cloneJSON(c.Input)})
	}
	for _, c := range calls {
		if c.Error == "" {
			got = append(got, map[string]any{"tool": c.Tool, "input": cloneJSON(c.Input)})
		}
	}
	if matches(want, got, false) {
		return nil
	}
	return []string{describeMismatch("calls", want, got, false)}
}

// CheckTrajectory 核对工具使用与回复内容。
func CheckTrajectory(expect Expect, calls []string, replies []string) []string {
	var failures []string
	used := map[string]bool{}
	for _, name := range calls {
		used[name] = true
	}
	for _, name := range expect.RequireTools {
		if !used[name] {
			failures = append(failures, "未调用 "+name)
		}
	}
	for _, name := range expect.ForbidTools {
		if used[name] {
			failures = append(failures, "调用了禁止的 "+name)
		}
	}
	all := strings.Join(replies, "\n")
	for _, want := range expect.Replies {
		if !matchString(want, all) {
			failures = append(failures, "回复里没有 "+want)
		}
	}
	return failures
}

// matches：对象按键子集匹配；数组里每个期望元素要各自配上一个不同的实际元素，exact 时
// 长度也须相等；字符串以 "re:" 开头时按正则匹配。值为 null 表示该键必须不存在或为空。
func matches(want, got any, exact bool) bool {
	switch w := want.(type) {
	case nil:
		return got == nil || isEmpty(got)
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return false
		}
		for k, v := range w {
			if !matches(v, g[k], false) {
				return false
			}
		}
		return true
	case []any:
		g, _ := got.([]any)
		if exact && len(g) != len(w) {
			return false
		}
		return assign(w, g, make([]bool, len(g)))
	case string:
		s, ok := got.(string)
		if !ok {
			return false
		}
		if strings.HasPrefix(w, "re:") {
			return matchString(w, s)
		}
		return w == s
	default:
		return fmt.Sprint(want) == fmt.Sprint(got)
	}
}

// assign 用回溯找一一对应，避免两个期望元素抢同一条记录。
func assign(want, got []any, taken []bool) bool {
	if len(want) == 0 {
		return true
	}
	for i, g := range got {
		if !taken[i] && matches(want[0], g, false) {
			taken[i] = true
			if assign(want[1:], got, taken) {
				return true
			}
			taken[i] = false
		}
	}
	return false
}

func matchString(want, got string) bool {
	if pattern, ok := strings.CutPrefix(want, "re:"); ok {
		re, err := regexp.Compile(pattern)
		return err == nil && re.MatchString(got)
	}
	return strings.Contains(got, want)
}

func isEmpty(v any) bool {
	switch x := v.(type) {
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	case string:
		return x == ""
	}
	return false
}

// PassHatK 按 τ-bench 定义算 pass^k：每个任务 n 次试验成功 c 次，取 C(c,k)/C(n,k) 的平均。
// 它衡量「连续 k 次都做对」的概率，比 pass@k 更能反映线上一致性。
func PassHatK(successes, trials []int, k int) float64 {
	if len(successes) == 0 {
		return 0
	}
	var sum float64
	for i := range successes {
		if trials[i] < k {
			return -1
		}
		sum += binom(successes[i], k) / binom(trials[i], k)
	}
	return sum / float64(len(successes))
}

func binom(n, k int) float64 {
	if k < 0 || k > n {
		return 0
	}
	result := 1.0
	for i := 1; i <= k; i++ {
		result = result * float64(n-k+i) / float64(i)
	}
	return result
}
