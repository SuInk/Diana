// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Package runtimeconfig 汇总「启动时就定死」的那层配置：命令行参数、环境变量和
// config.yaml 里的基础设施段，说清每一项现在的值从哪来，并把允许改的项写回
// config.yaml。业务配置（bot / llm 段）以数据库为准，不归这里管。
//
// 写回只改 YAML、不碰运行中的进程：这些值大多在启动时就被各模块读走了，
// 改完需要重启才生效，快照里用 pending 标出「文件已改、尚未生效」的项。
package runtimeconfig

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/SuInk/diana/model/hostinfo"

	"go.yaml.in/yaml/v4"
)

// Kind 是字段的值类型，决定前端用什么输入控件、写回时怎么校验。
type Kind string

const (
	KindString Kind = "string"
	KindInt    Kind = "int"
	KindBool   Kind = "bool"
	KindList   Kind = "list"
	// KindPort 是 1–65535 的端口号，按字符串写回（server.port 在 YAML 里是字符串）。
	KindPort Kind = "port"
)

// Source 说明一项当前生效的值来自哪里。
type Source string

const (
	SourceDefault Source = "default"
	SourceFile    Source = "config_file"
	SourceEnv     Source = "env"
	SourceArg     Source = "arg"
)

// Field 描述 config.yaml 里的一项。Editable 是 WebUI 管理员能不能改，
// BotEditable 是主人在私聊里能不能让机器人改；后者必须是前者的子集，
// 只放改错了也不会让服务连不上、也不涉及安全边界的项。
type Field struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Kind        Kind   `json:"kind"`
	Default     string `json:"default"`
	Hint        string `json:"hint,omitempty"`
	Editable    bool   `json:"editable"`
	BotEditable bool   `json:"bot_editable"`
	Secret      bool   `json:"secret,omitempty"`
	// Risky 标出改错会让控制台连不上或换掉数据位置的项，前端保存前要单独确认。
	Risky bool   `json:"risky,omitempty"`
	Min   *int64 `json:"min,omitempty"`
	Max   *int64 `json:"max,omitempty"`
}

// EnvVar 描述一个会被读取的环境变量。环境变量属于进程，WebUI 和机器人都改不了，
// 只展示有没有设置；Secret 的只报「已设置」，不回显值。
type EnvVar struct {
	Name   string `json:"name"`
	Label  string `json:"label"`
	Secret bool   `json:"secret,omitempty"`
}

// Item 是快照里的一项配置。
type Item struct {
	Field
	Section string `json:"section"`
	// Value 是本次启动时生效的值（未写则为默认值），Secret 项为空。
	Value string `json:"value"`
	// FileValue 是 config.yaml 现在写着的值；和 Value 不同说明改了还没重启。
	FileValue string `json:"file_value"`
	// FileSet 说明文件里写了这一项；没写时 FileValue 是默认值。
	FileSet bool   `json:"file_set"`
	Source  Source `json:"source"`
	Pending bool   `json:"pending"`
}

// EnvItem 是快照里的一个环境变量。
type EnvItem struct {
	EnvVar
	Set   bool   `json:"set"`
	Value string `json:"value,omitempty"`
}

// Snapshot 是返回给 WebUI 和机器人工具的完整视图。
type Snapshot struct {
	ConfigPath    string    `json:"config_path"`
	ConfigExists  bool      `json:"config_exists"`
	Args          []string  `json:"args"`
	Items         []Item    `json:"items"`
	Env           []EnvItem `json:"env"`
	RestartNeeded bool      `json:"restart_needed"`
}

// Manager 持有启动时的配置原文，按需重读文件比对。
type Manager struct {
	mu         sync.Mutex
	path       string
	startup    *yaml.Node
	args       []string
	fields     []Field
	env        []EnvVar
	lookupEnv  func(string) (string, bool)
	fieldByKey map[string]Field
	listenHost string
	port       string
	container  bool
}

