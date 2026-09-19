// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/ghmirror"
)

// 第三方插件（仓库插件）：以 GitHub 仓库为分发载体，默认通过仓库链接安装。
// 开发文档见 docs/third-party-plugins.md；examples/plugin-template 是配套的
// GitHub 模板仓库。

const (
	// RepoPluginManifestFile 是仓库根目录里唯一被认可的清单文件名。
	RepoPluginManifestFile = "diana.plugin.json"
	// RepoPluginEntryFile 是插件入口文件，清单 entry 必须指向它。
	RepoPluginEntryFile = "SKILL.md"
	// repoPluginSourceDir 是数据目录下存放第三方插件源码的目录名。
	repoPluginSourceDir = "plugin-sources"

	repoPluginMaxFiles      = 2000
	repoPluginMaxTotalBytes = 64 << 20
	repoPluginMaxFileBytes  = 8 << 20
)

var (
	ErrRepoPluginURL       = errors.New("diana: 不是有效的 GitHub 仓库链接")
	ErrRepoPluginManifest  = errors.New("diana: 仓库缺少有效的 " + RepoPluginManifestFile)
	ErrRepoPluginFormat    = errors.New("diana: 插件清单格式不合法")
	ErrRepoPluginSkill     = errors.New("diana: SKILL.md 缺少 name/description frontmatter")
	ErrRepoPluginRisk      = errors.New("diana: 未确认安装风险")
	ErrRepoPluginNotSource = errors.New("diana: 该插件不是从仓库安装的")
)

// RepoPluginRef 是解析后的 GitHub 仓库坐标。Ref 为空表示默认分支最新提交。
type RepoPluginRef struct {
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
	Ref   string `json:"ref,omitempty"`
}

// RepoURL 返回规范化仓库地址。
func (r RepoPluginRef) RepoURL() string {
	return "https://github.com/" + r.Owner + "/" + r.Repo
}

// gitHubRef 返回下载用的 ref；空 Ref 用 HEAD 指默认分支。
func (r RepoPluginRef) gitHubRef() string {
	if strings.TrimSpace(r.Ref) == "" {
		return "HEAD"
	}
	return r.Ref
}

var (
	gitHubOwnerRepoPattern  = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	gitHubCommitPattern     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	pluginIDPattern         = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	pluginVersionPattern    = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	pluginSettingKeyPattern = regexp.MustCompile(`^[a-z0-9_]+$`)
)

// ParseGitHubRepoURL 解析插件安装链接。只接受 github.com 的仓库地址，
// 支持 /owner/repo、/owner/repo/tree/<ref> 两种路径；其他 Git 托管域名
// 和 github.com 的其他子路径一律拒绝，不做开放重定向。
func ParseGitHubRepoURL(raw string) (RepoPluginRef, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return RepoPluginRef{}, ErrRepoPluginURL
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") {
		return RepoPluginRef{}, ErrRepoPluginURL
	}
	if !strings.EqualFold(parsed.Hostname(), "github.com") {
		return RepoPluginRef{}, fmt.Errorf("%w: 只支持 github.com", ErrRepoPluginURL)
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 2 && len(parts) != 4 {
		return RepoPluginRef{}, ErrRepoPluginURL
	}
	ref := RepoPluginRef{Owner: parts[0], Repo: parts[1]}
	if !gitHubOwnerRepoPattern.MatchString(ref.Owner) || !gitHubOwnerRepoPattern.MatchString(ref.Repo) {
		return RepoPluginRef{}, ErrRepoPluginURL
	}
	if len(parts) == 4 {
		if parts[2] != "tree" || parts[3] == "" || strings.Contains(parts[3], "/") {
			return RepoPluginRef{}, ErrRepoPluginURL
		}
		ref.Ref = parts[3]
	}
	return ref, nil
}

// repoPluginManifestFile 是 diana.plugin.json 的严格解码形态。
// DisallowUnknownFields 让 official/built_in 这类内置字段直接判为格式错误，
// 第三方清单永远拿不到内置语义。
type repoPluginManifestFile struct {
	ID            string              `json:"id"`
	Name          string              `json:"name"`
	Version       string              `json:"version"`
	Description   string              `json:"description"`
	Permissions   []string            `json:"permissions"`
	Platforms     []string            `json:"platforms,omitempty"`
	PlatformNotes map[string]string   `json:"platform_notes,omitempty"`
	Settings      []PluginSettingSpec `json:"settings,omitempty"`
	Entry         string              `json:"entry"`
	Files         []string            `json:"files,omitempty"`
	MinDiana      string              `json:"min_diana,omitempty"`
	Homepage      string              `json:"homepage,omitempty"`
	Source        string              `json:"source,omitempty"`
}

