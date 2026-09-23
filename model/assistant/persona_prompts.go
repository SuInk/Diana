// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
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

// checkPersonaPrompts 校验人设文件里的判据和 prompts：判据不超长，prompts 必须有且完整。
func checkPersonaPrompts(persona Persona) error {
	name := strings.TrimSpace(persona.Name)
	if name == "" {
		name = "未命名"
	}
	// 判据超长直接报错：落库时的清洗会截断，从文件读进来的话截断就是悄悄改了别人的配置。
	if len([]rune(strings.TrimSpace(persona.ExtraCriteria))) > ProactiveReplyExtraCriteriaMaxRunes {
		return fmt.Errorf("人设「%s」的补充判据超过 %d 字", name, ProactiveReplyExtraCriteriaMaxRunes)
	}
	if len([]rune(strings.TrimSpace(persona.AccountSafetyRules))) > AccountSafetyRulesMaxRunes {
		return fmt.Errorf("人设「%s」的账号安全规则超过 %d 字", name, AccountSafetyRulesMaxRunes)
	}
	if persona.Prompts == nil {
		return fmt.Errorf("人设「%s」缺少 prompts：人设文件要列出全部提示词", name)
	}
	var unknown []string
	for key := range persona.Prompts {
		if _, _, ok := promptOverrideDefault(key); !ok {
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
		if _, ok := persona.Prompts[spec.FormatKey]; spec.FormatKey != "" && !ok {
			missing = append(missing, spec.FormatKey)
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
	formatVersion := []*yaml.Node{yamlString("format_version"), yamlInt(PersonaFormatVersion)}
	var root *yaml.Node
	if len(nodes) == 1 {
		root = nodes[0]
		root.Content = append(formatVersion, root.Content...)
	} else {
		root = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: append(formatVersion,
			yamlString("personas"), &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: nodes},
		)}
	}
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(root); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	// 字面块里的空行，编码器会补上缩进空格。只含空格的行清成真正的空行：解析结果
	// 一样，文件里也不留行尾空白。
	return blankLinePadding.ReplaceAll(buffer.Bytes(), nil), nil
}

var blankLinePadding = regexp.MustCompile(`(?m)^[ \t]+$`)

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
	dropMappingKeys(node, "id", "updated_at", "voice", "persona_version")
	// 人设版本号紧跟在名字后面：分享出去的文件第一眼就要看到是第几版。
	version := []*yaml.Node{yamlString("persona_version"), yamlInt(max(persona.Version, 1))}
	insertAfterKey(node, "name", version...)
	if len(node.Content) > 0 {
		node.Content[0].HeadComment = yamlBanner("人设")
	}
	// 判据空着也写出来：YAML 是全部提示词配置，看文件的人得知道这两栏存在。挪到人设
	// 字段后面、提示词前面，自成一节。
	criteria := takeMappingKey(node, "extra_criteria")
	safety := takeMappingKey(node, "account_safety_rules")
	criteria[0].HeadComment = yamlBanner("判据")
	node.Content = append(node.Content, criteria...)
	node.Content = append(node.Content, safety...)
	useBlockStyle(node)
	promptsKey := yamlString("prompts")
	promptsKey.HeadComment = yamlBanner("内置提示词")
	node.Content = append(node.Content, promptsKey, promptsYAMLNode(prompts))
	return node, nil
}

// yamlBanner 是一节开头的分隔横幅，前面空一行：文件里一眼就能看出哪儿是人设、
// 哪儿是接话、哪儿是发送前审核。
//
// 注释只有横幅和每段一行标题。用途说明、占位符解释都在界面和文档里，全写进文件
// 的话两百多段各带三四行注释，正文反而淹没了。
func yamlBanner(title string) string {
	rule := strings.Repeat("═", 40)
	return strings.Join([]string{"", rule, "【" + title + "】", rule}, "\n")
}

// takeMappingKey 从映射里取出一个键值对（没有就造一个空串），返回后原映射里不再有它。
func takeMappingKey(node *yaml.Node, key string) []*yaml.Node {
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			pair := []*yaml.Node{node.Content[index], node.Content[index+1]}
			node.Content = append(node.Content[:index:index], node.Content[index+2:]...)
			return pair
		}
	}
	return []*yaml.Node{yamlString(key), yamlString("")}
}

// promptsYAMLNode 列出登记表里的每一段提示词，按界面上的分组排，注释里写标题和用途。
func promptsYAMLNode(overrides PromptOverrides) *yaml.Node {
	groupLabels := map[PromptGroup]string{}
	for _, group := range promptGroupOrder {
		groupLabels[group.ID] = group.Label
	}
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	var lastGroup PromptGroup
	for _, spec := range PromptSpecs() {
		// 每段只留一行标题，让 reply.wake_only 这种键看得出是什么；占位符挂在标题后面。
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
			node.Content = append(node.Content, yamlString(spec.FormatKey), format)
		}
	}
	return node
}

func yamlInt(value int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(value)}
}

// insertAfterKey 把几个节点插到映射里某个键的值后面；没有这个键就插在最前面。
func insertAfterKey(node *yaml.Node, key string, inserted ...*yaml.Node) {
	position := 0
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			position = index + 2
			break
		}
	}
	content := append([]*yaml.Node{}, node.Content[:position]...)
	content = append(content, inserted...)
	node.Content = append(content, node.Content[position:]...)
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
