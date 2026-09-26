// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 调试轨迹每轮都带着完整的模型请求，一条几十到几百 KB，调试模式开着时一周能攒
// 一两个 GB。它只按事件整段读、按天整批过期，从来不需要 SQL 查询，放在库里只会
// 撑大库文件（删掉的页会复用，但文件不会缩小），拖慢备份和损坏后的恢复。
//
// 所以存成数据库旁边的普通文件，排查时直接打开就能看：
//
//	debug-traces/<UTC 日期>/<group-群号 | private-QQ号>/<message_id>/<序号>-<步骤>.json
//
// 每一步一个缩进排好的 JSON。当天的保持明文，之后每日维护压成 .json.gz（见
// CompressDebugTraceFiles）；过期就删整天的目录。
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

// debugTraceMessageDir 是某条消息的轨迹在某一天目录下的相对路径。
func debugTraceMessageDir(groupID, userID, messageID string) string {
	chat := "other"
	if groupID = strings.TrimSpace(groupID); groupID != "" {
		chat = "group-" + groupID
	} else if userID = strings.TrimSpace(userID); userID != "" {
		chat = "private-" + userID
	}
	return filepath.Join(debugTracePathPart(chat), debugTracePathPart(messageID))
}

// debugTracePathPart 把 ID 变成安全的单级目录名。QQ 的 ID 都是数字，原样保留；
// 其他平台的 ID 可能带 / 或 :，替换掉。替换后撞名也没关系，读取时还会按事件
// 元数据再筛一遍。
func debugTracePathPart(value string) string {
	var builder strings.Builder
	for _, r := range strings.TrimSpace(value) {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '-', r == '_', r == '.':
			builder.WriteRune(r)
		default:
			builder.WriteByte('_')
		}
		if builder.Len() >= 96 {
			break
		}
	}
	name := strings.Trim(builder.String(), ".")
	if name == "" {
		return "_"
	}
	return name
}

// debugTraceStepName 让文件名本身说明这一步做了什么：模型请求写用途（接话评分、
// 回复……），Agent 事件写阶段。
func debugTraceStepName(entry AppLogEntry) string {
	label := debugMetadataString(entry.Metadata, "phase")
	if label == "model_request" {
		if purpose := debugMetadataString(entry.Metadata, "purpose"); purpose != "" {
			label = purpose
		}
	}
	if label == "" {
		label = entry.Action
	}
	sequence := 0
	switch value := entry.Metadata["sequence"].(type) {
	case int64:
		sequence = int(value)
	case int:
		sequence = value
	case float64:
		sequence = int(value)
	}
	return fmt.Sprintf("%03d-%s", sequence, debugTracePathPart(label))
}

func (s *SQLiteStore) writeDebugTraceFile(root string, entry AppLogEntry) error {
	var body bytes.Buffer
	encoder := json.NewEncoder(&body)
	// 提示词里的 < > & 原样保留，不转成 <，方便人读。
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(entry); err != nil {
		return err
	}
	dir := filepath.Join(root, entry.CreatedAt.UTC().Format(debugTraceDayForm), debugTraceMessageDir(
		debugMetadataString(entry.Metadata, "group_id"),
		debugMetadataString(entry.Metadata, "user_id"),
		entry.Target,
	))
	// 里面是完整的模型上下文，和数据库一样只给本用户读。
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	s.markDebugTraceSince(root, entry.CreatedAt)
	step := debugTraceStepName(entry)
	// 同一条消息被两个机器人处理时序号会重复，排他创建，撞了就加后缀。
	for attempt := 1; ; attempt++ {
		name := step + ".json"
		if attempt > 1 {
			name = fmt.Sprintf("%s-%d.json", step, attempt)
		}
		file, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errors.Is(err, os.ErrExist) && attempt < 100 {
			continue
		}
		if err != nil {
			return err
		}
		if _, err := file.Write(body.Bytes()); err != nil {
			_ = file.Close()
			return err
		}
		return file.Close()
	}
}