// decodeRepoPluginManifest 严格解码清单：未知字段、多份 JSON 都拒绝。
func decodeRepoPluginManifest(data []byte) (repoPluginManifestFile, error) {
	var manifest repoPluginManifestFile
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return repoPluginManifestFile{}, fmt.Errorf("%w: %v", ErrRepoPluginFormat, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return repoPluginManifestFile{}, fmt.Errorf("%w: 清单只能包含一份 JSON", ErrRepoPluginFormat)
	}
	return manifest, nil
}

var repoPluginSettingTypes = map[string]bool{
	PluginSettingTypeBool:               true,
	PluginSettingTypeNumber:             true,
	PluginSettingTypeString:             true,
	PluginSettingTypeSelect:             true,
	PluginSettingTypeMultiSelect:        true,
	PluginSettingTypePlatformLevelRules: true,
	PluginSettingTypeText:               true,
	PluginSettingTypeSize:               true,
}

// validate 校验清单格式。返回的 error 一律包装 ErrRepoPluginFormat，
// 安装器据此向用户展示「格式不合法」而不是网络错误。
func (m repoPluginManifestFile) validate() error {
	wrap := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrRepoPluginFormat, fmt.Sprintf(format, args...))
	}
	m.ID = strings.TrimSpace(m.ID)
	if !pluginIDPattern.MatchString(m.ID) {
		return wrap("id %q 不合法：小写字母、数字、点、横线、下划线，且以字母或数字开头", m.ID)
	}
	if strings.HasPrefix(m.ID, "official.") {
		return wrap("id %q 使用了保留的 official. 前缀", m.ID)
	}
	if strings.TrimSpace(m.Name) == "" {
		return wrap("name 不能为空")
	}
	if strings.TrimSpace(m.Description) == "" {
		return wrap("description 不能为空")
	}
	m.Version = strings.TrimSpace(m.Version)
	if !pluginVersionPattern.MatchString(m.Version) {
		return wrap("version %q 不合法：需要 x.y.z 语义化版本", m.Version)
	}
	if len(m.Permissions) == 0 {
		return wrap("permissions 不能为空：请如实声明插件需要的全部权限")
	}
	for _, permission := range m.Permissions {
		if _, ok := repoPluginPermissionByID(permission); !ok {
			return wrap("声明了未知权限 %q", permission)
		}
	}
	platformIDs := map[string]bool{}
	for _, platform := range SupportedPlatforms() {
		platformIDs[platform.ID] = true
	}
	for _, platform := range m.Platforms {
		if !platformIDs[NormalizePlatformID(platform)] {
			return wrap("platforms 包含未知平台 %q", platform)
		}
	}
	if strings.TrimSpace(m.Entry) != RepoPluginEntryFile {
		return wrap("entry 必须是 %s", RepoPluginEntryFile)
	}
	if err := m.validateFiles(wrap); err != nil {
		return err
	}
	if strings.TrimSpace(m.MinDiana) != "" && !pluginVersionPattern.MatchString(strings.TrimPrefix(strings.TrimSpace(m.MinDiana), "v")) {
		return wrap("min_diana %q 不合法：需要 x.y.z 版本", m.MinDiana)
	}
	for _, raw := range []struct {
		field string
		link  string
	}{
		{"homepage", m.Homepage},
		{"source", m.Source},
	} {
		if link := strings.TrimSpace(raw.link); link != "" {
			parsed, err := url.Parse(link)
			if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Hostname() == "" {
				return wrap("%s %q 不是合法的 http(s) 链接", raw.field, link)
			}
		}
	}
	return m.validateSettings(wrap)
}

