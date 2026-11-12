// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 续接记录落盘：每个会话一个 gzip 压缩的 JSON 文件，放在数据库同目录的
// append-context/ 下。进程重启（升级、崩溃、机器重启）后，这个会话第一次回复时读回
// 上一次实际发出的请求接着续，不再整份重新组装。
//
// 读回的前缀必须和重启前发出去的逐字一样，否则供应商缓存从分叉处作废。llm.Message
// 里推理续接状态（ResponsesOutput、ReasoningContent、AnthropicThinking）、缓存断点和
// 预算优先级平时不进 JSON，这里用单独的结构把它们一并存下。
//
// 只在 APP_DB_PATH 有值时落盘：正式进程启动时一定会设（cmd/webui），测试大多不设，
// 不能往开发机的用户缓存目录里写，也不能读到上一次跑测试留下的记录。

const (
	appendContextFileVersion = 1
	// appendContextFileMaxAge 之前的记录不读回：隔了这么久供应商缓存早就过期，群里
	// 也聊了很多，接着续只会让请求一下子很长。
	appendContextFileMaxAge = 24 * time.Hour
)

type appendContextDisk struct {
	mu sync.Mutex
	// pending 是每个会话还没写下去的最新一份；写的协程只写最新的，中间版本跳过。
	pending map[string]*appendContextLog
	writing map[string]bool
	cleaned bool
}

type appendContextFile struct {
	Version  int                    `json:"version"`
	Key      string                 `json:"key"`
	SavedAt  time.Time              `json:"saved_at"`
	RunKey   string                 `json:"run_key"`
	HeadKey  string                 `json:"head_key"`
	Overhead int64                  `json:"overhead"`
	Model    string                 `json:"model,omitempty"`
	Prefix   []appendContextMessage `json:"prefix"`
}

type appendContextMessage struct {
	ContinuationScope string              `json:"continuation_scope,omitempty"`
	AnthropicThinking []json.RawMessage   `json:"anthropic_thinking,omitempty"`
	ReasoningContent  *string             `json:"reasoning_content,omitempty"`
	Role              llm.Role            `json:"role"`
	Content           string              `json:"content"`
	Parts             []llm.ContentPart   `json:"parts,omitempty"`
	ToolCalls         []appendContextCall `json:"tool_calls,omitempty"`
	ToolCallID        string              `json:"tool_call_id,omitempty"`
	ToolName          string              `json:"tool_name,omitempty"`
	ToolError         bool                `json:"tool_error,omitempty"`
	ResponsesOutput   []json.RawMessage   `json:"responses_output,omitempty"`
	Priority          llm.MessagePriority `json:"priority,omitempty"`
	ContextGroup      string              `json:"context_group,omitempty"`
	CacheBreakpoint   bool                `json:"cache_breakpoint,omitempty"`
	AtomicText        bool                `json:"atomic_text,omitempty"`
}

type appendContextCall struct {
	ThoughtSignature []byte         `json:"thought_signature,omitempty"`
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	Arguments        map[string]any `json:"arguments,omitempty"`
}

// appendContextPersistDisabled 让测试整包关掉落盘：测试入口给所有用例设了同一个
// APP_DB_PATH，不同用例用同一个会话键时会读到别人留下的记录。专门测落盘的用例再打开。
var appendContextPersistDisabled atomic.Bool

func appendContextDir() string {
	if appendContextPersistDisabled.Load() {
		return ""
	}
	dbPath := strings.TrimSpace(os.Getenv("APP_DB_PATH"))
	if dbPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(dbPath), "append-context")
}

func appendContextFilePath(dir, key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(dir, hex.EncodeToString(sum[:16])+".json.gz")
}

// save 把这个会话的最新记录交给后台写下去，log 为 nil 时删掉文件。不阻塞回复。
func (d *appendContextDisk) save(key string, record *appendContextLog) {
	dir := appendContextDir()
	if dir == "" {
		return
	}
	d.mu.Lock()
	if d.pending == nil {
		d.pending, d.writing = map[string]*appendContextLog{}, map[string]bool{}
	}
	d.pending[key] = record
	if d.writing[key] {
		d.mu.Unlock()
		return
	}
	d.writing[key] = true
	d.mu.Unlock()
	go func() {
		defer recoverGoroutinePanic("append_context_persist")
		d.flush(dir, key)
	}()
}

func (d *appendContextDisk) flush(dir, key string) {
	// 正常退出时 writing 在查到「没有待写」的同一把锁里复位，save 不会漏掉这期间来的
	// 新记录；中途 panic 时在这里补复位，否则这个会话之后再也不会落盘。
	finished := false
	defer func() {
		if !finished {
			d.mu.Lock()
			d.writing[key] = false
			d.mu.Unlock()
		}
	}()
	for {
		d.mu.Lock()
		record, ok := d.pending[key]
		if !ok {
			d.writing[key] = false
			finished = true
			d.mu.Unlock()
			return
		}
		delete(d.pending, key)
		d.mu.Unlock()
		if err := writeAppendContextFile(dir, key, record); err != nil {
			log.Printf("diana append context persist failed: %v", err)
		}
	}
}

