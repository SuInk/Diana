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
	"unicode/utf8"

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
	// Keywords 仅作为扩展目录的发现信息，不触发正文自动注入。
	Keywords []string `json:"keywords,omitempty"`
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
	Name        string       `yaml:"name"`
	Description string       `yaml:"description"`
	Keywords    skillKeyList `yaml:"keywords"`
	Metadata    struct {
		ShortDescription string       `yaml:"short-description"`
		Keywords         skillKeyList `yaml:"keywords"`
	} `yaml:"metadata"`
}

// skillKeyList 同时接受 `keywords: a, b` 和 `keywords: [a, b]` 两种写法：两种都是
// 常见手写形式，为此报一个解析错误、让整个 skill 加载失败不值当。
type skillKeyList []string

func (l *skillKeyList) UnmarshalYAML(node *yaml.Node) error {
	var list []string
	if err := node.Decode(&list); err == nil {
		*l = cleanSkillKeywords(list)
		return nil
	}
	var single string
	if err := node.Decode(&single); err != nil {
		return err
	}
	*l = cleanSkillKeywords(strings.Split(single, ","))
	return nil
}

func cleanSkillKeywords(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		folded := strings.ToLower(value)
		if seen[folded] {
			continue
		}
		seen[folded] = true
		out = append(out, value)
	}
	return out
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
		Keywords:         append(append([]string(nil), frontmatter.Keywords...), frontmatter.Metadata.Keywords...),
		Path:             abs,
	}
	skill.Keywords = cleanSkillKeywords(skill.Keywords)
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
	builder.WriteString("## Skills\n使用前调用 read_skill 读取完整 SKILL.md；相对路径以返回的 path 所在目录为准。\n")
	for _, skill := range skills {
		line := fmt.Sprintf("- %s: %s\n", skill.Name, skill.Description)
		if builder.Len()+len(line) > budget {
			builder.WriteString("- 更多 Skill 请查询 list_capabilities。\n")
			break
		}
		builder.WriteString(line)
	}
	return strings.TrimSpace(builder.String())
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

// hasExplicitSkillMention 认 `$名字`，前后紧挨着名字字符的不算（$demo 不命中
// $demo-skill）。边界只看 ASCII：中文不用空格分词，「用$music帮我点歌」里 music
// 后面紧跟汉字也是点名。以前按字节取前后字符，汉字的 UTF-8 首字节被当成 å、ç 这类
// 拉丁字母，后面接中文的点名一律落空。
func hasExplicitSkillMention(text, name string) bool {
	if name == "" {
		return false
	}
	pattern := regexp.MustCompile(`\$` + regexp.QuoteMeta(name))
	matches := pattern.FindAllStringIndex(text, -1)
	for _, match := range matches {
		before, _ := utf8.DecodeLastRuneInString(text[:match[0]])
		after, _ := utf8.DecodeRuneInString(text[match[1]:])
		if !isSkillNameRune(before) && !isSkillNameRune(after) {
			return true
		}
	}
	return false
}

func isSkillNameRune(r rune) bool {
	return r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-')
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

// 读本地 SKILL.md。按需加载本来就多花一次往返（先读、再照着做），再扣一格预算就是
// 双重惩罚——和 tools_load 同一个理由。
func (t *SkillsReadTool) Introspection(map[string]any) bool { return true }

func (t *SkillsReadTool) Description() string {
	return `读取 skill 的 SKILL.md；长文按 next_offset 续读，has_more=false 才是全文结束。offset/limit 按 Unicode 字符计。`
}

func (t *SkillsReadTool) InputSchema() map[string]any {
	offset := toolIntParam("起始字符位置，从 0 起；续读用上次的 next_offset")
	offset["minimum"] = 0
	limit := toolIntParam("最多读取的 Unicode 字符数；省略时按本次输出额度返回")
	limit["minimum"] = 1
	return toolObjectSchema([]string{"name"}, map[string]any{
		"name":   toolStringParam("skill 名称"),
		"offset": offset,
		"limit":  limit,
	})
}

func (t *SkillsReadTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if err := validateToolInput(t.InputSchema(), input); err != nil {
		return "", err
	}
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
		budget := ToolOutputBudget(ctx)
		if budget <= 0 {
			budget = DefaultMaxToolOutputChars
		}
		return skillContentPage(skill, content, intFromInput(input, "offset", 0), intFromInput(input, "limit", 0), budget)
	}
	return "", fmt.Errorf("skill %q not found", name)
}

// Fit the complete JSON envelope, including escaped content, into the existing
// tool-output allowance. The Runner must not cut the page in the middle of JSON.
func skillContentPage(skill SkillMetadata, content string, offset, limit, budget int) (string, error) {
	runes := []rune(content)
	if offset < 0 || offset > len(runes) {
		return "", fmt.Errorf("offset 必须在 0 到 %d 之间", len(runes))
	}
	end := len(runes)
	if limit > 0 && limit < end-offset {
		end = offset + limit
	}
	render := func(end int) string {
		payload := struct {
			Name       string `json:"name"`
			Path       string `json:"path"`
			Content    string `json:"content"`
			Offset     int    `json:"offset"`
			TotalChars int    `json:"total_chars"`
			HasMore    bool   `json:"has_more"`
			NextOffset *int   `json:"next_offset,omitempty"`
		}{Name: skill.Name, Path: skill.Path, Content: string(runes[offset:end]), Offset: offset, TotalChars: len(runes), HasMore: end < len(runes)}
		if payload.HasMore {
			payload.NextOffset = &end
		}
		encoded, _ := json.MarshalIndent(payload, "", "  ")
		return string(encoded)
	}
	if page := render(end); utf8.RuneCountInString(page) <= budget {
		return page, nil
	}
	low, high := offset, end
	for low < high {
		middle := low + (high-low+1)/2
		if utf8.RuneCountInString(render(middle)) <= budget {
			low = middle
		} else {
			high = middle - 1
		}
	}
	if low == offset {
		return "", errors.New("read_skill 的本次输出额度不足以容纳正文和分页信息")
	}
	return render(low), nil
}

func (t *SkillsReadTool) skills() []SkillMetadata {
	if t == nil || t.provider == nil {
		return nil
	}
	return t.provider()
}