func (m repoPluginManifestFile) validateSettings(wrap func(string, ...any) error) error {
	seen := map[string]bool{}
	for _, spec := range m.Settings {
		key := strings.TrimSpace(spec.Key)
		if !pluginSettingKeyPattern.MatchString(key) {
			return wrap("settings.key %q 不合法：小写字母、数字、下划线", spec.Key)
		}
		if seen[key] {
			return wrap("settings.key %q 重复", key)
		}
		seen[key] = true
		if strings.TrimSpace(spec.Label) == "" {
			return wrap("settings %q 缺少 label", key)
		}
		if !repoPluginSettingTypes[spec.Type] {
			return wrap("settings %q 类型 %q 未知", key, spec.Type)
		}
		if spec.Default == nil {
			return wrap("settings %q 缺少默认值", key)
		}
		if spec.Min != nil && spec.Max != nil && *spec.Min > *spec.Max {
			return wrap("settings %q min 大于 max", key)
		}
		switch spec.Type {
		case PluginSettingTypeSelect, PluginSettingTypeMultiSelect:
			if len(spec.Options) == 0 {
				return wrap("settings %q 是 %s 类型，必须提供 options", key, spec.Type)
			}
			optionValues := map[string]bool{}
			for _, option := range spec.Options {
				if strings.TrimSpace(option.Value) == "" || strings.TrimSpace(option.Label) == "" {
					return wrap("settings %q 存在空的 option value/label", key)
				}
				if optionValues[option.Value] {
					return wrap("settings %q option value %q 重复", key, option.Value)
				}
				optionValues[option.Value] = true
			}
		}
		if spec.Secret && spec.Type != PluginSettingTypeString && spec.Type != PluginSettingTypeText {
			return wrap("settings %q 声明了 secret，类型必须是 string 或 text", key)
		}
	}
	return nil
}

// normalizedFiles 返回分发文件白名单。files 缺省时只分发入口文件；入口文件
// 永远在白名单里。以 / 结尾的条目视为目录（安装时整目录分发，无法逐个核对
// 存在性）；其余条目视为文件，安装前逐一核对远端存在。
func (m repoPluginManifestFile) normalizedFiles() []string {
	files := []string{RepoPluginEntryFile}
	seen := map[string]bool{RepoPluginEntryFile: true}
	for _, entry := range m.Files {
		entry = strings.Trim(strings.TrimSpace(entry), " ")
		entry = strings.TrimPrefix(entry, "/")
		if entry == "" {
			continue
		}
		directory := strings.HasSuffix(entry, "/")
		cleaned := filepath.ToSlash(filepath.Clean(strings.TrimSuffix(entry, "/")))
		if cleaned == "." || cleaned == "" {
			continue
		}
		normalized := cleaned
		if directory {
			normalized += "/"
		}
		if seen[normalized] {
			continue
		}
		seen[normalized] = true
		files = append(files, normalized)
	}
	return files
}

// validateFiles 校验 files 白名单路径安全：不允许绝对路径和 .. 越级。
func (m repoPluginManifestFile) validateFiles(wrap func(string, ...any) error) error {
	for _, entry := range m.Files {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		core := strings.TrimSuffix(strings.TrimPrefix(entry, "/"), "/")
		cleaned := filepath.ToSlash(filepath.Clean(core))
		if cleaned == "." || cleaned == "" || cleaned == ".." || strings.HasPrefix(cleaned, "../") || filepath.IsAbs(cleaned) {
			return wrap("files 包含非法路径 %q", entry)
		}
	}
	return nil
}

// pluginManifest 转成对内的 PluginManifest，登记进插件管理器。
func (m repoPluginManifestFile) pluginManifest() PluginManifest {
	notes := make(map[string]string, len(m.PlatformNotes))
	for platform, note := range m.PlatformNotes {
		notes[NormalizePlatformID(platform)] = note
	}
	return PluginManifest{
		ID:            m.ID,
		Name:          strings.TrimSpace(m.Name),
		Version:       m.Version,
		Description:   strings.TrimSpace(m.Description),
		Official:      false,
		BuiltIn:       false,
		Platforms:     m.Platforms,
		PlatformNotes: notes,
		Permissions:   append([]string(nil), m.Permissions...),
		Settings:      m.Settings,
	}
}

// RepoPluginPermission 是权限词表条目，安装确认框据此翻译与排序。
type RepoPluginPermission struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Sensitive bool   `json:"sensitive"`
}

