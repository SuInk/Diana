// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// defaultGrepLimit 是一次检索最多返回多少条匹配。
	defaultGrepLimit = 100
	// maxGrepLimit 是调用方能要到的上限；结果还会再被 MaxToolOutputChars 截一刀。
	maxGrepLimit = 500
	// grepMaxLineRunes 是单行最多回传多少字符。压缩过的 JS、日志里一行几万字符是常态，
	// 整行回传会把一条匹配变成一整个工具结果。
	grepMaxLineRunes = 300
	// grepMaxFileBytes 是参与检索的单文件上限，超过就跳过并计数。
	grepMaxFileBytes = 2 << 20
	// defaultFindLimit / maxFindLimit 是按名字找文件的返回条数。
	defaultFindLimit = 200
	maxFindLimit     = 1000
	// maxWalkEntries 是一次遍历最多看多少个条目，防止工作目录被塞满时把一次调用卡死。
	maxWalkEntries = 20000
)

// ---- write_file ------------------------------------------------------------

// WriteFileTool 在 Agent 工作目录内整体写入一个文件。
type WriteFileTool struct {
	root     string
	maxBytes int
}

func (t *WriteFileTool) Name() string { return "write_file" }

func (t *WriteFileTool) Description() string {
	return `在 Agent 工作目录内写入文件，父目录会自动创建。` +
		`整体覆盖：已存在的文件会被完全替换，要改其中一段请用 edit_file，别把整个文件重写一遍。`
}

func (t *WriteFileTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"path", "content"}, map[string]any{
		"path":    toolStringParam("工作目录内的相对文件路径"),
		"content": toolStringParam("要写入的完整内容"),
	})
}

func (t *WriteFileTool) Run(_ context.Context, input map[string]any) (string, error) {
	rel := stringFromInput(input, "path")
	if rel == "" {
		return "", errors.New("path is required")
	}
	content, ok := input["content"]
	if !ok {
		return "", errors.New("content is required")
	}
	text, ok := content.(string)
	if !ok {
		return "", errors.New("content must be a string")
	}
	if limit := t.writeLimit(); len(text) > limit {
		return "", fmt.Errorf("content is %d bytes, over the %d byte limit", len(text), limit)
	}
	target, err := safePath(t.root, rel)
	if err != nil {
		return "", err
	}
	existed := true
	if info, statErr := os.Stat(target); statErr != nil {
		if !os.IsNotExist(statErr) {
			return "", statErr
		}
		existed = false
	} else if info.IsDir() {
		return "", fmt.Errorf("%s is a directory", rel)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(target, []byte(text), 0o644); err != nil {
		return "", err
	}
	return marshalToolResult(map[string]any{
		"path":      relPathForOutput(t.root, target),
		"bytes":     len(text),
		"lines":     strings.Count(text, "\n") + 1,
		"overwrote": existed,
	})
}

func (t *WriteFileTool) writeLimit() int {
	if t.maxBytes > 0 {
		return t.maxBytes
	}
	return MaxAllowedReadFileMaxBytes
}

// ---- edit_file -------------------------------------------------------------

// EditFileTool 用精确文本替换改文件的其中几段。
type EditFileTool struct {
	root     string
	maxBytes int
}

func (t *EditFileTool) Name() string { return "edit_file" }

func (t *EditFileTool) Description() string {
	return `用精确文本替换修改工作目录内的文件。每个 old_text 必须在原文件里唯一命中一处，` +
		`多处命中或找不到都会整批拒绝而不是改错地方；多个替换互相不能重叠，且都按原文件匹配，不是依次生效。` +
		`要整体重写用 write_file。`
}

func (t *EditFileTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"path", "edits"}, map[string]any{
		"path": toolStringParam("工作目录内的相对文件路径"),
		"edits": map[string]any{
			"type":        "array",
			"description": "要应用的替换，按原文件匹配",
			"items": toolObjectSchema([]string{"old_text", "new_text"}, map[string]any{
				"old_text": toolStringParam("原文件里要被替换的片段，必须唯一命中"),
				"new_text": toolStringParam("替换成的内容，留空表示删除"),
			}),
		},
	})
}

