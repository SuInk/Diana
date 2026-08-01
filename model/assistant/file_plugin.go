package assistant

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	defaultFileParserMaxBytes = 2 * 1024 * 1024
	defaultFileParserMaxChars = 12000

	fileParserSettingMaxFileKB = "max_file_kb"
	fileParserSettingMaxChars  = "max_chars"
)

type FileParserPlugin struct {
	client   *http.Client
	maxBytes int64
	maxChars int
}

type fileRef struct {
	Name string
	URL  string
}

// NewFileParserPlugin 创建官方内置文件解析插件。
func NewFileParserPlugin(client *http.Client) *FileParserPlugin {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &FileParserPlugin{
		client:   client,
		maxBytes: defaultFileParserMaxBytes,
		maxChars: defaultFileParserMaxChars,
	}
}

// Manifest 返回文件解析插件清单。
func (p *FileParserPlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID:          "official.file-parser-go",
		Name:        "文件解析",
		Version:     "0.1.0",
		Description: "官方内置 Go 文件解析插件，支持 QQ 文件段和文本类文件链接，提取内容作为 LLM 上下文。",
		Official:    true,
		BuiltIn:     true,
		Permissions: []string{"network:http", "message:read", "file:parse"},
		Settings: []PluginSettingSpec{
			{
				Key:         fileParserSettingMaxFileKB,
				Label:       "最大文件大小",
				Description: "超过该大小的文件不下载、不解析。",
				Type:        PluginSettingTypeNumber,
				Default:     defaultFileParserMaxBytes / 1024,
				Min:         settingRange(64),
				Max:         settingRange(20 * 1024),
				Step:        64,
				Unit:        "KB",
			},
			{
				Key:         fileParserSettingMaxChars,
				Label:       "提取文本上限",
				Description: "单个文件提取进 LLM 上下文的最大字符数，超出截断。",
				Type:        PluginSettingTypeNumber,
				Default:     defaultFileParserMaxChars,
				Min:         settingRange(500),
				Max:         settingRange(100000),
				Step:        500,
				Unit:        "字符",
			},
		},
	}
}

// Handle 解析消息里的文件并生成 LLM 上下文。
func (p *FileParserPlugin) Handle(ctx context.Context, req PluginRequest) (*PluginResponse, error) {
	refs := collectFileRefs(req)
	if len(refs) == 0 {
		return nil, nil
	}

	// 直接构造插件（不经 PluginManager）时 Settings 为空，退回构造时的字段值。
	maxBytes := int64(req.Settings.Int(fileParserSettingMaxFileKB, int(p.maxBytes/1024))) * 1024
	maxChars := req.Settings.Int(fileParserSettingMaxChars, p.maxChars)

	parts := make([]string, 0, len(refs))
	for _, ref := range refs {
		parts = append(parts, p.parseRef(ctx, ref, maxBytes, maxChars))
	}
	return &PluginResponse{
		Handled: true,
		Context: "文件解析结果：\n" + strings.Join(parts, "\n"),
	}, nil
}

// collectFileRefs 从消息 segment 和文本链接中收集文件引用。
func collectFileRefs(req PluginRequest) []fileRef {
	refs := make([]fileRef, 0, 4)
	seen := map[string]struct{}{}

	add := func(name string, rawURL string) {
		rawURL = strings.TrimSpace(rawURL)
		if rawURL == "" {
			return
		}
		if !isSupportedFileURL(rawURL) {
			return
		}
		name = strings.TrimSpace(name)
		if name == "" {
			name = fileNameFromURL(rawURL)
		}
		key := rawURL
		if _, ok := seen[key]; ok {
			return
		}
		// 同一文件可能同时出现在 QQ file 段和消息文本链接里，这里按 URL 去重。
		seen[key] = struct{}{}
		refs = append(refs, fileRef{Name: name, URL: rawURL})
	}

	for _, segment := range req.Event.Segments {
		if segment.Type != "file" {
			continue
		}
		// NapCat/OneBot 不同版本字段名不完全一致，所以按常见 name/url/file/path 兜底读取。
		add(firstNonEmpty(segment.Data["name"], segment.Data["file"], segment.Data["filename"]), firstNonEmpty(
			segment.Data["url"],
			segment.Data["file"],
			segment.Data["path"],
		))
	}

	for _, raw := range extractURLs(req.Text) {
		// 文本里的直链也当作候选文件，方便用户直接丢 txt/md/json 链接给机器人解析。
		add(fileNameFromURL(raw), raw)
	}

	return refs
}