// waitIdle 等后台写完，测试用。
func (d *appendContextDisk) waitIdle() {
	for {
		d.mu.Lock()
		busy := false
		for _, writing := range d.writing {
			busy = busy || writing
		}
		d.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func writeAppendContextFile(dir, key string, record *appendContextLog) error {
	path := appendContextFilePath(dir, key)
	if record == nil {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	file := appendContextFile{Version: appendContextFileVersion, Key: key, SavedAt: time.Now().UTC(), RunKey: record.key,
		HeadKey: record.headKey, Overhead: record.overhead, Model: record.model, Prefix: make([]appendContextMessage, len(record.prefix))}
	for index, message := range record.prefix {
		file.Prefix[index] = appendContextMessageFrom(message)
	}
	var buffer bytes.Buffer
	zw := gzip.NewWriter(&buffer)
	if err := json.NewEncoder(zw).Encode(file); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".append-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(buffer.Bytes()); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// load 读回这个会话落盘的记录。没有、读不出、版本不对、太旧，都当作没有。
func (d *appendContextDisk) load(key string) *appendContextLog {
	dir := appendContextDir()
	if dir == "" {
		return nil
	}
	d.cleanOnce(dir)
	file, err := os.Open(appendContextFilePath(dir, key))
	if err != nil {
		return nil
	}
	defer file.Close()
	zr, err := gzip.NewReader(file)
	if err != nil {
		return nil
	}
	defer zr.Close()
	var stored appendContextFile
	if err := json.NewDecoder(io.LimitReader(zr, 64<<20)).Decode(&stored); err != nil {
		log.Printf("diana append context load failed: %v", err)
		return nil
	}
	if stored.Version != appendContextFileVersion || stored.Key != key || len(stored.Prefix) == 0 || stored.RunKey == "" ||
		time.Since(stored.SavedAt) > appendContextFileMaxAge {
		return nil
	}
	record := &appendContextLog{key: stored.RunKey, headKey: stored.HeadKey, overhead: stored.Overhead, model: stored.Model, sentAt: stored.SavedAt, prefix: make([]llm.Message, len(stored.Prefix))}
	for index, message := range stored.Prefix {
		record.prefix[index] = message.toMessage()
	}
	record.index()
	return record
}

// cleanOnce 进程里第一次读记录时删掉过期的文件：会话没再说话，文件不会被覆盖。
func (d *appendContextDisk) cleanOnce(dir string) {
	d.mu.Lock()
	if d.cleaned {
		d.mu.Unlock()
		return
	}
	d.cleaned = true
	d.mu.Unlock()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || entry.IsDir() {
			continue
		}
		if time.Since(info.ModTime()) > appendContextFileMaxAge {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}

func appendContextMessageFrom(message llm.Message) appendContextMessage {
	stored := appendContextMessage{
		ContinuationScope: message.ContinuationScope, AnthropicThinking: message.AnthropicThinking, ReasoningContent: message.ReasoningContent,
		Role: message.Role, Content: message.Content, Parts: message.Parts, ToolCallID: message.ToolCallID, ToolName: message.ToolName,
		ToolError: message.ToolError, ResponsesOutput: message.ResponsesOutput, Priority: message.Priority, ContextGroup: message.ContextGroup,
		CacheBreakpoint: message.CacheBreakpoint, AtomicText: message.AtomicText,
	}
	for _, call := range message.ToolCalls {
		stored.ToolCalls = append(stored.ToolCalls, appendContextCall{ThoughtSignature: call.ThoughtSignature, ID: call.ID, Name: call.Name, Arguments: call.Arguments})
	}
	return stored
}

func (stored appendContextMessage) toMessage() llm.Message {
	message := llm.Message{
		ContinuationScope: stored.ContinuationScope, AnthropicThinking: stored.AnthropicThinking, ReasoningContent: stored.ReasoningContent,
		Role: stored.Role, Content: stored.Content, Parts: stored.Parts, ToolCallID: stored.ToolCallID, ToolName: stored.ToolName,
		ToolError: stored.ToolError, ResponsesOutput: stored.ResponsesOutput, Priority: stored.Priority, ContextGroup: stored.ContextGroup,
		CacheBreakpoint: stored.CacheBreakpoint, AtomicText: stored.AtomicText,
	}
	for _, call := range stored.ToolCalls {
		message.ToolCalls = append(message.ToolCalls, llm.ToolCall{ThoughtSignature: call.ThoughtSignature, ID: call.ID, Name: call.Name, Arguments: call.Arguments})
	}
	return message
}