type fileEdit struct {
	oldText string
	newText string
	start   int
	end     int
}

func (t *EditFileTool) Run(_ context.Context, input map[string]any) (string, error) {
	rel := stringFromInput(input, "path")
	if rel == "" {
		return "", errors.New("path is required")
	}
	edits, err := parseFileEdits(input["edits"])
	if err != nil {
		return "", err
	}
	target, err := safePath(t.root, rel)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory", rel)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		return "", err
	}
	original := string(raw)

	// 全部按原文件定位。依次生效会让第二个替换命中第一个替换刚写进去的内容，
	// 那种改动从调用方的输入上完全看不出来。
	for i := range edits {
		count := strings.Count(original, edits[i].oldText)
		switch count {
		case 1:
			edits[i].start = strings.Index(original, edits[i].oldText)
			edits[i].end = edits[i].start + len(edits[i].oldText)
		case 0:
			return "", fmt.Errorf("edit %d: old_text 在 %s 里找不到", i+1, rel)
		default:
			return "", fmt.Errorf("edit %d: old_text 在 %s 里命中 %d 处，必须唯一；请带上更多上下文", i+1, rel, count)
		}
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	for i := 1; i < len(edits); i++ {
		if edits[i].start < edits[i-1].end {
			return "", errors.New("edits 互相重叠，请合并成一个替换")
		}
	}

	var builder strings.Builder
	cursor := 0
	for _, edit := range edits {
		builder.WriteString(original[cursor:edit.start])
		builder.WriteString(edit.newText)
		cursor = edit.end
	}
	builder.WriteString(original[cursor:])
	updated := builder.String()

	if limit := t.writeLimit(); len(updated) > limit {
		return "", fmt.Errorf("结果是 %d 字节，超过 %d 字节上限", len(updated), limit)
	}
	// 原封写回去：BOM 和行尾风格都在未被替换的那部分里原样留着，不需要特意处理，
	// 但也因此不要在这里做任何「顺手规范化」。
	if err := os.WriteFile(target, []byte(updated), info.Mode().Perm()); err != nil {
		return "", err
	}
	return marshalToolResult(map[string]any{
		"path":               relPathForOutput(t.root, target),
		"edits_applied":      len(edits),
		"first_changed_line": strings.Count(original[:edits[0].start], "\n") + 1,
		"bytes_before":       len(original),
		"bytes_after":        len(updated),
	})
}

func (t *EditFileTool) writeLimit() int {
	if t.maxBytes > 0 {
		return t.maxBytes
	}
	return MaxAllowedReadFileMaxBytes
}

// parseFileEdits 读出替换列表。模型偶尔会把数组塞成 JSON 字符串，或者在只有一处
// 改动时直接传单个对象；这些都还原得回来，没必要为此让整次调用失败。
func parseFileEdits(raw any) ([]fileEdit, error) {
	if text, ok := raw.(string); ok {
		var decoded any
		if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &decoded); err != nil {
			return nil, errors.New("edits 不是有效的数组")
		}
		raw = decoded
	}
	if single, ok := raw.(map[string]any); ok {
		raw = []any{single}
	}
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return nil, errors.New("edits is required and must contain at least one edit")
	}
	edits := make([]fileEdit, 0, len(items))
	for i, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("edit %d: 必须是对象", i+1)
		}
		oldText := stringFromInput(entry, "old_text")
		if oldText == "" {
			return nil, fmt.Errorf("edit %d: old_text 不能为空", i+1)
		}
		edits = append(edits, fileEdit{oldText: oldText, newText: stringFromInput(entry, "new_text")})
	}
	return edits, nil
}

// ---- grep ------------------------------------------------------------------

// GrepTool 在工作目录内按正则检索文件内容。
type GrepTool struct {
	root     string
	maxBytes int
}

func (t *GrepTool) Name() string { return "grep" }

func (t *GrepTool) Description() string {
	return `在 Agent 工作目录内按内容检索文件，返回「路径:行号: 内容」。` +
		`默认按正则解释 pattern，literal 为真时按字面量。用它定位代码或配置在哪，比逐个 read_file 快得多。`
}