// parseRef 下载并解析单个文件引用。
func (p *FileParserPlugin) parseRef(ctx context.Context, ref fileRef, maxBytes int64, maxChars int) string {
	data, contentType, err := p.readURL(ctx, ref.URL, maxBytes)
	if err != nil {
		return fmt.Sprintf("- %s\n  地址：%s\n  状态：%v", ref.Name, ref.URL, err)
	}
	if !looksTextual(ref.Name, contentType, data) {
		return fmt.Sprintf("- %s\n  地址：%s\n  状态：暂不支持该文件类型", ref.Name, ref.URL)
	}
	text := sanitizeFileText(data, maxChars)
	if text == "" {
		return fmt.Sprintf("- %s\n  地址：%s\n  状态：未提取到文本", ref.Name, ref.URL)
	}
	return fmt.Sprintf("- %s\n  地址：%s\n  内容：\n%s", ref.Name, ref.URL, indentText(text, "  "))
}

// readURL 按大小限制读取远程文件内容。
func (p *FileParserPlugin) readURL(ctx context.Context, raw string, maxBytes int64) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "Diana/0.1")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return nil, resp.Header.Get("Content-Type"), fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	// 多读 1 字节判断是否超限，避免把大文件完整读进内存。
	limited := io.LimitReader(resp.Body, maxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, resp.Header.Get("Content-Type"), err
	}
	if int64(len(data)) > maxBytes {
		return nil, resp.Header.Get("Content-Type"), fmt.Errorf("file exceeds %d bytes", maxBytes)
	}
	return data, resp.Header.Get("Content-Type"), nil
}

var supportedFileExts = map[string]struct{}{
	".txt":      {},
	".md":       {},
	".markdown": {},
	".json":     {},
	".jsonl":    {},
	".csv":      {},
	".tsv":      {},
	".log":      {},
	".yaml":     {},
	".yml":      {},
	".toml":     {},
	".ini":      {},
	".conf":     {},
	".xml":      {},
	".html":     {},
	".htm":      {},
}

// isSupportedFileURL 判断 URL 是否是支持的文本类文件链接。
func isSupportedFileURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	// 先用扩展名做粗筛，真正下载后还会结合 Content-Type/UTF-8 再判断一次。
	_, ok := supportedFileExts[strings.ToLower(path.Ext(parsed.Path))]
	return ok
}

// looksTextual 根据扩展名、Content-Type 和内容判断文件是否像文本。
func looksTextual(name string, contentType string, data []byte) bool {
	contentType = strings.ToLower(contentType)
	if strings.Contains(contentType, "text/") ||
		strings.Contains(contentType, "json") ||
		strings.Contains(contentType, "xml") ||
		strings.Contains(contentType, "yaml") ||
		strings.Contains(contentType, "csv") {
		return true
	}
	if _, ok := supportedFileExts[strings.ToLower(path.Ext(name))]; ok {
		return true
	}
	// 没有可靠扩展名或 content-type 时，用 UTF-8 和 NUL 字节做最后的文本判断。
	if !utf8.Valid(data) {
		return false
	}
	return bytes.IndexByte(data, 0) < 0
}

// sanitizeFileText 清理并截断文件文本内容。
func sanitizeFileText(data []byte, maxChars int) string {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = stripControlChars(text)
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if maxChars > 0 && len(runes) > maxChars {
		// 按 rune 截断，避免中文文本被按字节截坏。
		text = string(runes[:maxChars]) + "\n...[已截断]"
	}
	return text
}

// stripControlChars 移除文件文本中的控制字符。
func stripControlChars(text string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\t':
			return r
		}
		if r < 32 {
			return -1
		}
		return r
	}, text)
}

// indentText 给多行文本加统一缩进。
func indentText(text string, prefix string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

// fileNameFromURL 从 URL 路径中提取文件名。
func fileNameFromURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "文件"
	}
	name := path.Base(parsed.Path)
	if name == "." || name == "/" || name == "" {
		return "文件"
	}
	return name
}

// firstNonEmpty 返回第一个去空白后非空的字符串。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
