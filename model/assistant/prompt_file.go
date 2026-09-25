// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	yaml "go.yaml.in/yaml/v4"
)

// 内置提示词文件：一台机器人的全部内置提示词写成一份 YAML，导出来改、改完导回去。
//
// 写成 YAML 而不是 JSON，是因为这份文件是给人改的：两百多段提示词大多几百字、
// 带换行，JSON 只能写成一行塞满 \n 的字符串；YAML 的字面块能原样写，还能在每段
// 上面挂分组横幅和标题注释。
//
// 导出时列出登记表里的每一段，改过的写改过的正文，没改过的写默认原文。读回来时
// 宽松：没写的段落按默认值，和默认值一样的不存成覆盖，登记表里已经没有的键跳过并
// 报出来。这样一份从旧版本导出的文件导进新版本不会整份被拒，只是删掉的那几段不再
// 生效；新版本里新增的段落自然用默认值。

// PromptFileFormatVersion 是提示词文件的格式版本。以后格式有不兼容的变化时加一。
const PromptFileFormatVersion = 1

// PromptFileImport 是读回一份提示词文件的结果。
type PromptFileImport struct {
	// Overrides 是整理后的覆盖表：只含和默认值不同的段落。
	Overrides PromptOverrides `json:"overrides"`
	// Changed 是改过的段落数（正文和输出格式分别算）。
	Changed int `json:"changed"`
	// Unknown 是文件里有、登记表里没有的键：拼错了，或者是旧版本删掉的提示词。
	Unknown []string `json:"unknown,omitempty"`
	// DianaVersion 是导出这份文件的 Diana 版本，只用来提示，不参与判断。
	DianaVersion string `json:"diana_version,omitempty"`
}

var (
	errPromptFileEmpty   = errors.New("提示词文件是空的")
	errPromptFileVersion = errors.New("提示词文件缺少 format_version，不是 Diana 导出的提示词文件")
	yamlLinePattern      = regexp.MustCompile(`\bline (\d+)`)
)

const promptFileHeader = `Diana 内置提示词
每一段都列出来了：没改过的是当前版本的默认原文，改哪段就改哪段的正文，导回去后
保存配置生效。和默认原文一样的段落不会存成覆盖，以后默认文案更新会跟着走；删掉
一整段等于用默认值。{名字} 这样的占位符由运行时填入，删掉的话那项信息就不再进
提示词。带 .format 的是程序要解析的输出格式，改坏了那条链路会沉默或放行。`

// RenderPromptFile 把覆盖表渲染成一份完整的提示词文件。
func RenderPromptFile(overrides PromptOverrides, dianaVersion string) ([]byte, error) {
	root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", HeadComment: promptFileHeader}
	root.Content = append(root.Content,
		yamlString("format_version"), &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(PromptFileFormatVersion)},
	)
	if dianaVersion = strings.TrimSpace(dianaVersion); dianaVersion != "" {
		root.Content = append(root.Content, yamlString("diana_version"), yamlString(dianaVersion))
	}
	root.Content = append(root.Content, yamlString("prompts"), promptsYAMLNode(overrides))
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	// 字面块里的空行会被编码器写成带缩进的空白行，编辑器里看着像多余的空格，去掉。
	return blankLinePadding.ReplaceAll(buffer.Bytes(), nil), nil
}

var blankLinePadding = regexp.MustCompile(`(?m)^[ \t]+$`)

// ParsePromptFile 读回一份提示词文件。
func ParsePromptFile(raw []byte) (PromptFileImport, error) {
	if strings.TrimSpace(string(raw)) == "" {
		return PromptFileImport{}, errPromptFileEmpty
	}
	var file struct {
		FormatVersion int               `yaml:"format_version"`
		DianaVersion  string            `yaml:"diana_version"`
		Prompts       map[string]string `yaml:"prompts"`
	}
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return PromptFileImport{}, promptFileSyntaxError(err)
	}
	switch {
	case file.FormatVersion == 0:
		return PromptFileImport{}, errPromptFileVersion
	case file.FormatVersion > PromptFileFormatVersion:
		return PromptFileImport{}, fmt.Errorf("提示词文件的格式版本是 %d，当前 Diana 只认到 %d，请先升级 Diana", file.FormatVersion, PromptFileFormatVersion)
	}
	result := PromptFileImport{DianaVersion: strings.TrimSpace(file.DianaVersion)}
	incoming := PromptOverrides{}
	for key, value := range file.Prompts {
		key = strings.TrimSpace(key)
		if _, _, ok := promptOverrideDefault(key); !ok {
			result.Unknown = append(result.Unknown, key)
			continue
		}
		incoming[key] = value
	}
	sort.Strings(result.Unknown)
	result.Overrides = normalizePromptOverrides(incoming)
	if err := validatePromptOverrides(result.Overrides); err != nil {
		return PromptFileImport{}, err
	}
	result.Changed = len(result.Overrides)
	return result, nil
}

// promptFileSyntaxError 把 YAML 解析错误说成人话，保留行号。
func promptFileSyntaxError(err error) error {
	if match := yamlLinePattern.FindStringSubmatch(err.Error()); match != nil {
		return fmt.Errorf("提示词文件第 %s 行格式不对（常见原因是缩进没对齐）：%w", match[1], err)
	}
	return fmt.Errorf("提示词文件格式不对：%w", err)
}

// promptsYAMLNode 列出登记表里的每一段提示词，按界面上的分组排：分组前一条横幅，
// 每段一行标题（带占位符）。用途说明在界面上看，全写进文件的话两百多段各带三四行
// 注释，正文反而淹没了。
func promptsYAMLNode(overrides PromptOverrides) *yaml.Node {
	groupLabels := map[PromptGroup]string{}
	for _, group := range promptGroupOrder {
		groupLabels[group.ID] = group.Label
	}
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	var lastGroup PromptGroup
	for _, spec := range PromptSpecs() {
		title := spec.Title
		for _, variable := range spec.Vars {
			title += " {" + variable.Name + "}"
		}
		if spec.Group != lastGroup {
			title = yamlBanner(groupLabels[spec.Group]) + "\n" + title
			lastGroup = spec.Group
		}
		key := yamlString(spec.Key)
		key.HeadComment = title
		value := yamlString(overrides.body(&spec))
		value.Style = yaml.LiteralStyle
		node.Content = append(node.Content, key, value)
		if spec.FormatKey != "" {
			format := yamlString(strings.TrimSpace(overrides.contract(&spec)))
			format.Style = yaml.LiteralStyle
			formatKey := yamlString(spec.FormatKey)
			formatKey.HeadComment = "↑ 上面这段的输出格式，程序按它解析模型的回答"
			node.Content = append(node.Content, formatKey, format)
		}
	}
	return node
}

func yamlBanner(title string) string {
	rule := strings.Repeat("═", 40)
	return strings.Join([]string{"", rule, "【" + title + "】", rule}, "\n")
}

func yamlString(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}