func (t *GrepTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"pattern"}, map[string]any{
		"pattern":     toolStringParam("要检索的正则；literal 为真时按字面量处理"),
		"path":        toolStringParam("限定在工作目录内的这个子目录里找，可选"),
		"glob":        toolStringParam("只看匹配这个通配符的文件，例如 *.go 或 src/**/*.ts，可选"),
		"ignore_case": toolBoolParam("忽略大小写，可选"),
		"literal":     toolBoolParam("把 pattern 当字面量而不是正则，可选"),
		"context":     toolIntParam("每条匹配额外带前后多少行，可选"),
		"limit":       toolIntParam("最多返回多少条匹配，默认 " + fmt.Sprint(defaultGrepLimit)),
	})
}

func (t *GrepTool) Run(ctx context.Context, input map[string]any) (string, error) {
	pattern := stringFromInput(input, "pattern")
	if pattern == "" {
		return "", errors.New("pattern is required")
	}
	if boolFromInput(input, "literal", false) {
		pattern = regexp.QuoteMeta(pattern)
	}
	if boolFromInput(input, "ignore_case", false) {
		pattern = "(?i)" + pattern
	}
	expr, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("pattern 不是有效的正则: %w", err)
	}
	base, err := safePath(t.root, stringFromInput(input, "path"))
	if err != nil {
		return "", err
	}
	globPattern := strings.TrimSpace(stringFromInput(input, "glob"))
	around := intFromInput(input, "context", 0)
	if around < 0 {
		around = 0
	}
	if around > 5 {
		around = 5
	}
	limit := intFromInput(input, "limit", defaultGrepLimit)
	if limit <= 0 || limit > maxGrepLimit {
		limit = defaultGrepLimit
	}

	var (
		out       []string
		matches   int
		skipped   int
		truncated bool
	)
	err = walkAgentFiles(ctx, base, func(fullPath, rel string, info fs.FileInfo) error {
		if matches >= limit {
			truncated = true
			return fs.SkipAll
		}
		if globPattern != "" && !matchGlobPath(globPattern, rel) {
			return nil
		}
		if info.Size() > grepMaxFileBytes {
			skipped++
			return nil
		}
		data, readErr := os.ReadFile(fullPath)
		if readErr != nil {
			// 读不了的单个文件不该让整次检索失败：权限、竞态删除都很常见。
			skipped++
			return nil
		}
		if isProbablyBinary(data) {
			skipped++
			return nil
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if !expr.MatchString(line) {
				continue
			}
			if matches >= limit {
				truncated = true
				return fs.SkipAll
			}
			matches++
			for j := i - around; j <= i+around; j++ {
				if j < 0 || j >= len(lines) {
					continue
				}
				marker := ":"
				if j != i {
					marker = "-"
				}
				out = append(out, fmt.Sprintf("%s:%d%s %s", rel, j+1, marker, clipRunes(lines[j], grepMaxLineRunes)))
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(out) == 0 {
		return fmt.Sprintf("没有匹配（跳过 %d 个二进制或过大的文件）", skipped), nil
	}
	var header strings.Builder
	fmt.Fprintf(&header, "%d 条匹配", matches)
	if truncated {
		fmt.Fprintf(&header, "（已达上限 %d，还有更多）", limit)
	}
	if skipped > 0 {
		fmt.Fprintf(&header, "，跳过 %d 个二进制或过大的文件", skipped)
	}
	return header.String() + "\n" + strings.Join(out, "\n"), nil
}

// ---- find_files ------------------------------------------------------------

// FindFilesTool 在工作目录内按名字/通配符找文件。
type FindFilesTool struct {
	root string
}

func (t *FindFilesTool) Name() string { return "find_files" }

func (t *FindFilesTool) Description() string {
	return `在 Agent 工作目录内按通配符找文件，返回相对路径。` +
		`pattern 不含 / 时只比对文件名（*.go），含 / 时比对相对路径，** 跨目录（src/**/*.ts）。`
}

func (t *FindFilesTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"pattern"}, map[string]any{
		"pattern": toolStringParam("通配符，例如 *.go、config.*、src/**/*.ts"),
		"path":    toolStringParam("限定在工作目录内的这个子目录里找，可选"),
		"limit":   toolIntParam("最多返回多少条，默认 " + fmt.Sprint(defaultFindLimit)),
	})
}

func (t *FindFilesTool) Run(ctx context.Context, input map[string]any) (string, error) {
	pattern := strings.TrimSpace(stringFromInput(input, "pattern"))
	if pattern == "" {
		return "", errors.New("pattern is required")
	}
	base, err := safePath(t.root, stringFromInput(input, "path"))
	if err != nil {
		return "", err
	}
	limit := intFromInput(input, "limit", defaultFindLimit)
	if limit <= 0 || limit > maxFindLimit {
		limit = defaultFindLimit
	}
	var (
		found     []string
		truncated bool
	)
	err = walkAgentFiles(ctx, base, func(_, rel string, _ fs.FileInfo) error {
		if len(found) >= limit {
			truncated = true
			return fs.SkipAll
		}
		if matchGlobPath(pattern, rel) {
			found = append(found, rel)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(found) == 0 {
		return "没有匹配的文件", nil
	}
	header := fmt.Sprintf("%d 个文件", len(found))
	if truncated {
		header += fmt.Sprintf("（已达上限 %d，还有更多）", limit)
	}
	return header + "\n" + strings.Join(found, "\n"), nil
}

// ---- 共用 ------------------------------------------------------------------

// walkAgentFiles 遍历工作目录下的普通文件，回调拿到绝对路径和相对路径。
//
// 跳过 .git：它体积大、内容是二进制打包对象，检索它既慢又没有意义。
// 条目总数封顶，工作目录被塞满时不至于让一次工具调用一直走下去。
func walkAgentFiles(ctx context.Context, base string, visit func(fullPath, rel string, info fs.FileInfo) error) error {
	seen := 0
	return filepath.WalkDir(base, func(fullPath string, entry fs.DirEntry, err error) error {
		if err != nil {
			// 单个目录读不了就跳过它，不要中断整次遍历。
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if seen++; seen > maxWalkEntries {
			return fs.SkipAll
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return nil
		}
		rel, relErr := filepath.Rel(base, fullPath)
		if relErr != nil {
			return nil
		}
		return visit(fullPath, filepath.ToSlash(rel), info)
	})
}

// matchGlobPath 匹配通配符。不含 / 的模式只比对文件名——「找 *.go」几乎总是这个意思；
// 含 / 的比对相对路径，** 跨任意层目录。
func matchGlobPath(pattern, rel string) bool {
	pattern = filepath.ToSlash(strings.TrimSpace(pattern))
	if pattern == "" {
		return false
	}
	if !strings.Contains(pattern, "/") {
		ok, err := path.Match(pattern, path.Base(rel))
		return err == nil && ok
	}
	return matchGlobSegments(strings.Split(pattern, "/"), strings.Split(rel, "/"))
}

// matchGlobSegments 逐段匹配，** 可以吃掉任意多段（包括零段）。
func matchGlobSegments(pattern, segments []string) bool {
	switch {
	case len(pattern) == 0:
		return len(segments) == 0
	case pattern[0] == "**":
		for i := 0; i <= len(segments); i++ {
			if matchGlobSegments(pattern[1:], segments[i:]) {
				return true
			}
		}
		return false
	case len(segments) == 0:
		return false
	default:
		ok, err := path.Match(pattern[0], segments[0])
		if err != nil || !ok {
			return false
		}
		return matchGlobSegments(pattern[1:], segments[1:])
	}
}

// isProbablyBinary 用「有没有 NUL 字节」判断，和 grep 的判据一致：够用，也不会
// 把 UTF-8 文本误判成二进制。
func isProbablyBinary(data []byte) bool {
	if len(data) > 8000 {
		data = data[:8000]
	}
	for _, b := range data {
		if b == 0 {
			return true
		}
	}
	return false
}

// clipRunes 按字符截断，不按字节——按字节截会把一个汉字劈成两半。
func clipRunes(text string, limit int) string {
	text = strings.TrimRight(text, "\r")
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	count := 0
	for i := range text {
		if count == limit {
			return text[:i] + "…"
		}
		count++
	}
	return text
}

func marshalToolResult(result map[string]any) (string, error) {
	body, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}
