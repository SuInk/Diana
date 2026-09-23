// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"sort"
	"strings"
)

// 内置提示词的覆盖机制：每段写死在代码里的提示词登记成一条 PromptSpec，机器人
// 配置里的 PromptOverrides 按 Key 存管理员改过的正文，没改过的一律读内置默认值。
//
// 覆盖值只存「改过的」，不把默认值抄进配置：默认值一旦写进库，之后每次改默认文案，
// 存量机器人都还跑着旧版，而且从字段上看不出那是用户写的还是当年的默认值。
//
// 需要解析输出的提示词（评分、审核、分类）把输出格式拆到 Contract 里，运行时永远拼在
// 正文之后。格式也能改（键是 <Key>.format），但单独成一栏、带着警告：正文随便改，
// 格式改坏一个字，这条链路的兜底就是沉默或放行，而用户看到的只有「机器人突然不说话了」。
// 分开放，改判据的人就不会顺手把格式也改了。

// PromptGroup 是提示词在界面上的分组，顺序见 promptGroupOrder。
type PromptGroup string

const (
	PromptGroupReplyBase  PromptGroup = "reply_base"
	PromptGroupReplyTools PromptGroup = "reply_tools"
	PromptGroupReplyRules PromptGroup = "reply_rules"
	PromptGroupReplyStyle PromptGroup = "reply_style"
	PromptGroupReplyTail  PromptGroup = "reply_tail"
	PromptGroupRouting    PromptGroup = "routing"
	PromptGroupAudit      PromptGroup = "audit"
	PromptGroupMemory     PromptGroup = "memory"
	PromptGroupSocial     PromptGroup = "social"
	PromptGroupMedia      PromptGroup = "media"
	PromptGroupTasks      PromptGroup = "tasks"
)

// PromptGroupInfo 是分组在界面上的标题和一句说明。
type PromptGroupInfo struct {
	ID          PromptGroup `json:"id"`
	Label       string      `json:"label"`
	Description string      `json:"description"`
}

var promptGroupOrder = []PromptGroupInfo{
	{PromptGroupReplyBase, "回复 · 基础文案", "正式回复里紧跟人设的几段：梗与修辞、排版、时间、发言者、只发图或只叫一声时的替代正文。"},
	{PromptGroupReplyTools, "回复 · 工具用法", "对应工具本轮真的可用时才注入，教模型什么时候调、怎么调。"},
	{PromptGroupReplyRules, "回复 · 通用规则", "身份、记忆、拒答、历史格式这类每轮都在的规则。"},
	{PromptGroupReplyStyle, "回复 · 表达与排版", "分条、换行、表情、动作描写和收尾的语气锚点。"},
	{PromptGroupReplyTail, "回复 · 发言者与时间", "随发言者和时刻变化的尾部段落：身份档位、时区、时段、心情。"},
	{PromptGroupRouting, "接话与意图判断", "决定这条消息要不要回、回哪一条、指的是哪条的判断模型提示词。"},
	{PromptGroupAudit, "发送前审核与改写", "候选回复发出去之前的审核、压缩、去重和提示语改写。"},
	{PromptGroupMemory, "记忆与关系", "长期记忆门控、会话摘要、好感度与画像评估。"},
	{PromptGroupSocial, "欢迎、戳一戳与纪念日", "不经过正式回复链路的几种社交回应。"},
	{PromptGroupMedia, "图片、文档与子任务", "看图、OCR、读文档和独立子问题。"},
	{PromptGroupTasks, "订阅与定时任务", "RSS 筛选和定时查询。"},
}

// PromptGroups 返回界面分组，按展示顺序排列。
func PromptGroups() []PromptGroupInfo {
	return append([]PromptGroupInfo(nil), promptGroupOrder...)
}

