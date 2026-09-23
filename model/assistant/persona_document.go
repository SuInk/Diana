// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/json"
	"errors"
	"fmt"
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
	Version  int       `json:"version,omitempty"`
	Personas []Persona `json:"personas"`
}

// ErrPersonaDocumentEmpty 表示文件里一套人设都没有。
var ErrPersonaDocumentEmpty = errors.New("persona document contains no personas")

// ParsePersonaDocument 解析一份人设文件，YAML 和 JSON 都收。
//
// 单套人设直接写在顶层（没有 personas 数组）也认：手写一份只有一个角色的文件时，
// 逼人多套一层数组没有道理。
func ParsePersonaDocument(raw []byte) (PersonaDocument, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return PersonaDocument{}, ErrPersonaDocumentEmpty
	}
	var intermediate any
	if err := yaml.Unmarshal(raw, &intermediate); err != nil {
		return PersonaDocument{}, fmt.Errorf("解析人设文件失败：%w", err)
	}
	encoded, err := json.Marshal(intermediate)
	if err != nil {
		return PersonaDocument{}, fmt.Errorf("解析人设文件失败：%w", err)
	}
	var document PersonaDocument
	if err := json.Unmarshal(encoded, &document); err != nil {
		return PersonaDocument{}, fmt.Errorf("人设文件格式不对：%w", err)
	}
	if len(document.Personas) == 0 {
		// 顶层直接是一套人设：再按单套解一次，解得出名字或内容就当它是。
		var single Persona
		if err := json.Unmarshal(encoded, &single); err == nil && !single.Empty() {
			document.Personas = []Persona{single}
		}
	}
	if len(document.Personas) == 0 {
		return PersonaDocument{}, ErrPersonaDocumentEmpty
	}
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
