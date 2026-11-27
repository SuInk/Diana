// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// ---- write_file ------------------------------------------------------------

// WriteFileTool 在 Agent 工作目录内整体写入一个文件。
type WriteFileTool struct {
	root      string
	maxBytes  int
	protected protectedFiles
	keep      keepScope
}

func (t *WriteFileTool) Name() string { return "write_file" }

// 分区约定（tmp/outputs/keep 各自的保留期）在 runner 的规则段里；二进制会被代码拒绝并
// 指向 save_to_workspace，这里只留一句防止模型写一份文字描述冒充文件。
func (t *WriteFileTool) Description() string {
	return `在工作目录内写文本文件，整体覆盖，父目录自动创建。改其中一段用 edit_file。只能写文本，二进制用 save_to_workspace。`
}

func (t *WriteFileTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"path", "content"}, map[string]any{
		"path":    toolStringParam("工作目录内相对路径；用户指定路径或修改项目文件时沿用目标位置；新产物默认放 " + WorkspaceTmpDir + "/、" + WorkspaceOutputsDir + "/ 或 " + WorkspaceKeepDir + "/"),
		"content": toolStringParam("完整内容"),
	})
}

func (t *WriteFileTool) Run(_ context.Context, input map[string]any) (string, error) {
	rel := stringFromInput(input, "path")
	if rel == "" {
		return "", errors.New("path is required")
	}
	// content 只能是字符串，写进 .png 的永远是一段文本：文件建出来了、工具也说成功了，
	// 可谁都打不开。线上就出过「把图存进工作目录」被存成一份文字描述的事。
	if IsBinaryFileExtension(rel) {
		return "", fmt.Errorf("write_file 只能写文本，%s 是二进制格式（图片、音视频、PDF、压缩包之类），写进去只会是一份打不开的文件；要把聊天里的图片或文件、网址上的文件、MCP 产物存进工作目录，请用 save_to_workspace", rel)
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
	if t.protected.blocked(target) {
		return "", errProtectedFile(rel)
	}
	keepDir, inKeep := "", false
	if clean := relPathForOutput(t.root, target); clean != "." {
		if _, inKeep = keepLocation(clean); inKeep {
			normalized, err := t.keep.normalizeDest(clean)
			if err != nil {
				return "", err
			}
			if target, err = safePath(t.root, normalized); err != nil {
				return "", err
			}
			keepDir = t.keep.botDir
			lock := keepAreaLock(t.root, keepDir)
			lock.Lock()
			defer lock.Unlock()
			var replacing int64
			if info, err := os.Lstat(target); err == nil && info.Mode().IsRegular() {
				replacing = info.Size()
			}
			if err := checkKeepQuota(t.root, keepDir, int64(len(text)), replacing); err != nil {
				return "", err
			}
		}
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
	if inKeep {
		_ = upsertKeepEntry(t.root, KeepEntry{Path: relPathForOutput(t.root, target), SavedBy: t.keep.actor, SavedAt: time.Now(), Size: int64(len(text)), MIME: SniffMediaType([]byte(text))})
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
	root      string
	maxBytes  int
	protected protectedFiles
	keep      keepScope
}

func (t *EditFileTool) Name() string { return "edit_file" }

func (t *EditFileTool) Description() string {
	return `用精确文本替换修改工作目录内的文件。每个 old_text 须唯一命中，否则整批拒绝；各替换按原文件匹配、不能重叠。`
}

func (t *EditFileTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"path", "edits"}, map[string]any{
		"path": toolStringParam("相对路径"),
		"edits": map[string]any{
			"type":        "array",
			"description": "替换列表",
			"items": toolObjectSchema([]string{"old_text", "new_text"}, map[string]any{
				"old_text": toolStringParam("原文片段，须唯一命中"),
				"new_text": toolStringParam("新内容，留空即删除"),
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
	if t.protected.blocked(target) {
		return "", errProtectedFile(rel)
	}
	clean := relPathForOutput(t.root, target)
	if err := t.keep.checkOwned(clean); err != nil {
		return "", err
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", workspaceNotFound(rel, err)
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
	keepDir, inKeep := keepLocation(clean)
	if inKeep {
		lock := keepAreaLock(t.root, keepDir)
		lock.Lock()
		defer lock.Unlock()
		if err := checkKeepQuota(t.root, keepDir, int64(len(updated)), int64(len(original))); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(target, []byte(updated), info.Mode().Perm()); err != nil {
		return "", err
	}
	if inKeep {
		_ = upsertKeepEntry(t.root, KeepEntry{Path: clean, Size: int64(len(updated))})
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
	root      string
	protected protectedFiles
	store     FileStore
}

func (t *GrepTool) Name() string           { return "grep" }
func (t *GrepTool) CodeModeParallel() bool { return true }

func (t *GrepTool) Description() string {
	return `在工作目录内按内容检索文件，返回「路径:行号: 内容」。定位代码或配置比逐个 read_file 快。`
}

func (t *GrepTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"pattern"}, map[string]any{
		"pattern":     toolStringParam("正则；literal=true 时按字面量"),
		"path":        toolStringParam("限定子目录"),
		"glob":        toolStringParam("文件通配符，如 *.go"),
		"ignore_case": toolBoolParam(""),
		"literal":     toolBoolParam("pattern 按字面量"),
		"context":     toolIntParam("匹配前后各带几行"),
		"offset":      toolIntParam("跳过前几条匹配，续读时照结果里给的填"),
		"limit":       toolIntParam("最多条数，可选；不填时装满单次结果上限为止"),
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
	store := toolFileStore(t.store, t.root, t.protected)
	baseRel, base, release, err := searchTree(ctx, store, stringFromInput(input, "path"))
	if err != nil {
		return "", err
	}
	defer release()
	globPattern := strings.TrimSpace(stringFromInput(input, "glob"))
	around := intFromInput(input, "context", 0)
	if around < 0 {
		around = 0
	}
	if around > 5 {
		around = 5
	}
	offset, limit := pageInput(input)
	var (
		entries []string // 第 offset 条起、累计还装得下的匹配，每条带前后文
		used    int
		matches int
		skipped int
	)
	budget := toolBudget(ctx)
	err = walkAgentFiles(ctx, base, func(fullPath, rel string, _ fs.FileInfo) error {
		if store.Blocked(joinStoreRel(baseRel, rel)) {
			return nil
		}
		if globPattern != "" && !matchGlobPath(globPattern, rel) {
			return nil
		}
		// 逐行流式读，不设单文件大小上限：以前超过 2 MB 的文件直接跳过，大日志里的匹配查不到。
		skip, readErr := grepFile(grepScan{path: fullPath, rel: rel, expr: expr, around: around, lineBudget: max(budget/2, 1)}, func() bool {
			matches++
			// 总数照数完，正文只留这一页可能用到的：内存跟着单次上限走，不跟着匹配数走。
			return matches > offset && (limit <= 0 || len(entries) < limit) && used <= budget
		}, func(entry string) {
			// 每条前后文一闭合就记账，超出预算之后的匹配只计数不保留。
			entries = append(entries, entry)
			used += utf8.RuneCountInString(entry) + 1
		})
		if readErr != nil || skip {
			// 读不了的单个文件不该让整次检索失败：权限、竞态删除都很常见。
			skipped++
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	skippedNote := ""
	if skipped > 0 {
		skippedNote = fmt.Sprintf("跳过 %d 个二进制或读不了的文件。", skipped)
	}
	if matches == 0 {
		return "没有匹配。" + skippedNote, nil
	}
	if len(entries) == 0 {
		return fmt.Sprintf("共 %d 条匹配，offset=%d 已经越过最后一条。%s", matches, offset, skippedNote), nil
	}
	out, _, err := fitEntries(len(entries), budget, func(n int) (string, error) {
		header := pageWindowNotice("条匹配", offset+1, offset+n, matches, fmt.Sprintf("用 offset=%d、其他参数不变接着查", offset+n))
		return header + skippedNote + "\n" + strings.Join(entries[:n], "\n"), nil
	})
	return out, err
}

type grepScan struct {
	path, rel  string
	expr       *regexp.Regexp
	around     int
	lineBudget int // 单行最多显示多少字，超出的按字符切开并写明怎么用 read_file 续读
}

// grepFile 逐行扫一个文件。每条匹配先问 want 要不要留；要留的等前后文齐了交给 add，
// 格式是「路径:行号: 内容」，前后文行用「-」。开头像二进制（有 NUL）时返回 skip。
// 超长行流式匹配，只留开头一段：几百 MB 的单行也不会整行读进内存。
func grepFile(scan grepScan, want func() bool, add func(string)) (bool, error) {
	file, err := os.Open(scan.path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 64<<10)
	head, _ := reader.Peek(8000)
	if isProbablyBinary(head) {
		return true, nil
	}
	type block struct {
		lines []string
		after int
	}
	var before []string
	var open []*block
	for lineNo := 1; ; lineNo++ {
		text, lineBytes, matched, readErr := grepLine(reader, scan.expr)
		if lineBytes == 0 {
			if readErr == nil || errors.Is(readErr, io.EOF) {
				break
			}
			return false, readErr
		}
		display := func(marker string) string { return grepDisplayLine(scan, lineNo, marker, text, lineBytes) }
		kept := open[:0]
		for _, b := range open {
			b.lines = append(b.lines, display("-"))
			if b.after--; b.after > 0 {
				kept = append(kept, b)
			} else {
				add(strings.Join(b.lines, "\n"))
			}
		}
		open = kept
		if matched && want() {
			b := &block{lines: append(append([]string(nil), before...), display(":")), after: scan.around}
			if scan.around > 0 {
				open = append(open, b)
			} else {
				add(b.lines[0])
			}
		}
		if scan.around > 0 {
			before = append(before, display("-"))
			if len(before) > scan.around {
				before = before[1:]
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return false, readErr
		}
	}
	for _, b := range open {
		add(strings.Join(b.lines, "\n"))
	}
	return false, nil
}

// grepLine 读一行，返回去掉行尾的内容（超长行只有开头 64KiB）、整行字节数和是否匹配。
func grepLine(reader *bufio.Reader, expr *regexp.Regexp) (string, int, bool, error) {
	fragment, err := reader.ReadSlice('\n')
	if !errors.Is(err, bufio.ErrBufferFull) {
		text := strings.TrimRight(strings.TrimSuffix(string(fragment), "\n"), "\r")
		return text, len(fragment), len(fragment) > 0 && expr.MatchString(text), err
	}
	head := append([]byte(nil), fragment...)
	rest := &restOfLine{reader: reader}
	matched := expr.MatchReader(bufio.NewReader(io.MultiReader(bytes.NewReader(head), &trimNewline{rest: rest})))
	_, _ = io.Copy(io.Discard, rest)
	return string(head), len(head) + rest.n, matched, rest.err
}

// grepDisplayLine 排一行结果；超过 lineBudget 字的按字符切开，写明整行多长、怎么用
// read_file 的 char_offset 接着读。
func grepDisplayLine(scan grepScan, lineNo int, marker, text string, lineBytes int) string {
	prefix := fmt.Sprintf("%s:%d%s ", scan.rel, lineNo, marker)
	// 超长行只读了开头一段，lineBytes 比留下的内容长出不止一个行尾。
	whole := len(text)+2 >= lineBytes
	if whole && utf8.RuneCountInString(text) <= scan.lineBudget {
		return prefix + text
	}
	cut := fitLeadingLines(text, scan.lineBudget, false)
	shown := utf8.RuneCountInString(text[:cut])
	return fmt.Sprintf("%s%s…[这一行共 %d 字节，只给了前 %d 字；用 read_file path=%s offset=%d char_offset=%d 接着读]", prefix, text[:cut], lineBytes, shown, scan.rel, lineNo, shown)
}

// restOfLine 把 reader 里当前这一行剩下的部分（含换行）当成一个 Reader，读到换行或文件末尾为止。
type restOfLine struct {
	reader  *bufio.Reader
	pending []byte
	done    bool
	n       int
	err     error
}

func (r *restOfLine) Read(p []byte) (int, error) {
	for len(r.pending) == 0 {
		if r.done {
			return 0, io.EOF
		}
		fragment, err := r.reader.ReadSlice('\n')
		r.pending, r.n = fragment, r.n+len(fragment)
		if !errors.Is(err, bufio.ErrBufferFull) {
			r.done = true
			if err != nil {
				r.err = err
			}
		}
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

// trimNewline 去掉行尾的换行再交给正则，和短行用 MatchString 时的口径一致。
type trimNewline struct {
	rest *restOfLine
}

func (t *trimNewline) Read(p []byte) (int, error) {
	n, err := t.rest.Read(p)
	if n > 0 && p[n-1] == '\n' && t.rest.done && len(t.rest.pending) == 0 {
		n--
		if n > 0 && p[n-1] == '\r' {
			n--
		}
	}
	return n, err
}

// pageInput 读出 offset/limit：offset 从 0 起，limit ≤0 表示不限条数、装满单次上限为止。
func pageInput(input map[string]any) (int, int) {
	return max(intFromInput(input, "offset", 0), 0), max(intFromInput(input, "limit", 0), 0)
}

// ---- find_files ------------------------------------------------------------

// FindFilesTool 在工作目录内按名字/通配符找文件。
type FindFilesTool struct {
	root      string
	protected protectedFiles
	store     FileStore
}

func (t *FindFilesTool) Name() string           { return "find_files" }
func (t *FindFilesTool) CodeModeParallel() bool { return true }

func (t *FindFilesTool) Description() string {
	return `在工作目录内按通配符找文件，返回相对路径。`
}

func (t *FindFilesTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"pattern"}, map[string]any{
		"pattern": toolStringParam("不含 / 只比文件名，含 / 比相对路径，** 跨目录"),
		"path":    toolStringParam("限定子目录"),
		"offset":  toolIntParam("跳过前几个，续读时照结果里给的填"),
		"limit":   toolIntParam("最多条数，可选；不填时装满单次结果上限为止"),
	})
}

func (t *FindFilesTool) Run(ctx context.Context, input map[string]any) (string, error) {
	pattern := strings.TrimSpace(stringFromInput(input, "pattern"))
	if pattern == "" {
		return "", errors.New("pattern is required")
	}
	store := toolFileStore(t.store, t.root, t.protected)
	baseRel, base, release, err := searchTree(ctx, store, stringFromInput(input, "path"))
	if err != nil {
		return "", err
	}
	defer release()
	offset, limit := pageInput(input)
	var (
		found []string
		used  int
		total int
	)
	budget := toolBudget(ctx)
	err = walkAgentFiles(ctx, base, func(fullPath, rel string, _ fs.FileInfo) error {
		if store.Blocked(joinStoreRel(baseRel, rel)) || !matchGlobPath(pattern, rel) {
			return nil
		}
		total++
		if total > offset && (limit <= 0 || len(found) < limit) && used <= budget {
			found = append(found, rel)
			used += utf8.RuneCountInString(rel) + 1
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if total == 0 {
		return "没有匹配的文件", nil
	}
	if len(found) == 0 {
		return fmt.Sprintf("共 %d 个文件，offset=%d 已经越过最后一个。", total, offset), nil
	}
	out, _, err := fitEntries(len(found), budget, func(n int) (string, error) {
		header := pageWindowNotice("个文件", offset+1, offset+n, total, fmt.Sprintf("用 offset=%d、其他参数不变接着找", offset+n))
		return header + "\n" + strings.Join(found[:n], "\n"), nil
	})
	return out, err
}

// ---- 共用 ------------------------------------------------------------------

// searchTree 解析 grep / find_files 的起点目录，返回它在工作区里的相对路径，以及一个可以
// 在宿主机上遍历的目录（容器后端是拷出来的临时副本，用完调 release）。起点不存在时
// WalkDir 会静默返回空结果，模型读到「没有匹配」会以为文件真不存在，所以这里先把
// 「目录写错了」单独报出来。
func searchTree(ctx context.Context, store FileStore, raw string) (string, string, func(), error) {
	rel, err := store.Resolve(raw)
	if err != nil {
		return "", "", nil, err
	}
	if _, err := store.Stat(ctx, rel); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", "", nil, fmt.Errorf("工作目录里没有 %s 这个目录（%w）；省略 path 就从工作目录根开始查", strings.TrimSpace(raw), fs.ErrNotExist)
		}
		return "", "", nil, err
	}
	dir, release, err := store.LocalTree(ctx, rel)
	if err != nil {
		return "", "", nil, err
	}
	return rel, dir, release, nil
}

// walkAgentFiles 遍历工作目录下的普通文件，回调拿到绝对路径和相对路径。
//
// 跳过 .git：它体积大、内容是二进制打包对象，检索它既慢又没有意义。不设条目上限：
// 以前走到 2 万条就静默停下，后面的文件模型永远查不到；耗时由工具超时兜住。
func walkAgentFiles(ctx context.Context, base string, visit func(fullPath, rel string, info fs.FileInfo) error) error {
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

func marshalToolResult(result map[string]any) (string, error) {
	body, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}
