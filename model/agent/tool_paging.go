// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// 工具结果照 Pi 的做法装进单次上限（ToolOutputBudget）：条数不设硬上限，装不下时按
// 整条、整行切，并在结果里写明给了哪一段、一共多少、怎么接着读。切在哪由工具自己
// 决定，不交给 Runner 的兜底截断——那一刀落在 JSON 中间，续读参数常常排在被截掉的
// 那一半里。

// toolBudget 返回这次调用的结果字数上限，不在 Runner 里调用时按 ToolOutputMaxChars。
func toolBudget(ctx context.Context) int {
	if budget := ToolOutputBudget(ctx); budget > 0 {
		return budget
	}
	return ToolOutputMaxChars
}

// ToolResultBudget 同 toolBudget，给 assistant 包里的工具用。
func ToolResultBudget(ctx context.Context) int { return toolBudget(ctx) }

// textCost 是一个字符在结果里占几个字。jsonField 为 true 时按 encoding/json 的默认
// 转义算（引号、反斜杠、换行占 2，控制字符、<>&、U+2028/2029 和非法字节占 6）。
func textCost(r rune, size int, jsonField bool) int {
	if !jsonField {
		return 1
	}
	switch {
	case r == utf8.RuneError && size == 1:
		return 6
	case r == '"' || r == '\\' || r == '\n' || r == '\r' || r == '\t' || r == '\b' || r == '\f':
		return 2
	case r < 0x20 || r == '<' || r == '>' || r == '&' || r == ' ' || r == ' ':
		return 6
	}
	return 1
}

// TextCost 返回 text 在结果里占的字数，规则见 textCost。
func TextCost(text string, jsonField bool) int {
	total := 0
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		total += textCost(r, size, jsonField)
		i += size
	}
	return total
}

// fitLeadingLines 返回 text 开头能装进 budget 字的最长整行前缀的字节数。第一行就装不下
// 时退到按字符切（不切坏 UTF-8），由调用方在续读说明里讲清这一行没给全。
func fitLeadingLines(text string, budget int, jsonField bool) int {
	used, lastLine, i := 0, 0, 0
	for i < len(text) {
		r, size := utf8.DecodeRuneInString(text[i:])
		cost := textCost(r, size, jsonField)
		if used+cost > budget {
			if lastLine > 0 {
				return lastLine
			}
			return i
		}
		used += cost
		i += size
		if r == '\n' {
			lastLine = i
		}
	}
	return len(text)
}

// fitTrailingLines 返回 text 末尾能装进 budget 字的最长整行后缀的起点。最后一行就装不下
// 时退到按字符切，只给这一行的结尾。
func fitTrailingLines(text string, budget int, jsonField bool) int {
	used, lastLine, i := 0, len(text), len(text)
	for i > 0 {
		r, size := utf8.DecodeLastRuneInString(text[:i])
		cost := textCost(r, size, jsonField)
		if used+cost > budget {
			if lastLine < len(text) {
				return lastLine
			}
			return i
		}
		// 换行属于它前面那一行：i 落在换行之后时，后缀正好从一行的开头起。
		if r == '\n' && i < len(text) {
			lastLine = i
		}
		used += cost
		i -= size
	}
	return 0
}

// fitTextResult 让 render 出来的整个结果不超过 budget 字，正文尽量多装整行。
// tail=false 时 render(cut) 给出 text[:cut]，tail=true 时给出 text[cut:]。
// 说明文字随切点变长变短，所以量过实际结果再收紧，最多几轮。
func fitTextResult(budget int, text string, tail, jsonField bool, render func(cut int) (string, error)) (string, error) {
	empty, full := 0, len(text) // 一行不给、整段都给时的切点
	if tail {
		empty, full = len(text), 0
	}
	out, err := render(full)
	if err != nil || utf8.RuneCountInString(out) <= budget {
		return out, err
	}
	base, err := render(empty)
	if err != nil {
		return "", err
	}
	avail := budget - utf8.RuneCountInString(base)
	for range 6 {
		if avail <= 0 {
			return base, nil
		}
		cut := 0
		if tail {
			cut = fitTrailingLines(text, avail, jsonField)
		} else {
			cut = fitLeadingLines(text, avail, jsonField)
		}
		out, err = render(cut)
		if err != nil {
			return "", err
		}
		over := utf8.RuneCountInString(out) - budget
		if over <= 0 {
			return out, nil
		}
		avail -= over
	}
	return base, nil
}

// FitTextResult 同 fitTextResult，给 assistant 包里的工具用。
func FitTextResult(budget int, text string, tail, jsonField bool, render func(cut int) (string, error)) (string, error) {
	return fitTextResult(budget, text, tail, jsonField, render)
}

// FitEntries 同 fitEntries，给 assistant 包里的工具用。
func FitEntries(total, budget int, render func(n int) (string, error)) (string, int, error) {
	return fitEntries(total, budget, render)
}