// PromptVar 是提示词正文里可用的占位符，运行时替换成 {Name}。
type PromptVar struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// PromptSpec 描述一段可以在界面上覆盖的内置提示词。
type PromptSpec struct {
	Key   string      `json:"key"`
	Group PromptGroup `json:"group"`
	Title string      `json:"title"`
	// Usage 说明这段什么时候发给模型、拿来做什么，界面上显示在标题下面。
	Usage   string `json:"usage"`
	Default string `json:"default"`
	// Vars 列出正文里会被替换的占位符。覆盖时删掉占位符不会报错，只是那项信息
	// 不再进提示词，界面会提醒。
	Vars []PromptVar `json:"vars,omitempty"`
	// Contract 是输出格式，运行时拼在正文之后。它自带与正文之间的分隔符，默认正文 +
	// Contract 与改造前的整段提示词逐字节相同。覆盖它用 FormatKey，见 formatKey。
	Contract string `json:"contract,omitempty"`
	// FormatKey 是覆盖输出格式时用的键，只有带 Contract 的条目才有。
	FormatKey string `json:"format_key,omitempty"`
}

var (
	promptRegistry      []*PromptSpec
	promptRegistryByKey = map[string]*PromptSpec{}
)

// registerPrompt 登记一段内置提示词，返回的指针在调用处用来取正文。
//
// 在包级 var 初始化时调用：Key 重复或为空说明两处代码抢了同一个名字，直接 panic，
// 测试一跑就会炸，不会带着一个被静默覆盖的条目上线。
func registerPrompt(spec PromptSpec) *PromptSpec {
	spec.Key = strings.TrimSpace(spec.Key)
	if spec.Key == "" {
		panic("assistant: prompt spec without key")
	}
	if _, exists := promptRegistryByKey[spec.Key]; exists {
		panic("assistant: duplicate prompt spec " + spec.Key)
	}
	if spec.Contract != "" {
		spec.FormatKey = spec.Key + promptFormatSuffix
	}
	registered := &spec
	promptRegistry = append(promptRegistry, registered)
	promptRegistryByKey[spec.Key] = registered
	return registered
}

// PromptSpecs 返回全部可覆盖的提示词，按分组顺序、组内登记顺序排列。
func PromptSpecs() []PromptSpec {
	rank := make(map[PromptGroup]int, len(promptGroupOrder))
	for index, group := range promptGroupOrder {
		rank[group.ID] = index
	}
	specs := make([]PromptSpec, 0, len(promptRegistry))
	for _, spec := range promptRegistry {
		specs = append(specs, *spec)
	}
	sort.SliceStable(specs, func(i, j int) bool {
		return rank[specs[i].Group] < rank[specs[j].Group]
	})
	return specs
}

// PromptOverrideMaxRunes 是单段覆盖正文的长度上限。
//
// 最长的内置提示词（发送前审核）五千多字，给到四倍余量。这个数只挡住误粘贴整本
// 小说这种事故：一段提示词每轮都要发，写得再长也是每轮都在付钱。
const PromptOverrideMaxRunes = 20000

// PromptOverrides 是按 PromptSpec.Key 存放的覆盖正文。没出现的键、空串都表示用内置默认值。
type PromptOverrides map[string]string

// promptFormatSuffix 接在键后面，表示覆盖的是这段的输出格式。
const promptFormatSuffix = ".format"

// text 返回这段提示词本轮实际使用的正文（含输出格式）。
func (o PromptOverrides) text(spec *PromptSpec) string {
	return o.body(spec) + o.contract(spec)
}

// contract 返回输出格式：覆盖过就用覆盖值，并沿用默认格式前面的分隔符（覆盖值是
// 修剪过的，分隔符由程序补）；没覆盖过就是默认格式。
func (o PromptOverrides) contract(spec *PromptSpec) string {
	if spec == nil || spec.Contract == "" {
		return ""
	}
	custom := strings.TrimSpace(o[spec.FormatKey])
	if custom == "" {
		return spec.Contract
	}
	separator := spec.Contract[:len(spec.Contract)-len(strings.TrimLeft(spec.Contract, " \n"))]
	return separator + custom
}

// body 返回正文部分：覆盖过就用覆盖值，否则用默认值。不含 Contract。
func (o PromptOverrides) body(spec *PromptSpec) string {
	if spec == nil {
		return ""
	}
	if custom := strings.TrimSpace(o[spec.Key]); custom != "" {
		return custom
	}
	return spec.Default
}

