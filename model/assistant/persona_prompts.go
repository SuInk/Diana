// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	yaml "go.yaml.in/yaml/v4"
)

// 人设文件里的提示词。
//
// 人设 YAML 就是一套人设的全部提示词配置：导出和 YAML 编辑器里总是列出登记表里的
// 每一段，改过的写改过的正文，没改过的写默认原文。读回来时 prompts 必须一段不少、
// 一段不多——缺了的不会悄悄回落默认值，多出来的（拼错的键）也不会被静默丢掉。
// 否则分享出去的一份人设，在对方那里跑出来的会是另一套提示词，而谁都看不出来。
//
// 存储仍然只存改过的（normalizePromptOverrides）：完整性只是文件格式上的要求，
// 落库时和默认值相同的条目照旧丢掉，以后默认文案更新，没改过的那些照样跟着走。
//
// 没有 prompts 这一节的文件（这个功能之前导出的人设、手写的最小人设）仍然能读，
// 按全部用默认值处理：那些文件从来没有声明过提示词，谈不上缺了哪段。

// CheckPersonaPrompts 校验人设文件里的 prompts 是否完整。没有这一节返回 nil。
// 导入接口收前端已经解析好的 JSON 人设时也要过这一道，不能只在 YAML 路径上查。
func CheckPersonaPrompts(persona Persona) error {
	return checkPersonaPrompts(persona)
}

func checkPersonaPrompts(persona Persona) error {
	if persona.Prompts == nil {
		return nil
	}
	name := strings.TrimSpace(persona.Name)
	if name == "" {
		name = "未命名"
	}
	var unknown []string
	for key := range persona.Prompts {
		if _, ok := promptRegistryByKey[strings.TrimSpace(key)]; !ok {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("人设「%s」的 prompts 里有不认识的提示词：%s", name, summarizePromptKeys(unknown))
	}
	var missing []string
	for _, spec := range promptRegistry {
		if _, ok := persona.Prompts[spec.Key]; !ok {
			missing = append(missing, spec.Key)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("人设「%s」的 prompts 缺少 %d 段提示词：%s。人设文件要列出全部提示词，没改过的写默认原文", name, len(missing), summarizePromptKeys(missing))
	}
	for key, value := range persona.Prompts {
		if len([]rune(strings.TrimSpace(value))) > PromptOverrideMaxRunes {
			return fmt.Errorf("人设「%s」的提示词 %s 超过 %d 字", name, key, PromptOverrideMaxRunes)
		}
	}
	return nil
}

func summarizePromptKeys(keys []string) string {
	const shown = 8
	if len(keys) <= shown {
		return strings.Join(keys, "、")
	}
	return strings.Join(keys[:shown], "、") + fmt.Sprintf(" 等 %d 个", len(keys))
}

const personaYAMLHeader = `Diana 人设文件。prompts 列出全部内置提示词：没改过的是默认原文，改哪段就改哪段的正文。
读回时 prompts 必须一段不少、一段不多；正文和默认原文相同的不会存成覆盖，以后默认文案更新会跟着走。
{名字} 这样的占位符由运行时填入，删掉的话那项信息就不再进提示词。`

// RenderPersonaYAML 把人设渲染成 YAML。一套时直接写在顶层，多套时放进 personas 数组。
//
// 结构按 Persona 的 JSON 形状走（先转 JSON 再读成 YAML 节点，字段顺序和 json tag
// 都保住），然后把多行文本改成字面块、把 prompts 换成带注释的全量列表。
func RenderPersonaYAML(personas []Persona) ([]byte, error) {
	nodes := make([]*yaml.Node, 0, len(personas))
	for _, persona := range personas {
		node, err := personaYAMLNode(persona)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	var root *yaml.Node
	if len(nodes) == 1 {
		root = nodes[0]
	} else {
		root = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
			yamlString("version"), {Kind: yaml.ScalarNode, Tag: "!!int", Value: "1"},
			yamlString("personas"), {Kind: yaml.SequenceNode, Tag: "!!seq", Content: nodes},
		}}
	}
	root.HeadComment = personaYAMLHeader
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(root); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func personaYAMLNode(persona Persona) (*yaml.Node, error) {
	persona = persona.flattenVoice()
	prompts := persona.Prompts
	persona.Prompts = nil
	encoded, err := json.Marshal(persona)
	if err != nil {
		return nil, err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(encoded, &document); err != nil {
		return nil, err
	}
	node := document.Content[0]
	// 不带 ID 和时间戳：ID 是本机的，导到别处只会撞车（导入时一律重新分配），
	// 时间按对方导入的那一刻记。
	dropMappingKeys(node, "id", "updated_at", "voice")
	useBlockStyle(node)
	node.Content = append(node.Content, yamlString("prompts"), promptsYAMLNode(prompts))
	return node, nil
}

// promptsYAMLNode 列出登记表里的每一段提示词，按界面上的分组排，注释里写标题和用途。
func promptsYAMLNode(overrides PromptOverrides) *yaml.Node {
	groupLabels := map[PromptGroup]PromptGroupInfo{}
	for _, group := range promptGroupOrder {
		groupLabels[group.ID] = group
	}
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	var lastGroup PromptGroup
	for _, spec := range PromptSpecs() {
		var comment []string
		if spec.Group != lastGroup {
			group := groupLabels[spec.Group]
			comment = append(comment, "──── "+group.Label+" ────", group.Description, "")
			lastGroup = spec.Group
		}
		comment = append(comment, spec.Title+"："+spec.Usage)
		for _, variable := range spec.Vars {
			comment = append(comment, "占位符 {"+variable.Name+"}："+variable.Description)
		}
		if spec.Contract != "" {
			comment = append(comment, "输出格式由程序锁定，总是拼在这段之后，不在这里编辑。")
		}
		key := yamlString(spec.Key)
		key.HeadComment = strings.Join(comment, "\n")
		value := yamlString(overrides.body(&spec))
		value.Style = yaml.LiteralStyle
		node.Content = append(node.Content, key, value)
	}
	return node
}

func yamlString(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func dropMappingKeys(node *yaml.Node, keys ...string) {
	if node.Kind != yaml.MappingNode {
		return
	}
	drop := map[string]bool{}
	for _, key := range keys {
		drop[key] = true
	}
	kept := node.Content[:0]
	for index := 0; index+1 < len(node.Content); index += 2 {
		if drop[node.Content[index].Value] {
			continue
		}
		kept = append(kept, node.Content[index], node.Content[index+1])
	}
	node.Content = kept
}

// useBlockStyle 把从 JSON 读进来的节点改成块写法：JSON 本身是流式 YAML，原样
// 编码出来就是一整行花括号。多行字符串改成字面块——人设正文、品格条目动辄几百字
// 带换行，写成一行转义的 "\n" 就没法在编辑器里改了。
func useBlockStyle(node *yaml.Node) {
	node.Style = 0
	if node.Kind == yaml.ScalarNode && node.Tag == "!!str" && strings.Contains(node.Value, "\n") {
		node.Style = yaml.LiteralStyle
	}
	for _, child := range node.Content {
		useBlockStyle(child)
	}
}