// fitEntries 找出 render(n) 不超过 budget 的最大 n（1 ≤ n ≤ total）并返回那次结果。
// 一条都装不下时仍给第一条：整条交给模型、由 Runner 兜底截断并说明，比什么都不给强。
func fitEntries(total, budget int, render func(n int) (string, error)) (string, int, error) {
	if total <= 0 {
		out, err := render(0)
		return out, 0, err
	}
	out, err := render(total)
	if err != nil || utf8.RuneCountInString(out) <= budget {
		return out, total, err
	}
	low, high := 1, total-1 // 已知 total 装不下
	best := 1
	for low <= high {
		mid := (low + high) / 2
		body, err := render(mid)
		if err != nil {
			return "", 0, err
		}
		if utf8.RuneCountInString(body) <= budget {
			best = mid
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	out, err = render(best)
	return out, best, err
}

// pageWindowNotice 是分页结果的续读说明：显示第 from–to 条，共 total 条，怎么接着读。
func pageWindowNotice(unit string, from, to, total int, next string) string {
	if to >= total {
		return fmt.Sprintf("显示第 %d–%d %s，共 %d %s，已全部给出。", from, to, unit, total, unit)
	}
	return fmt.Sprintf("显示第 %d–%d %s，共 %d %s（单次结果上限 %d 字）；%s。没读完之前不要断言没有。", from, to, unit, total, unit, ToolOutputMaxChars, next)
}

// ---- 命令输出 --------------------------------------------------------------

// commandOutputPage 是命令输出交给模型的样子。装得下给全文；装不下照 Pi 的 bash 工具
// 给末尾的整行（报错几乎都在尾部），完整输出留在 FullOutputPath，Notice 写明怎么读。
type commandOutputPage struct {
	Output         string
	Truncated      bool
	FullOutputPath string
	Notice         string
}

// commandOutputSpill 决定完整输出存哪：把 src 的内容存好，返回给模型看的路径和读法。
type commandOutputSpill func(src *os.File) (path, howToRead string, err error)

// pageCommandOutput 读 file 里的完整命令输出。overhead 是结果里除输出以外的部分大约
// 占多少字；jsonField 表示输出会作为 JSON 字段值返回。只读文件末尾一段进内存，命令
// 刷出几个 GB 也不会整份读进来。
func pageCommandOutput(file *os.File, budget, overhead int, jsonField bool, spill commandOutputSpill) (commandOutputPage, error) {
	info, err := file.Stat()
	if err != nil {
		return commandOutputPage{}, err
	}
	size := info.Size()
	avail := max(budget-overhead, 0)
	// 每个字至少一个字节、最多四个；读末尾 4*avail 字节足够装满预算。
	window := outputTailWindow(budget, overhead)
	start := max(size-window, 0)
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return commandOutputPage{}, err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return commandOutputPage{}, err
	}
	text := string(data)
	if start == 0 && TextCost(text, jsonField) <= avail {
		return commandOutputPage{Output: strings.ToValidUTF8(text, "�")}, nil
	}
	totalLines, err := countLines(file)
	if err != nil {
		return commandOutputPage{}, err
	}
	path, howToRead, err := spill(file)
	if err != nil {
		return commandOutputPage{}, err
	}
	return pageOutputTail(text, start, size, totalLines, avail, jsonField, path, howToRead), nil
}

// outputTailWindow 是按预算给尾部时最多要读的字节数：每个字最多四个字节。
func outputTailWindow(budget, overhead int) int64 {
	return int64(max(budget-overhead, 0))*utf8.UTFMax + 1
}

// pageOutputTail 把从第 start 字节起的尾部窗口 text 截成能装进 avail 字的整行，并写明
// 一共多少行、给了哪几行、完整输出在 path、怎么读。size、totalLines 是完整输出的。
func pageOutputTail(text string, start, size int64, totalLines, avail int, jsonField bool, path, howToRead string) commandOutputPage {
	if start > 0 {
		// 窗口开头多半落在一行中间，从下一行起算。
		if i := strings.IndexByte(text, '\n'); i >= 0 && i+1 < len(text) {
			text = text[i+1:]
			start += int64(i + 1)
		}
	}
	notice := func(firstLine int, from int64, partial string) string {
		return fmt.Sprintf("输出共 %d 行、%d 字节，超过单次结果上限 %d 字，这里只给了最后第 %d–%d 行（从第 %d 字节起）%s。完整输出已存到 %s，%s。不要把这段当成全部输出。", totalLines, size, ToolOutputMaxChars, firstLine, totalLines, from, partial, path, howToRead)
	}
	// 说明和 full_output_path 也占预算：按最长的写法先量出来，剩下的才给正文。
	reserve := TextCost(notice(totalLines, size, "（第一行只给了结尾）"), jsonField) + TextCost(path, jsonField) + 64
	cut := fitTrailingLines(text, max(avail-reserve, 0), jsonField)
	shown := text[cut:]
	shownLines := strings.Count(shown, "\n")
	if shown != "" && !strings.HasSuffix(shown, "\n") {
		shownLines++
	}
	partial := ""
	if cut > 0 && text[cut-1] != '\n' {
		partial = "（第一行只给了结尾）"
	}
	return commandOutputPage{Output: strings.ToValidUTF8(shown, "�"), Truncated: true, FullOutputPath: path, Notice: notice(totalLines-shownLines+1, start+int64(cut), partial)}
}

// countLines 数文件的行数，结尾没有换行的最后一段也算一行。
func countLines(file *os.File) (int, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	reader := bufio.NewReaderSize(file, 64<<10)
	buf := make([]byte, 64<<10)
	lines, last := 0, byte('\n')
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			lines += bytes.Count(buf[:n], []byte{'\n'})
			last = buf[n-1]
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, err
		}
	}
	if last != '\n' {
		lines++
	}
	return lines, nil
}