// render 取正文并替换占位符。只替换 vars 里给出的名字，正文里别的花括号原样保留，
// 用户在覆盖里写 JSON 示例也不会被吃掉。
func (o PromptOverrides) render(spec *PromptSpec, vars map[string]string) string {
	return replacePromptVars(o.text(spec), vars)
}

func replacePromptVars(text string, vars map[string]string) string {
	if len(vars) == 0 {
		return text
	}
	names := make([]string, 0, len(vars))
	for name := range vars {
		names = append(names, name)
	}
	sort.Strings(names)
	pairs := make([]string, 0, len(vars)*2)
	for _, name := range names {
		pairs = append(pairs, "{"+name+"}", vars[name])
	}
	return strings.NewReplacer(pairs...).Replace(text)
}

// isCustomized 报告这段提示词（正文或输出格式）是否被覆盖过。
func (o PromptOverrides) isCustomized(spec *PromptSpec) bool {
	if spec == nil {
		return false
	}
	return strings.TrimSpace(o[spec.Key]) != "" || (spec.FormatKey != "" && strings.TrimSpace(o[spec.FormatKey]) != "")
}

// promptOverrideDefault 按覆盖键找它对应的默认文本：正文键对 Default，格式键对 Contract。
func promptOverrideDefault(key string) (*PromptSpec, string, bool) {
	key = strings.TrimSpace(key)
	if spec, ok := promptRegistryByKey[key]; ok {
		return spec, spec.Default, true
	}
	if base, found := strings.CutSuffix(key, promptFormatSuffix); found {
		if spec, ok := promptRegistryByKey[base]; ok && spec.Contract != "" {
			return spec, spec.Contract, true
		}
	}
	return nil, "", false
}

// prompt 是 cfg.PromptOverrides.text 的简写，调用处大多手里只有 cfg。
func (cfg BotConfig) prompt(spec *PromptSpec) string {
	return cfg.PromptOverrides.text(spec)
}

// promptf 是 cfg.PromptOverrides.render 的简写。
func (cfg BotConfig) promptf(spec *PromptSpec, vars map[string]string) string {
	return cfg.PromptOverrides.render(spec, vars)
}

// normalizePromptOverrides 整理覆盖表：去掉未登记的键（旧版本删掉的提示词）、空值和
// 与默认值相同的值，统一换行符。返回 nil 表示没有任何覆盖。
//
// 「与默认值相同就丢掉」是有意的：界面上把正文改回原样再保存，不该留下一条冻结了
// 当前默认值的覆盖，否则以后默认文案更新，这台机器人会一直停在旧版上。
func normalizePromptOverrides(overrides PromptOverrides) PromptOverrides {
	if len(overrides) == 0 {
		return nil
	}
	normalized := PromptOverrides{}
	for key, value := range overrides {
		_, fallback, ok := promptOverrideDefault(key)
		if !ok {
			continue
		}
		value = strings.TrimSpace(strings.ReplaceAll(value, "\r\n", "\n"))
		if value == "" || value == strings.TrimSpace(fallback) {
			continue
		}
		normalized[strings.TrimSpace(key)] = value
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func copyPromptOverrides(overrides PromptOverrides) PromptOverrides {
	if len(overrides) == 0 {
		return nil
	}
	copied := make(PromptOverrides, len(overrides))
	for key, value := range overrides {
		copied[key] = value
	}
	return copied
}

// validatePromptOverrides 只卡长度。未登记的键不算错：配置可能来自更新的版本，
// 保存时 normalizePromptOverrides 会把它们滤掉。
func validatePromptOverrides(overrides PromptOverrides) error {
	for key, value := range overrides {
		if len([]rune(strings.TrimSpace(value))) <= PromptOverrideMaxRunes {
			continue
		}
		title := key
		if spec, _, ok := promptOverrideDefault(key); ok {
			title = spec.Title
		}
		return fmt.Errorf("提示词「%s」不能超过 %d 字", title, PromptOverrideMaxRunes)
	}
	return nil
}

// customizedPromptKeys 按登记顺序列出被覆盖过的键。
func customizedPromptKeys(overrides PromptOverrides) []string {
	var keys []string
	for _, spec := range PromptSpecs() {
		if overrides.isCustomized(&spec) {
			keys = append(keys, spec.Key)
		}
	}
	return keys
}