// repoPluginPermissionCatalog 是第三方插件可以声明的全部权限。词表与内置
// 插件同一套；新增权限只能在 Diana 主版本里加，插件申报词表之外的值直接
// 按格式错误拒绝安装。
var repoPluginPermissionCatalog = []RepoPluginPermission{
	{ID: "message:read", Label: "读取消息内容与历史"},
	{ID: "message:send", Label: "主动发送消息"},
	{ID: "message:write", Label: "修改、撤回已发消息", Sensitive: true},
	{ID: "notice:read", Label: "读取群公告与通知事件"},
	{ID: "network:http", Label: "发起 HTTP 网络请求"},
	{ID: "network:https", Label: "发起 HTTPS 网络请求"},
	{ID: "llm:generate", Label: "调用模型生成内容"},
	{ID: "llm:multiple", Label: "多次调用模型生成内容"},
	{ID: "llm:tool", Label: "注册为模型可调用工具"},
	{ID: "llm:config:write", Label: "修改模型配置", Sensitive: true},
	{ID: "file:parse", Label: "解析附件文档"},
	{ID: "file:write", Label: "写入文件", Sensitive: true},
	{ID: "filesystem:temp", Label: "使用临时目录"},
	{ID: "filesystem:write", Label: "写入工作目录", Sensitive: true},
	{ID: "process:execute", Label: "执行外部命令", Sensitive: true},
	{ID: "process:media", Label: "调用媒体处理工具"},
	{ID: "browser:render", Label: "使用浏览器渲染页面"},
	{ID: "browser:headless", Label: "使用无头浏览器"},
	{ID: "sandbox:ephemeral", Label: "使用一次性沙箱"},
	{ID: "task:persistent", Label: "创建持久后台任务"},
	{ID: "task:notify", Label: "发送任务通知"},
	{ID: "agent:tool", Label: "作为 Agent 工具被调用"},
	{ID: "github:contents:read", Label: "读取 GitHub 仓库内容"},
	{ID: "github:issues:read", Label: "读取 GitHub Issues"},
	{ID: "github:issues:write", Label: "创建和修改 GitHub Issues"},
	{ID: "github:pull_requests:read", Label: "读取 GitHub Pull Requests"},
	{ID: "github:pull_requests:write", Label: "创建和修改 GitHub Pull Requests"},
	{ID: "platform:group:read", Label: "读取群信息"},
	{ID: "platform:group:moderate:owner", Label: "执行群主级群管理操作", Sensitive: true},
	{ID: "knowledge:read", Label: "读取知识库"},
	{ID: "plugin:list", Label: "读取插件清单"},
	{ID: "audit:write", Label: "写审计日志"},
}

func repoPluginPermissionByID(id string) (RepoPluginPermission, bool) {
	for _, permission := range repoPluginPermissionCatalog {
		if permission.ID == id {
			return permission, true
		}
	}
	return RepoPluginPermission{}, false
}

// DescribeRepoPluginPermissions 把权限声明翻译成确认框展示形态：高敏感权限
// 排在前面，未在词表中的值跳过（安装前的格式校验已经挡掉）。
func DescribeRepoPluginPermissions(ids []string) []RepoPluginPermission {
	out := make([]RepoPluginPermission, 0, len(ids))
	for _, id := range ids {
		if permission, ok := repoPluginPermissionByID(id); ok {
			out = append(out, permission)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Sensitive != out[j].Sensitive {
			return out[i].Sensitive
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// validateSkillFrontmatter 校验 SKILL.md 带 name/description 的 YAML frontmatter。
// 与扩展页 Skill 导入同一规则；不依赖完整 YAML 解析，只扫描 frontmatter 块。
func validateSkillFrontmatter(data []byte) error {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return fmt.Errorf("%w: 缺少 YAML frontmatter", ErrRepoPluginSkill)
	}
	rest := text[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return fmt.Errorf("%w: frontmatter 未闭合", ErrRepoPluginSkill)
	}
	found := map[string]bool{}
	for _, line := range strings.Split(rest[:end], "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if (key == "name" || key == "description") && value != "" {
			found[key] = true
		}
	}
	if !found["name"] || !found["description"] {
		return fmt.Errorf("%w: name 和 description 都必须非空", ErrRepoPluginSkill)
	}
	return nil
}

// RepoPluginSource 记录一次仓库安装的出处，更新时按同一坐标重新拉取。
type RepoPluginSource struct {
	ID          string    `json:"id"`
	Owner       string    `json:"owner"`
	Repo        string    `json:"repo"`
	Ref         string    `json:"ref,omitempty"`
	Version     string    `json:"version"`
	URL         string    `json:"url"`
	InstalledAt time.Time `json:"installed_at"`
}

// RepoPluginStore 把已安装第三方插件的来源记录在数据目录的 index.json 里。
type RepoPluginStore struct {
	mu      sync.Mutex
	path    string
	sources map[string]RepoPluginSource
}

// NewRepoPluginStore 创建来源记录存储；Load 之前 List/Get 返回空。
func NewRepoPluginStore(dataDir string) *RepoPluginStore {
	return &RepoPluginStore{
		path:    filepath.Join(dataDir, repoPluginSourceDir, "index.json"),
		sources: map[string]RepoPluginSource{},
	}
}

// Load 读取已保存的来源记录。文件不存在视为没有记录，不是错误。
func (s *RepoPluginStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var sources []RepoPluginSource
	if err := json.Unmarshal(data, &sources); err != nil {
		return fmt.Errorf("diana: 解析第三方插件来源记录: %w", err)
	}
	for _, source := range sources {
		s.sources[source.ID] = source
	}
	return nil
}

// List 返回全部来源记录，按插件 ID 排序。
func (s *RepoPluginStore) List() []RepoPluginSource {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RepoPluginSource, 0, len(s.sources))
	for _, source := range s.sources {
		out = append(out, source)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Get 返回单个来源记录。
func (s *RepoPluginStore) Get(id string) (RepoPluginSource, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	source, ok := s.sources[id]
	return source, ok
}

// Save 写入或替换一条来源记录并落盘。
func (s *RepoPluginStore) Save(source RepoPluginSource) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sources[source.ID] = source
	return s.writeLocked()
}

// Remove 删除一条来源记录并落盘。
func (s *RepoPluginStore) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sources, id)
	return s.writeLocked()
}

