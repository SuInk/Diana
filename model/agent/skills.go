// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v4"
)

const (
	skillFileName            = "SKILL.md"
	skillInstallMetadataName = ".diana-skill.json"
)

type SkillMetadata struct {
	Name             string `json:"name"`
	Description      string `json:"description"`
	Path             string `json:"path"`
	ShortDescription string `json:"short_description,omitempty"`
	Source           string `json:"source,omitempty"`
	Managed          bool   `json:"managed,omitempty"`
	// Resident 表示这个 skill 的正文随请求一起下发，模型不必再 read_skill。默认按需：
	// 目录只给名称和用途。正文很长的 skill 常驻会把每一轮都撑大，档位由用户自己配。
	Resident bool `json:"resident,omitempty"`
	// Bundled 表示这个 skill 目录里除 SKILL.md 外还带了脚本或资源。正文之外的
	// 文件只有拿得到 run_command / read_file 的会话才碰得到，权限提示要说清楚。
	Bundled bool `json:"bundled,omitempty"`
	// Content is populated only for skills embedded in the Diana binary. It is
	// excluded from catalogs and registry cache keys; read_skill returns it.
	Content string `json:"-"`
}

type skillInstallMetadata struct {
	Source      string `json:"source,omitempty"`
	InstalledAt string `json:"installed_at,omitempty"`
	Managed     bool   `json:"managed"`
}

type skillFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Metadata    struct {
		ShortDescription string `yaml:"short-description"`
	} `yaml:"metadata"`
}

// LoadSkills scans configured roots for local SKILL.md skill folders.
func LoadSkills(roots []string) ([]SkillMetadata, error) {
	var skills []SkillMetadata
	var errs []string
	seen := map[string]bool{}
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		info, err := os.Stat(root)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			errs = append(errs, fmt.Sprintf("%s: %v", root, err))
			continue
		}
		if !info.IsDir() {
			continue
		}
		if filepath.Base(root) == skillFileName {
			if skill, err := parseSkill(root); err == nil {
				if !seen[skill.Path] {
					seen[skill.Path] = true
					skills = append(skills, skill)
				}
			} else {
				errs = append(errs, fmt.Sprintf("%s: %v", root, err))
			}
			continue
		}
		if direct := filepath.Join(root, skillFileName); fileExists(direct) {
			if skill, err := parseSkill(direct); err == nil {
				if !seen[skill.Path] {
					seen[skill.Path] = true
					skills = append(skills, skill)
				}
			} else {
				errs = append(errs, fmt.Sprintf("%s: %v", direct, err))
			}
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", root, err))
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			path := filepath.Join(root, entry.Name(), skillFileName)
			if !fileExists(path) {
				continue
			}
			skill, err := parseSkill(path)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", path, err))
				continue
			}
			if !seen[skill.Path] {
				seen[skill.Path] = true
				skills = append(skills, skill)
			}
		}
	}
	sort.Slice(skills, func(i, j int) bool {
		if skills[i].Name == skills[j].Name {
			return skills[i].Path < skills[j].Path
		}
		return skills[i].Name < skills[j].Name
	})
	if len(errs) > 0 {
		return skills, errors.New(strings.Join(errs, "; "))
	}
	return skills, nil
}

func parseSkill(path string) (SkillMetadata, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return SkillMetadata{}, err
	}
	frontmatter, err := parseSkillFrontmatter(string(body))
	if err != nil {
		return SkillMetadata{}, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return SkillMetadata{}, err
	}
	skill := SkillMetadata{
		Name:             strings.TrimSpace(frontmatter.Name),
		Description:      strings.TrimSpace(frontmatter.Description),
		ShortDescription: strings.TrimSpace(frontmatter.Metadata.ShortDescription),
		Path:             abs,
	}
	skill.Bundled = skillFolderHasResources(filepath.Dir(abs))
	if metadataBody, readErr := os.ReadFile(filepath.Join(filepath.Dir(abs), skillInstallMetadataName)); readErr == nil {
		var installed skillInstallMetadata
		if json.Unmarshal(metadataBody, &installed) == nil {
			skill.Source = strings.TrimSpace(installed.Source)
			skill.Managed = installed.Managed
		}
	}
	if skill.Name == "" {
		return SkillMetadata{}, errors.New("missing skill name")
	}
	if skill.Description == "" {
		return SkillMetadata{}, errors.New("missing skill description")
	}
	return skill, nil
}

// skillFolderHasResources 判断 skill 目录里除正文和安装元数据外还有没有别的文件。
func skillFolderHasResources(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == skillFileName || name == skillInstallMetadataName {
			continue
		}
		return true
	}
	return false
}