// readDebugTraceFiles 读出某条消息在各天目录下的全部调试记录。一轮处理可能跨过
// UTC 零点，所以每个还没过期的日期目录都看一眼，目录只有保留天数那么多个。
func (s *SQLiteStore) readDebugTraceFiles(groupID, userID, messageID string) ([]AppLogEntry, error) {
	root := s.debugTraceDir()
	if root == "" || strings.TrimSpace(messageID) == "" {
		return nil, nil
	}
	days, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list debug trace days: %w", err)
	}
	messageDir := debugTraceMessageDir(groupID, userID, messageID)
	var entries []AppLogEntry
	for _, day := range days {
		if !day.IsDir() {
			continue
		}
		dir := filepath.Join(root, day.Name(), messageDir)
		files, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("list debug trace: %w", err)
		}
		sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })
		for _, file := range files {
			name := file.Name()
			if file.IsDir() || !(strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".json.gz")) {
				continue
			}
			data, err := readDebugTraceStep(filepath.Join(dir, name))
			if err != nil {
				return nil, fmt.Errorf("read debug trace: %w", err)
			}
			var entry AppLogEntry
			// 进程在写的时候被杀会留下半个文件，跳过它，其余步骤照常显示。
			if json.Unmarshal(data, &entry) == nil {
				entries = append(entries, entry)
			}
		}
	}
	return entries, nil
}

func readDebugTraceStep(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil || !strings.HasSuffix(path, ".gz") {
		return data, err
	}
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		// 压缩到一半被打断的残片，当作坏文件跳过，原文件还在。
		return nil, nil
	}
	defer func() { _ = reader.Close() }()
	data, err = io.ReadAll(reader)
	if err != nil {
		return nil, nil
	}
	return data, nil
}

// CompressDebugTraceFiles 把今天（UTC）以前的轨迹文件压成 .json.gz，返回压缩的文件数。
// 做法和 logrotate 的 delaycompress 一样：排查最常看的是当天，当天保持明文直接打开；
// 过了当天就只剩偶尔翻查，压缩后约为原来的三分之一，用 gzcat / zless 照样能读。
func (s *SQLiteStore) CompressDebugTraceFiles(ctx context.Context, now time.Time) (int, error) {
	root := s.debugTraceDir()
	if root == "" {
		return 0, nil
	}
	days, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	today := now.UTC().Format(debugTraceDayForm)
	compressed := 0
	for _, day := range days {
		if !day.IsDir() || day.Name() >= today {
			continue
		}
		if _, err := time.Parse(debugTraceDayForm, day.Name()); err != nil {
			continue
		}
		err := filepath.WalkDir(filepath.Join(root, day.Name()), func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".json") {
				return nil
			}
			if err := gzipDebugTraceStep(path); err != nil {
				return err
			}
			compressed++
			return nil
		})
		if err != nil {
			return compressed, err
		}
	}
	return compressed, nil
}

// gzipDebugTraceStep 先写临时文件再改名，最后删原文件：中途被打断时要么原文件
// 还在，要么压缩件已经完整，不会两个都读不出来。
func gzipDebugTraceStep(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var body bytes.Buffer
	writer := gzip.NewWriter(&body)
	if _, err := writer.Write(data); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	tmp := path + ".gz.tmp"
	if err := os.WriteFile(tmp, body.Bytes(), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path+".gz"); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Remove(path)
}

// PruneDebugTraceFiles 删掉整天都早于 before 的调试轨迹目录，返回删掉的天数。
// before 为零值表示不清理。
func (s *SQLiteStore) PruneDebugTraceFiles(before time.Time) (int, error) {
	if s == nil || before.IsZero() {
		return 0, nil
	}
	// 整天删，所以真正清掉的是截止时间所在那天零点之前的记录；事件页据此说明
	// 「这条早于某时，已按保留期清理」。库里的旧调试日志用同一个截止时间清理。
	day := before.UTC().Truncate(24 * time.Hour)
	s.debugTracePruned.Store(day.UnixNano())
	root := s.debugTraceDir()
	if root == "" {
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

func (s *SQLiteStore) debugTracePrunedBefore() time.Time {
	if s == nil {
		return time.Time{}
	}
	if value := s.debugTracePruned.Load(); value > 0 {
		return time.Unix(0, value)
	}
	return time.Time{}
}

// .since 记下这个数据目录第一次按新格式写调试轨迹的时间。在它之前处理的事件
// 没有「收到消息」这一步，分不清当时是调试模式关着还是没调模型。
const debugTraceSinceFile = ".since"

func (s *SQLiteStore) markDebugTraceSince(root string, at time.Time) {
	s.debugTraceSinceOnce.Do(func() {
		path := filepath.Join(root, debugTraceSinceFile)
		if _, err := os.Stat(path); err == nil {
			return
		}
		_ = os.WriteFile(path, []byte(at.UTC().Format(time.RFC3339Nano)+"\n"), 0o600)
	})
}

func (s *SQLiteStore) debugTraceSince() time.Time {
	root := s.debugTraceDir()
	if root == "" {
		return time.Time{}
	}
	data, err := os.ReadFile(filepath.Join(root, debugTraceSinceFile))
	if err != nil {
		return time.Time{}
	}
	since, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(data)))
	if err != nil {
		return time.Time{}
	}
	return since
}
