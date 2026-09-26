// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"os"
	"path/filepath"
	"strings"
	"time"
)

// 调试轨迹每轮都带着完整的模型请求，一条几十到几百 KB，调试模式开着时一周能攒
// 一两个 GB。它只按事件整段读、按天整批过期，从来不需要 SQL 查询，放在库里只会
// 撑大库文件（删掉的页会复用，但文件不会缩小），拖慢备份和损坏后的恢复。
// 所以存成数据库旁边的文件：debug-traces/<UTC 日期>/<message_id 哈希>.jsonl.gz，
// 每条记录是一个独立的 gzip 成员，追加写不用重写整个文件；过期就删整天的目录。
const (
	debugTraceDirName = "debug-traces"
	debugTraceAction  = "debug_trace"
	debugTraceDayForm = "2006-01-02"
)

// debugTraceDir 返回调试轨迹文件的根目录。内存库和 file: URI 没有旁边的目录，
// 这时返回空字符串，调试轨迹照旧写进 app_logs。
func (s *SQLiteStore) debugTraceDir() string {
	if s == nil || s.path == "" || s.path == ":memory:" || strings.HasPrefix(s.path, "file:") {
		return ""
	}
	return filepath.Join(filepath.Dir(s.path), debugTraceDirName)
}

func storesDebugTraceInFile(entry AppLogEntry) bool {
	return entry.Kind == LogKindDebug && entry.Action == debugTraceAction && strings.TrimSpace(entry.Target) != ""
}

func debugTraceFileName(target string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(target)))
	return hex.EncodeToString(sum[:16]) + ".jsonl.gz"
}

func (s *SQLiteStore) appendDebugTraceFile(root string, entry AppLogEntry) error {
	line, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	var member bytes.Buffer
	writer := gzip.NewWriter(&member)
	if _, err := writer.Write(append(line, '\n')); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	dir := filepath.Join(root, entry.CreatedAt.UTC().Format(debugTraceDayForm))
	// 里面是完整的模型上下文，和数据库一样只给本用户读。
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	s.debugTraceMu.Lock()
	defer s.debugTraceMu.Unlock()
	file, err := os.OpenFile(filepath.Join(dir, debugTraceFileName(entry.Target)), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(member.Bytes()); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// readDebugTraceFiles 读出某条消息在各天目录下的全部调试记录。一轮处理可能跨过
// UTC 零点，所以每个还没过期的日期目录都看一眼，目录只有保留天数那么多个。
func (s *SQLiteStore) readDebugTraceFiles(target string) ([]AppLogEntry, error) {
	root := s.debugTraceDir()
	if root == "" || strings.TrimSpace(target) == "" {
		return nil, nil
	}
	days, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list debug trace days: %w", err)
	}
	name := debugTraceFileName(target)
	var entries []AppLogEntry
	for _, day := range days {
		if !day.IsDir() {
			continue
		}
		dayEntries, err := readDebugTraceFile(filepath.Join(root, day.Name(), name))
		if err != nil {
			return nil, err
		}
		entries = append(entries, dayEntries...)
	}
	return entries, nil
}

func readDebugTraceFile(path string) ([]AppLogEntry, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open debug trace: %w", err)
	}
	defer func() { _ = file.Close() }()
	decompressed, err := gzip.NewReader(file)
	if err != nil {
		// 进程在写第一条时被杀，文件里只有半个 gzip 头，当作没有记录。
		return nil, nil
	}
	defer func() { _ = decompressed.Close() }()
	reader := bufio.NewReader(decompressed)
	var entries []AppLogEntry
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			var entry AppLogEntry
			if json.Unmarshal(line, &entry) == nil {
				entries = append(entries, entry)
			}
		}
		// 除了正常的 EOF，写到一半被打断的尾巴也会让解压报错；前面完整的记录照样返回。
		if err != nil {
			return entries, nil
		}
	}
}

// PruneDebugTraceFiles 删掉整天都早于 before 的调试轨迹目录，返回删掉的天数。
// before 为零值表示不清理。
func (s *SQLiteStore) PruneDebugTraceFiles(before time.Time) (int, error) {
	root := s.debugTraceDir()
	if root == "" || before.IsZero() {
		return 0, nil
	}
	days, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	cutoff := before.UTC()
	deleted := 0
	for _, day := range days {
		if !day.IsDir() {
			continue
		}
		start, err := time.Parse(debugTraceDayForm, day.Name())
		if err != nil {
			continue
		}
		// 目录里最晚的记录也早于截止时间，才整目录删。
		if start.AddDate(0, 0, 1).After(cutoff) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, day.Name())); err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}