// Options 构造 Manager 的参数。Path 为空表示启动时没找到配置文件，
// 第一次写回时落到 CreatePath。
type Options struct {
	Path        string
	CreatePath  string
	StartupData []byte
	Args        []string
	Fields      []Field
	Env         []EnvVar
	LookupEnv   func(string) (string, bool)
	// ListenHost / Port 是本次启动实际监听的地址，网络探测拿它拼访问地址。
	ListenHost string
	Port       string
	Container  bool
}

// New 创建 Manager。StartupData 解析失败时返回错误，调用方应在配置已经能被
// 正常加载之后再调这里，所以正常不会失败。
func New(opts Options) (*Manager, error) {
	startup, err := parseDocument(opts.StartupData)
	if err != nil {
		return nil, err
	}
	path := strings.TrimSpace(opts.Path)
	if path == "" {
		path = strings.TrimSpace(opts.CreatePath)
	}
	lookup := opts.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	byKey := make(map[string]Field, len(opts.Fields))
	for _, field := range opts.Fields {
		if field.BotEditable && !field.Editable {
			return nil, fmt.Errorf("runtimeconfig: %s is bot-editable but not editable", field.Key)
		}
		byKey[field.Key] = field
	}
	return &Manager{
		path:       path,
		startup:    startup,
		args:       append([]string(nil), opts.Args...),
		fields:     append([]Field(nil), opts.Fields...),
		env:        append([]EnvVar(nil), opts.Env...),
		lookupEnv:  lookup,
		fieldByKey: byKey,
		listenHost: opts.ListenHost,
		port:       opts.Port,
		container:  opts.Container,
	}, nil
}

// Snapshot 返回当前视图。
func (m *Manager) Snapshot() (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotLocked()
}

func (m *Manager) snapshotLocked() (Snapshot, error) {
	current, exists, err := m.readFile()
	if err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{ConfigPath: m.path, ConfigExists: exists, Args: redactArgs(m.args)}
	for _, field := range m.fields {
		item := Item{Field: field, Section: sectionOf(field.Key), Source: SourceDefault}
		startValue, startSet := scalarAt(m.startup, field.Key)
		fileValue, fileSet := scalarAt(current, field.Key)
		item.Value = field.Default
		if startSet {
			item.Value = startValue
			item.Source = SourceFile
		}
		item.FileValue = field.Default
		if fileSet {
			item.FileValue = fileValue
			item.FileSet = true
		}
		item.Pending = item.Value != item.FileValue
		if item.Pending {
			snap.RestartNeeded = true
		}
		if field.Secret {
			item.Value, item.FileValue = maskSecret(startSet && startValue != ""), maskSecret(fileSet && fileValue != "")
		}
		snap.Items = append(snap.Items, item)
	}
	for _, env := range m.env {
		value, ok := m.lookupEnv(env.Name)
		item := EnvItem{EnvVar: env, Set: ok && strings.TrimSpace(value) != ""}
		if item.Set && !env.Secret {
			item.Value = value
		}
		snap.Env = append(snap.Env, item)
	}
	return snap, nil
}

// Actor 区分改动来自 WebUI 管理员还是机器人工具，后者只能改 BotEditable 的项。
type Actor int

const (
	ActorAdmin Actor = iota
	ActorBot
)

// ErrNotEditable 表示请求改了一项不允许改的配置。
var ErrNotEditable = errors.New("runtimeconfig: field is not editable")

