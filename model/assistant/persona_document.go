// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	yaml "go.yaml.in/yaml/v4"
)

// 人设文件的解析。
//
// 以前只收 JSON。品格层写成 JSON 很难读——它有十来段、每条还带一句「因为」，
// 而这些正是要人反复改的东西：JSON 里没有注释、没有多行字符串，一段两百字的
// 身份描述会变成一行塞满 \n 的字符串。YAML 两样都有。
//
// 解析走 YAML 再转 JSON 的两步：YAML 是 JSON 的超集，一份老的 .json 照样能过，
// 不用维护两套解析；结构体上也不必为同一个字段挂两套 tag（yaml 包默认按小写
// 字段名匹配，和现有的 json tag 对不上，漏一个就是静默丢字段）。
// cmd/webui/appconfig.go 里的 decodeSection 用的是同一个办法。

// PersonaDocument 是一份人设文件。
type PersonaDocument struct {
	// FormatVersion 是文件格式的版本，解析按它走；0 表示没写版本号的旧文件。
	FormatVersion int       `json:"format_version,omitempty"`
	Personas      []Persona `json:"personas"`
}

// PersonaFormatVersion 是当前写出的人设文件格式版本。
//
// 版本 1：顶层写 format_version；一套人设直接写在顶层，多套放进 personas。字段只认
// Persona 的那些，拼错的字段名直接报错；prompts 必须有，而且一段不少、一段不多。
// 以后格式再变就加版本号，旧版本的解析分支留着，已经分享出去的文件照样能读。
const PersonaFormatVersion = 1

// ErrPersonaDocumentEmpty 表示文件里一套人设都没有。
var ErrPersonaDocumentEmpty = errors.New("persona document contains no personas")

// ParsePersonaDocument 解析一份人设文件，按 format_version 选规则：
//
//   - 版本 1：严格读，见 PersonaFormatVersion。
//   - 没写版本号：拒绝。这个格式之前的文件（旧 JSON 导出、手写的最小人设）不再兼容，
//     少了提示词和判据的文件读进来，跑出来的就不是作者给的那套人设。
//   - 比当前新的版本：拒绝，不猜。新格式里多出来的东西被旧程序悄悄丢掉，导入的人设
//     就和作者给的不一样了。
func ParsePersonaDocument(raw []byte) (PersonaDocument, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return PersonaDocument{}, ErrPersonaDocumentEmpty
	}
	var root any
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return PersonaDocument{}, yamlSyntaxError(err)
	}
	top, ok := root.(map[string]any)
	if !ok {
		return PersonaDocument{}, errors.New("人设文件的顶层应该是「键: 值」的映射")
	}
	version, err := personaFormatVersionOf(top)
	if err != nil {
		return PersonaDocument{}, err
	}
	var document PersonaDocument
	switch {
	case version == 0:
		return PersonaDocument{}, errors.New("人设文件缺少 format_version：旧格式的人设文件不再支持，请在 Diana 里重新导出")
	case version == 1:
		document, err = parsePersonaDocumentV1(top)
	default:
		return PersonaDocument{}, fmt.Errorf("人设文件的格式版本是 %d，当前 Diana 只认到 %d，请先升级 Diana", version, PersonaFormatVersion)
	}
	if err != nil {
		return PersonaDocument{}, err
	}
	document.FormatVersion = version
	for index, persona := range document.Personas {
		// 先查完整性再清洗：Normalized 会把和默认值相同的条目丢掉，丢完就分不出
		// 「写了但没改」和「根本没写」。
		if err := checkPersonaPrompts(persona); err != nil {
			return PersonaDocument{}, err
		}
		document.Personas[index] = persona.Normalized()
	}
	return document, nil
}

func personaFormatVersionOf(top map[string]any) (int, error) {
	value, ok := top["format_version"]
	if !ok || value == nil {
		return 0, nil
	}
	switch number := value.(type) {
	case int:
		if number >= 1 {
			return number, nil
		}
	case uint64:
		if number >= 1 && number < 1<<31 {
			return int(number), nil
		}
	}
	return 0, fmt.Errorf("format_version 应该是正整数，现在是 %v", value)
}

// parsePersonaDocumentV1 严格读版本 1：字段名拼错直接报错，每套都要有名字和 prompts。
func parsePersonaDocumentV1(top map[string]any) (PersonaDocument, error) {
	var items []any
	if list, ok := top["personas"]; ok {
		for key := range top {
			if key != "format_version" && key != "personas" {
				return PersonaDocument{}, fmt.Errorf("不认识的字段 %s：放多套人设时顶层只能有 format_version 和 personas", key)
			}
		}
		array, ok := list.([]any)
		if !ok {
			return PersonaDocument{}, errors.New("personas 应该是列表")
		}
		items = array
	} else {
		single := make(map[string]any, len(top))
		for key, value := range top {
			if key != "format_version" {
				single[key] = value
			}
		}
		items = []any{single}
	}
	if len(items) == 0 {
		return PersonaDocument{}, ErrPersonaDocumentEmpty
	}
	personas := make([]Persona, 0, len(items))
	for index, item := range items {
		where := ""
		if len(items) > 1 {
			where = fmt.Sprintf("第 %d 套人设：", index+1)
		}
		fields, ok := item.(map[string]any)
		if !ok {
			return PersonaDocument{}, fmt.Errorf("%s应该是「键: 值」的映射", where)
		}
		encoded, err := json.Marshal(fields)
		if err != nil {
			return PersonaDocument{}, fmt.Errorf("%s解析失败：%w", where, err)
		}
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.DisallowUnknownFields()
		var persona Persona
		if err := decoder.Decode(&persona); err != nil {
			return PersonaDocument{}, personaFieldError(where, err)
		}
		if strings.TrimSpace(persona.Name) == "" {
			return PersonaDocument{}, fmt.Errorf("%s缺少 name", where)
		}
		if persona.Prompts == nil {
			return PersonaDocument{}, fmt.Errorf("%s人设「%s」缺少 prompts：格式版本 1 的人设文件要列出全部提示词", where, persona.Name)
		}
		personas = append(personas, persona)
	}
	return PersonaDocument{Personas: personas}, nil
}

var yamlLinePattern = regexp.MustCompile(`\bline (\d+)`)

// yamlSyntaxError 把解析器的英文报错换成带行号的中文开头，原文留着便于排查。
func yamlSyntaxError(err error) error {
	message := strings.TrimPrefix(err.Error(), "yaml: ")
	message = yamlLinePattern.ReplaceAllString(message, "第 $1 行")
	return fmt.Errorf("YAML 语法错误：%s", message)
}

// personaFieldError 把字段层面的错误说成人话：拼错的字段名、类型不对。
func personaFieldError(where string, err error) error {
	var typeError *json.UnmarshalTypeError
	if errors.As(err, &typeError) {
		return fmt.Errorf("%s字段 %s 的类型不对：写成了%s", where, typeError.Field, jsonKindLabel(typeError.Value))
	}
	if field, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
		return fmt.Errorf("%s不认识的字段 %s，检查一下是不是拼错了", where, strings.Trim(field, `"`))
	}
	return fmt.Errorf("%s人设文件格式不对：%w", where, err)
}

func jsonKindLabel(kind string) string {
	switch kind {
	case "string":
		return "文字"
	case "number":
		return "数字"
	case "bool":
		return "true/false"
	case "array":
		return "列表"
	case "object":
		return "映射"
	}
	return kind
}