func (s *RepoPluginStore) writeLocked() error {
	sources := make([]RepoPluginSource, 0, len(s.sources))
	for _, source := range s.sources {
		sources = append(sources, source)
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].ID < sources[j].ID })
	data, err := json.MarshalIndent(sources, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// RepoPlugin 是从仓库安装的第三方插件。v1 的运行时形态是上下文插件：
// 每次请求把 SKILL.md 指令作为插件上下文注入提示词，由模型按指令执行。
// 插件声明的权限用于安装确认与展示；通用适配器本身只读自己目录里的文件。
type RepoPlugin struct {
	manifest PluginManifest
	dir      string
	source   RepoPluginSource
}

// NewRepoPlugin 从已安装目录构造插件实例。
func NewRepoPlugin(dir string, source RepoPluginSource, manifest PluginManifest) *RepoPlugin {
	return &RepoPlugin{manifest: manifest, dir: dir, source: source}
}

func (p *RepoPlugin) Manifest() PluginManifest { return p.manifest }

// Source 返回安装来源，WebUI 据此展示「从 GitHub 安装」标记与更新入口。
func (p *RepoPlugin) Source() RepoPluginSource { return p.source }

func (p *RepoPlugin) Handle(_ context.Context, _ PluginRequest) (*PluginResponse, error) {
	body, err := os.ReadFile(filepath.Join(p.dir, RepoPluginEntryFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	content := strings.TrimSpace(string(body))
	if content == "" {
		return nil, nil
	}
	return &PluginResponse{Handled: true, Context: content}, nil
}

// RepoPluginRisk 是安装确认框的风险提示。
type RepoPluginRisk struct {
	// FloatingRef 为 true 表示安装的是默认分支最新提交，内容与权限随时可能变化。
	FloatingRef bool     `json:"floating_ref"`
	Warnings    []string `json:"warnings"`
}

// RepoPluginPreview 是「粘贴链接 → 确认安装」中间步骤的完整视图：
// 格式校验通过后原样返回给前端，确认框拿它渲染权限与风险。
type RepoPluginPreview struct {
	Source      RepoPluginRef          `json:"source"`
	Manifest    PluginManifest         `json:"manifest"`
	Permissions []RepoPluginPermission `json:"permissions"`
	Files       []string               `json:"files"`
	Risk        RepoPluginRisk         `json:"risk"`
}

// RepoPluginInstaller 负责拉取、校验、落盘第三方插件。
type RepoPluginInstaller struct {
	Client      *http.Client
	DataDir     string
	RawBase     string // 默认 https://raw.githubusercontent.com，测试可指向本地服务
	ArchiveBase string // 默认 https://codeload.github.com，测试可指向本地服务
	// MirrorBase 可选返回 ghmirror 加速线路；返回空串表示直连。
	MirrorBase func(context.Context) string
}

// NewRepoPluginInstaller 创建安装器。dataDir 是 SQLite 所在的数据目录，
// 插件源码落在 <dataDir>/plugin-sources/<id>/。
func NewRepoPluginInstaller(dataDir string, client *http.Client) *RepoPluginInstaller {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	return &RepoPluginInstaller{
		Client:      client,
		DataDir:     dataDir,
		RawBase:     "https://raw.githubusercontent.com",
		ArchiveBase: "https://codeload.github.com",
	}
}

func (i *RepoPluginInstaller) rawBase() string {
	if strings.TrimSpace(i.RawBase) == "" {
		return "https://raw.githubusercontent.com"
	}
	return strings.TrimRight(i.RawBase, "/")
}

func (i *RepoPluginInstaller) archiveBase() string {
	if strings.TrimSpace(i.ArchiveBase) == "" {
		return "https://codeload.github.com"
	}
	return strings.TrimRight(i.ArchiveBase, "/")
}

func (i *RepoPluginInstaller) fetch(ctx context.Context, rawURL string) ([]byte, error) {
	if i.MirrorBase != nil {
		rawURL = ghmirror.Rewrite(i.MirrorBase(ctx), rawURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := i.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("diana: 获取 %s 失败: HTTP %d", rawURL, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, repoPluginMaxTotalBytes))
}

func (i *RepoPluginInstaller) manifestURL(ref RepoPluginRef) string {
	return fmt.Sprintf("%s/%s/%s/%s/%s", i.rawBase(), ref.Owner, ref.Repo, ref.gitHubRef(), RepoPluginManifestFile)
}

func (i *RepoPluginInstaller) skillURL(ref RepoPluginRef) string {
	return fmt.Sprintf("%s/%s/%s/%s/%s", i.rawBase(), ref.Owner, ref.Repo, ref.gitHubRef(), RepoPluginEntryFile)
}

func (i *RepoPluginInstaller) fileURL(ref RepoPluginRef, path string) string {
	return fmt.Sprintf("%s/%s/%s/%s/%s", i.rawBase(), ref.Owner, ref.Repo, ref.gitHubRef(), path)
}

func (i *RepoPluginInstaller) archiveURL(ref RepoPluginRef) string {
	return fmt.Sprintf("%s/%s/%s/tar.gz/%s", i.archiveBase(), ref.Owner, ref.Repo, ref.gitHubRef())
}

// loadManifest 拉取并校验清单，返回原始字节与解析后的插件清单。
// 原始字节随插件落盘一份，进程重启后按本地副本恢复，不依赖网络。
func (i *RepoPluginInstaller) loadManifest(ctx context.Context, ref RepoPluginRef) ([]byte, repoPluginManifestFile, PluginManifest, error) {
	data, err := i.fetch(ctx, i.manifestURL(ref))
	if err != nil {
		if isNotFound(err) {
			return nil, repoPluginManifestFile{}, PluginManifest{}, fmt.Errorf("%w: %s", ErrRepoPluginManifest, ref.RepoURL())
		}
		return nil, repoPluginManifestFile{}, PluginManifest{}, err
	}
	manifestFile, err := decodeRepoPluginManifest(data)
	if err != nil {
		return nil, repoPluginManifestFile{}, PluginManifest{}, err
	}
	if err := manifestFile.validate(); err != nil {
		return nil, repoPluginManifestFile{}, PluginManifest{}, err
	}
	return data, manifestFile, manifestFile.pluginManifest(), nil
}

func isNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "HTTP 404")
}

// checkRemoteFiles 核对分发白名单里的文件在仓库里真实存在。目录条目无法
// 逐个核对（raw 地址不列目录），交给解包阶段处理；文件缺失按格式错误拒绝，
// 宁可误拒也不漏放。
func (i *RepoPluginInstaller) checkRemoteFiles(ctx context.Context, ref RepoPluginRef, files []string) error {
	for _, path := range files {
		if strings.HasSuffix(path, "/") {
			continue
		}
		if _, err := i.fetch(ctx, i.fileURL(ref, path)); err != nil {
			return fmt.Errorf("%w: 清单引用的文件 %s 在仓库中不存在或无法读取", ErrRepoPluginFormat, path)
		}
	}
	return nil
}

// Preview 只拉取清单、入口与声明文件做格式校验，不下载仓库归档，
// 返回渲染安装确认框所需的全部信息。
func (i *RepoPluginInstaller) Preview(ctx context.Context, rawURL string) (RepoPluginPreview, error) {
	ref, err := ParseGitHubRepoURL(rawURL)
	if err != nil {
		return RepoPluginPreview{}, err
	}
	_, manifestFile, manifest, err := i.loadManifest(ctx, ref)
	if err != nil {
		return RepoPluginPreview{}, err
	}
	skill, err := i.fetch(ctx, i.skillURL(ref))
	if err != nil {
		if isNotFound(err) {
			return RepoPluginPreview{}, fmt.Errorf("%w: %s", ErrRepoPluginSkill, RepoPluginEntryFile)
		}
		return RepoPluginPreview{}, err
	}
	if err := validateSkillFrontmatter(skill); err != nil {
		return RepoPluginPreview{}, err
	}
	files := manifestFile.normalizedFiles()
	if err := i.checkRemoteFiles(ctx, ref, files); err != nil {
		return RepoPluginPreview{}, err
	}
	preview := RepoPluginPreview{
		Source:      ref,
		Manifest:    manifest,
		Permissions: DescribeRepoPluginPermissions(manifest.Permissions),
		Files:       files,
		Risk: RepoPluginRisk{
			FloatingRef: strings.TrimSpace(ref.Ref) == "",
			Warnings:    []string{"第三方插件由仓库作者发布，Diana 不对其行为负责；插件获得的权限在启用期间持续生效。"},
		},
	}
	if preview.Risk.FloatingRef {
		preview.Risk.Warnings = append(preview.Risk.Warnings, "安装的是默认分支最新提交，内容与权限声明随时可能变化；发布者建议使用固定 tag 的链接。")
	}
	return preview, nil
}

// checkTagVersion 固定到 tag 安装时，清单版本必须与 tag 一致，防止
// 「tag 写着 v1.0.0、清单写着 1.0.1」的错位发布。40 位十六进制 ref 视为
// commit 固定，不做这一步核对。
func checkTagVersion(ref RepoPluginRef, version string) error {
	if ref.Ref == "" || gitHubCommitPattern.MatchString(ref.Ref) {
		return nil
	}
	tagVersion := strings.TrimPrefix(ref.Ref, "v")
	if tagVersion != version {
		return fmt.Errorf("%w: 清单版本 %s 与 tag %s 不一致", ErrRepoPluginFormat, version, ref.Ref)
	}
	return nil
}

// Install 校验、下载归档、落盘并返回构造好的插件实例与来源记录。
// 调用方负责把插件登记进 PluginManager 并持久化状态。
func (i *RepoPluginInstaller) Install(ctx context.Context, rawURL string) (*RepoPlugin, RepoPluginSource, error) {
	ref, err := ParseGitHubRepoURL(rawURL)
	if err != nil {
		return nil, RepoPluginSource{}, err
	}
	manifestData, manifestFile, manifest, err := i.loadManifest(ctx, ref)
	if err != nil {
		return nil, RepoPluginSource{}, err
	}
	if err := checkTagVersion(ref, manifest.Version); err != nil {
		return nil, RepoPluginSource{}, err
	}
	skill, err := i.fetch(ctx, i.skillURL(ref))
	if err != nil {
		if isNotFound(err) {
			return nil, RepoPluginSource{}, fmt.Errorf("%w: %s", ErrRepoPluginSkill, RepoPluginEntryFile)
		}
		return nil, RepoPluginSource{}, err
	}
	if err := validateSkillFrontmatter(skill); err != nil {
		return nil, RepoPluginSource{}, err
	}
	files := manifestFile.normalizedFiles()
	if err := i.checkRemoteFiles(ctx, ref, files); err != nil {
		return nil, RepoPluginSource{}, err
	}
	archive, err := i.fetch(ctx, i.archiveURL(ref))
	if err != nil {
		return nil, RepoPluginSource{}, fmt.Errorf("diana: 下载仓库归档失败: %w", err)
	}
	root := filepath.Join(i.DataDir, repoPluginSourceDir)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, RepoPluginSource{}, err
	}
	staging, err := os.MkdirTemp(root, ".staging-*")
	if err != nil {
		return nil, RepoPluginSource{}, err
	}
	defer os.RemoveAll(staging)
	if err := extractRepoArchive(archive, staging, files); err != nil {
		return nil, RepoPluginSource{}, err
	}
	target := filepath.Join(root, manifest.ID)
	if err := os.RemoveAll(target); err != nil {
		return nil, RepoPluginSource{}, err
	}
	if err := os.Rename(staging, target); err != nil {
		return nil, RepoPluginSource{}, err
	}
	// 清单副本落盘：重启恢复按本地副本读，不再走网络。
	if err := os.WriteFile(filepath.Join(target, RepoPluginManifestFile), manifestData, 0o644); err != nil {
		return nil, RepoPluginSource{}, err
	}
	source := RepoPluginSource{
		ID:          manifest.ID,
		Owner:       ref.Owner,
		Repo:        ref.Repo,
		Ref:         ref.Ref,
		Version:     manifest.Version,
		URL:         ref.RepoURL(),
		InstalledAt: time.Now(),
	}
	return NewRepoPlugin(target, source, manifest), source, nil
}

// extractRepoArchive 解开 GitHub tar.gz 归档，只落白名单内的常规文件。
// 归档第一层是 owner-repo-ref/ 目录，整体剥掉；符号链接、硬链接、越路径
// 一律跳过，文件数与大小都有硬上限。
func extractRepoArchive(archive []byte, dest string, whitelist []string) error {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return fmt.Errorf("diana: 仓库归档不是有效的 gzip: %w", err)
	}
	defer gz.Close()
	allowed := func(rel string) bool {
		for _, entry := range whitelist {
			if strings.HasSuffix(entry, "/") {
				if strings.HasPrefix(rel, entry) {
					return true
				}
				continue
			}
			if rel == entry || strings.HasPrefix(rel, entry+"/") {
				return true
			}
		}
		return false
	}
	reader := tar.NewReader(gz)
	files := 0
	var total int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("diana: 解包仓库归档失败: %w", err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		rel, ok := stripArchiveRoot(header.Name)
		if !ok || !allowed(rel) {
			continue
		}
		files++
		if files > repoPluginMaxFiles {
			return fmt.Errorf("diana: 插件文件数超过上限 %d", repoPluginMaxFiles)
		}
		total += header.Size
		if total > repoPluginMaxTotalBytes {
			return fmt.Errorf("diana: 插件总体积超过上限 %dMB", repoPluginMaxTotalBytes>>20)
		}
		if header.Size > repoPluginMaxFileBytes {
			return fmt.Errorf("diana: 插件文件 %s 超过单文件上限 %dMB", rel, repoPluginMaxFileBytes>>20)
		}
		target := filepath.Join(dest, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(file, io.LimitReader(reader, header.Size))
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
}

// stripArchiveRoot 剥掉归档第一层目录并做越路径检查。返回的相对路径
// 用 / 分隔，已经是清理后的形态。
func stripArchiveRoot(name string) (string, bool) {
	name = strings.TrimPrefix(filepath.ToSlash(name), "./")
	parts := strings.Split(name, "/")
	if len(parts) < 2 {
		return "", false
	}
	rel := strings.Join(parts[1:], "/")
	cleaned := filepath.ToSlash(filepath.Clean(rel))
	if cleaned == "." || cleaned == "" || strings.HasPrefix(cleaned, "../") || cleaned == ".." || filepath.IsAbs(cleaned) {
		return "", false
	}
	return cleaned, true
}

// RemoveRepoPlugin 删除已安装第三方插件的落盘目录。来源记录由调用方清。
func RemoveRepoPlugin(dataDir, id string) error {
	if strings.TrimSpace(id) == "" || strings.ContainsAny(id, `/\`) {
		return fmt.Errorf("diana: 非法插件 ID %q", id)
	}
	return os.RemoveAll(filepath.Join(dataDir, repoPluginSourceDir, id))
}

// LoadRepoPlugin 从落盘目录重新构造插件实例，用于进程启动时恢复已安装的
// 第三方插件。目录或清单缺失返回错误，调用方记录日志并跳过。
func LoadRepoPlugin(dataDir string, source RepoPluginSource) (*RepoPlugin, error) {
	dir := filepath.Join(dataDir, repoPluginSourceDir, source.ID)
	data, err := os.ReadFile(filepath.Join(dir, RepoPluginManifestFile))
	if err != nil {
		return nil, err
	}
	manifestFile, err := decodeRepoPluginManifest(data)
	if err != nil {
		return nil, err
	}
	if err := manifestFile.validate(); err != nil {
		return nil, err
	}
	return NewRepoPlugin(dir, source, manifestFile.pluginManifest()), nil
}