// Update 把 changes 写回 config.yaml。值用字符串表示：空字符串表示删掉这一项、
// 回到默认值；列表项用换行或逗号分隔。全部校验通过才写，任何一项不合法都不改文件。
func (m *Manager) Update(changes map[string]string, actor Actor) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(changes) == 0 {
		return m.snapshotLocked()
	}
	keys := make([]string, 0, len(changes))
	for key := range changes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	normalized := make(map[string]any, len(changes))
	for _, key := range keys {
		field, ok := m.fieldByKey[key]
		if !ok {
			return Snapshot{}, fmt.Errorf("未知配置项 %s", key)
		}
		if !field.Editable || (actor == ActorBot && !field.BotEditable) {
			return Snapshot{}, fmt.Errorf("%w: %s", ErrNotEditable, key)
		}
		value, err := normalizeValue(field, changes[key])
		if err != nil {
			return Snapshot{}, fmt.Errorf("%s：%w", field.Label, err)
		}
		normalized[key] = value
	}
	doc, _, err := m.readFile()
	if err != nil {
		return Snapshot{}, err
	}
	if doc == nil {
		doc = &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	}
	for _, key := range keys {
		setAt(doc, key, normalized[key])
	}
	if err := m.writeFile(doc); err != nil {
		return Snapshot{}, err
	}
	return m.snapshotLocked()
}

// Network 探测本机地址和监听端口。withPublic 为 true 才向外部服务查公网出口地址。
func (m *Manager) Network(ctx context.Context, withPublic, force bool) (hostinfo.NetworkSnapshot, error) {
	return hostinfo.CollectNetwork(ctx, m.listenHost, m.port, withPublic, force, m.container)
}

// Field 查找字段定义。
func (m *Manager) Field(key string) (Field, bool) {
	field, ok := m.fieldByKey[key]
	return field, ok
}

func (m *Manager) readFile() (*yaml.Node, bool, error) {
	if m.path == "" {
		return nil, false, nil
	}
	data, err := os.ReadFile(m.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("读取 %s：%w", m.path, err)
	}
	doc, err := parseDocument(data)
	if err != nil {
		return nil, true, fmt.Errorf("解析 %s：%w", m.path, err)
	}
	return doc, true, nil
}

// writeFile 先写临时文件再改名，写到一半断电也不会留下半份 YAML。
// 已有文件的权限沿用，新文件用 0600：里面可能有管理员初始密码和密钥。
func (m *Manager) writeFile(doc *yaml.Node) error {
	if m.path == "" {
		return errors.New("没有可写入的配置文件路径")
	}
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(doc); err != nil {
		return err
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if info, err := os.Stat(m.path); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return fmt.Errorf("创建配置目录：%w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(m.path), ".config-*.yaml")
	if err != nil {
		return fmt.Errorf("写入 %s：%w", m.path, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return fmt.Errorf("写入 %s：%w", m.path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("写入 %s：%w", m.path, err)
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, m.path); err != nil {
		return fmt.Errorf("写入 %s：%w", m.path, err)
	}
	return nil
}

func parseDocument(data []byte) (*yaml.Node, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, nil
	}
	return &doc, nil
}

func sectionOf(key string) string {
	section, _, _ := strings.Cut(key, ".")
	return section
}

// lookupNode 按点分路径找节点，找不到返回 nil。
func lookupNode(doc *yaml.Node, key string) *yaml.Node {
	if doc == nil || len(doc.Content) == 0 {
		return nil
	}
	node := doc.Content[0]
	for _, part := range strings.Split(key, ".") {
		if node == nil || node.Kind != yaml.MappingNode {
			return nil
		}
		var next *yaml.Node
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == part {
				next = node.Content[i+1]
				break
			}
		}
		node = next
	}
	return node
}

// scalarAt 把节点读成展示用的字符串；列表用换行拼接。写了 null 或空值算没写。
func scalarAt(doc *yaml.Node, key string) (string, bool) {
	node := lookupNode(doc, key)
	if node == nil {
		return "", false
	}
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Tag == "!!null" {
			return "", false
		}
		return node.Value, true
	case yaml.SequenceNode:
		values := make([]string, 0, len(node.Content))
		for _, child := range node.Content {
			if child.Kind == yaml.ScalarNode && strings.TrimSpace(child.Value) != "" {
				values = append(values, strings.TrimSpace(child.Value))
			}
		}
		return strings.Join(values, "\n"), true
	}
	return "", false
}

// setAt 设置点分路径上的值，value 为 nil 时删掉这个键。中间段不存在就建出来，
// 已有的注释和其它键保持原样。
func setAt(doc *yaml.Node, key string, value any) {
	parts := strings.Split(key, ".")
	node := doc.Content[0]
	if node.Kind != yaml.MappingNode {
		*node = yaml.Node{Kind: yaml.MappingNode}
	}
	for i, part := range parts {
		last := i == len(parts)-1
		index := -1
		for j := 0; j+1 < len(node.Content); j += 2 {
			if node.Content[j].Value == part {
				index = j
				break
			}
		}
		if last {
			if value == nil {
				if index >= 0 {
					node.Content = append(node.Content[:index], node.Content[index+2:]...)
				}
				return
			}
			replacement := valueNode(value)
			if index >= 0 {
				old := node.Content[index+1]
				replacement.LineComment, replacement.HeadComment, replacement.FootComment = old.LineComment, old.HeadComment, old.FootComment
				node.Content[index+1] = replacement
				return
			}
			node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: part}, replacement)
			return
		}
		if index < 0 {
			if value == nil {
				return
			}
			child := &yaml.Node{Kind: yaml.MappingNode}
			node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: part}, child)
			node = child
			continue
		}
		child := node.Content[index+1]
		if child.Kind != yaml.MappingNode {
			if value == nil {
				return
			}
			*child = yaml.Node{Kind: yaml.MappingNode}
		}
		node = child
	}
}