func parseSkillFrontmatter(markdown string) (skillFrontmatter, error) {
	trimmed := strings.TrimLeft(markdown, "\ufeff\r\n\t ")
	if !strings.HasPrefix(trimmed, "---") {
		return skillFrontmatter{}, errors.New("missing YAML frontmatter")
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) < 3 {
		return skillFrontmatter{}, errors.New("incomplete YAML frontmatter")
	}
	var yamlLines []string
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			var frontmatter skillFrontmatter
			if err := yaml.Unmarshal([]byte(strings.Join(yamlLines, "\n")), &frontmatter); err != nil {
				return skillFrontmatter{}, err
			}
			return frontmatter, nil
		}
		yamlLines = append(yamlLines, lines[i])
	}
	return skillFrontmatter{}, errors.New("unterminated YAML frontmatter")
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// RenderSkillsCatalog 渲染 skills 目录：名称加用途，不含正文，也不含磁盘路径。
//
// 这份目录按会话变——skill 可以按机器人、按群分档开放，装一个卸一个也会改它。放进
// 系统提示词就等于每换一个群、每装一个 skill 都把整条 prompt 的前缀缓存作废，和
// 之前 loadedContracts 夹在中段是同一个坑。调用方把它挂在消息尾部的易变块里。
func RenderSkillsCatalog(skills []SkillMetadata, budget int) string {
	if len(skills) == 0 {
		return ""
	}
	if budget <= 0 {
		budget = DefaultSkillsListBudget
	}
	var builder strings.Builder
	builder.WriteString("## Skills\n")
	builder.WriteString("A skill is a set of instructions provided through a `SKILL.md` source. The entries below are only names and descriptions; no skill body is included in this request.\n")
	builder.WriteString("### Available skills\n")
	var resident []SkillMetadata
	for _, skill := range skills {
		line := fmt.Sprintf("- %s: %s\n", skill.Name, skill.Description)
		if builder.Len()+len(line) > budget {
			builder.WriteString("- ... additional skills omitted because the skills context budget was reached.\n")
			break
		}
		builder.WriteString(line)
		if skill.Resident {
			resident = append(resident, skill)
		}
	}
	builder.WriteString("### How to use skills\n")
	builder.WriteString("- If the user names a skill with `$SkillName`, or the task clearly matches a skill description, use that skill for this turn.\n")
	builder.WriteString("- A catalog entry is not the skill: always call `read_skill` with its name first, then follow the full `SKILL.md` instructions.\n")
	builder.WriteString("- When a `SKILL.md` references relative files, resolve them relative to the directory of the `path` returned by `read_skill`.\n")
	// 常驻 skill 的正文直接跟在目录后面:用户把它配成常驻,就是因为「要用时再读」
	// 在长上下文里读不到。正文在这里给全,模型不必再 read_skill。
	remaining := ResidentSkillBodyBudget
	for _, skill := range resident {
		body := strings.TrimSpace(skillBody(skill))
		if body == "" {
			continue
		}
		if len(body) > remaining {
			builder.WriteString(fmt.Sprintf("\n### %s (resident, body too long)\nCall `read_skill` with %q: it is marked resident but its body did not fit the budget.\n", skill.Name, skill.Name))
			continue
		}
		remaining -= len(body)
		builder.WriteString("\n### Resident skill: " + skill.Name + "\n")
		builder.WriteString("Its full `SKILL.md` follows; do not call `read_skill` for it.\n\n")
		builder.WriteString(body)
		builder.WriteString("\n")
	}
	return strings.TrimSpace(builder.String())
}

// skillBody 返回 SKILL.md 正文；内嵌 skill 自带正文,本地 skill 现读。读不出来就当
// 没有正文,目录行仍在,模型还能走 read_skill。
func skillBody(skill SkillMetadata) string {
	if skill.Content != "" {
		return skill.Content
	}
	if skill.Path == "" {
		return ""
	}
	data, err := os.ReadFile(skill.Path)
	if err != nil {
		return ""
	}
	return string(data)
}

func SelectExplicitSkills(skills []SkillMetadata, text string) []SkillMetadata {
	if strings.TrimSpace(text) == "" || len(skills) == 0 {
		return nil
	}
	var selected []SkillMetadata
	for _, skill := range skills {
		if hasExplicitSkillMention(text, skill.Name) {
			selected = append(selected, skill)
		}
	}
	return selected
}

func hasExplicitSkillMention(text, name string) bool {
	if name == "" {
		return false
	}
	pattern := regexp.MustCompile(`\$` + regexp.QuoteMeta(name))
	matches := pattern.FindAllStringIndex(text, -1)
	for _, match := range matches {
		beforeOK := match[0] == 0 || !isSkillNameRune(rune(text[match[0]-1]))
		afterIndex := match[1]
		afterOK := afterIndex >= len(text) || !isSkillNameRune(rune(text[afterIndex]))
		if beforeOK && afterOK {
			return true
		}
	}
	return false
}

func isSkillNameRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-'
}

type SkillTools struct {
	Read *SkillsReadTool
}

func NewSkillTools(skills []SkillMetadata) SkillTools {
	copied := append([]SkillMetadata(nil), skills...)
	return newLiveSkillTools(func() []SkillMetadata {
		return append([]SkillMetadata(nil), copied...)
	})
}

func newLiveSkillTools(provider func() []SkillMetadata) SkillTools {
	return SkillTools{
		Read: &SkillsReadTool{provider: provider},
	}
}

type SkillsReadTool struct {
	provider func() []SkillMetadata
}

func (t *SkillsReadTool) Name() string {
	return "read_skill"
}

func (t *SkillsReadTool) Description() string {
	return `读取某个 skill 的完整 SKILL.md。`
}

func (t *SkillsReadTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"name"}, map[string]any{
		"name": toolStringParam("skill 名称"),
	})
}

func (t *SkillsReadTool) Run(_ context.Context, input map[string]any) (string, error) {
	name := stringFromInput(input, "name")
	if name == "" {
		return "", errors.New("name is required")
	}
	for _, skill := range t.skills() {
		if skill.Name != name {
			continue
		}
		content := skill.Content
		if content == "" {
			body, err := os.ReadFile(skill.Path)
			if err != nil {
				return "", err
			}
			content = string(body)
		}
		payload := map[string]any{
			"name":    skill.Name,
			"path":    skill.Path,
			"content": content,
		}
		encoded, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	}
	return "", fmt.Errorf("skill %q not found", name)
}

func (t *SkillsReadTool) skills() []SkillMetadata {
	if t == nil || t.provider == nil {
		return nil
	}
	return t.provider()
}