// commandOutputRetention 是落盘的完整输出保留多久。
const commandOutputRetention = 24 * time.Hour

// spillCommandOutputTo 把完整输出复制到 dir 下一个新文件，顺手清掉 dir 里过期的旧文件。
// 返回新文件的绝对路径。
func spillCommandOutputTo(dir string, src *os.File) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	pruneCommandOutputs(dir, time.Now())
	var suffix [4]byte
	_, _ = rand.Read(suffix[:])
	// 时间精确到纳秒放在最前面，按文件名排序就是按写入先后排。
	name := time.Now().Format("20060102-150405.000000000") + "-" + hex.EncodeToString(suffix[:]) + ".log"
	target := filepath.Join(dir, name)
	dst, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		_ = dst.Close()
		return "", err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		_ = os.Remove(target)
		return "", err
	}
	return target, dst.Close()
}

func pruneCommandOutputs(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		if info, err := entry.Info(); err == nil && now.Sub(info.ModTime()) > commandOutputRetention {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}

// workspaceCommandOutputDir 是工作区命令（run_command、插件脚本、install_package）的完整
// 输出落点：tmp/ 一天后统一清理，read_file 读得到。
func workspaceCommandOutputDir(root string) string {
	return filepath.Join(root, WorkspaceTmpDir, "command-output")
}

// workspaceSpill 把完整输出存进工作区，告诉模型用 read_file 按行读。
func workspaceSpill(root string) commandOutputSpill {
	return func(src *os.File) (string, string, error) {
		target, err := spillCommandOutputTo(workspaceCommandOutputDir(root), src)
		if err != nil {
			return "", "", err
		}
		rel := relPathForOutput(root, target)
		return rel, fmt.Sprintf("用 read_file path=%s 加 offset/limit 按行读，或用 grep 在里面查", rel), nil
	}
}

// redactCommandOutput 把 src 里的凭据原文（以及它的 JSON 转义形式）换成 [REDACTED]，
// 写进一个新的临时文件。按块流式处理，跨块的凭据也遮得住；先遮再分页，任何一页都不会
// 露出半截凭据。
func redactCommandOutput(src *os.File, secrets []string) (*os.File, error) {
	var variants []string
	longest := 0
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		quoted, _ := json.Marshal(secret)
		encoded := string(quoted[1 : len(quoted)-1])
		for _, value := range []string{secret, encoded} {
			variants = append(variants, value)
			longest = max(longest, len(value))
		}
	}
	sort.Slice(variants, func(i, j int) bool { return len(variants[i]) > len(variants[j]) })
	dst, err := os.CreateTemp("", "diana-agent-command-redacted-*")
	if err != nil {
		return nil, err
	}
	_ = os.Remove(dst.Name()) // 只靠句柄用，进程退出自动回收
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		_ = dst.Close()
		return nil, err
	}
	writer := bufio.NewWriter(dst)
	chunk := make([]byte, 64<<10)
	var pending []byte
	flush := func(final bool) {
		for {
			at, length := -1, 0
			for _, value := range variants {
				if i := bytes.Index(pending, []byte(value)); i >= 0 && (at < 0 || i < at) {
					at, length = i, len(value)
				}
			}
			if at < 0 {
				break
			}
			_, _ = writer.Write(pending[:at])
			_, _ = writer.WriteString("[REDACTED]")
			pending = pending[at+length:]
		}
		keep := 0
		if !final && longest > 1 {
			keep = min(len(pending), longest-1)
		}
		_, _ = writer.Write(pending[:len(pending)-keep])
		pending = append([]byte(nil), pending[len(pending)-keep:]...)
	}
	for {
		n, readErr := src.Read(chunk)
		pending = append(pending, chunk[:n]...)
		if errors.Is(readErr, io.EOF) {
			flush(true)
			break
		}
		if readErr != nil {
			_ = dst.Close()
			return nil, readErr
		}
		flush(false)
	}
	if err := writer.Flush(); err != nil {
		_ = dst.Close()
		return nil, err
	}
	return dst, nil
}