func valueNode(value any) *yaml.Node {
	switch typed := value.(type) {
	case []string:
		seq := &yaml.Node{Kind: yaml.SequenceNode}
		for _, item := range typed {
			seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: item})
		}
		return seq
	case int64:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.FormatInt(typed, 10)}
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(typed)}
	default:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: fmt.Sprint(typed)}
	}
}

// normalizeValue 校验并转换输入；返回 nil 表示删掉这一项。
func normalizeValue(field Field, raw string) (any, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	switch field.Kind {
	case KindInt:
		value, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("需要整数")
		}
		if field.Min != nil && value < *field.Min {
			return nil, fmt.Errorf("不能小于 %d", *field.Min)
		}
		if field.Max != nil && value > *field.Max {
			return nil, fmt.Errorf("不能大于 %d", *field.Max)
		}
		return value, nil
	case KindPort:
		port, err := strconv.Atoi(trimmed)
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("端口需要是 1–65535 的整数")
		}
		return strconv.Itoa(port), nil
	case KindBool:
		value, err := strconv.ParseBool(trimmed)
		if err != nil {
			return nil, fmt.Errorf("需要 true 或 false")
		}
		return value, nil
	case KindList:
		var values []string
		for _, part := range strings.FieldsFunc(trimmed, func(r rune) bool { return r == '\n' || r == ',' }) {
			if part = strings.TrimSpace(part); part != "" {
				values = append(values, part)
			}
		}
		if len(values) == 0 {
			return nil, nil
		}
		return values, nil
	default:
		if strings.ContainsAny(trimmed, "\n\r") {
			return nil, fmt.Errorf("不能包含换行")
		}
		return trimmed, nil
	}
}

func maskSecret(set bool) string {
	if set {
		return "已设置"
	}
	return ""
}

// redactArgs 遮住命令行里形如 --password=xxx / --token xxx 的值。
func redactArgs(args []string) []string {
	out := make([]string, len(args))
	hideNext := false
	for i, arg := range args {
		if hideNext {
			out[i] = "***"
			hideNext = false
			continue
		}
		lower := strings.ToLower(arg)
		sensitive := strings.Contains(lower, "password") || strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "key")
		if name, _, ok := strings.Cut(arg, "="); ok && sensitive {
			out[i] = name + "=***"
			continue
		}
		if sensitive && strings.HasPrefix(arg, "-") {
			hideNext = true
		}
		out[i] = arg
	}
	return out
}

// Int64 是构造 Min/Max 用的小工具。
func Int64(v int64) *int64 { return &v }
