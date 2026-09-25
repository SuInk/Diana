package agent

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/llm"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCP 返回的图片、音频和文件不进模型的文本上下文——base64 只会占满字数预算，模型
// 也读不出画面——但也不能丢：以前丢掉之后模型只拿到一句「返回了图片」，手里没有
// 任何东西能发出去。这里把它们暂存起来，文本结果里留一个 media_id：模型要看，
// MCPTool.ToolResultParts 按 id 把画面附上；要发，assistant 的 mcp_media 工具按 id
// 取出来发。
//
// MCPTool 是几台机器人、所有对话共用的同一个实例，不能像别的工具那样把本次结果挂在
// 实例上，所以按 id 放进进程级暂存区。id 是随机的，只出现在发起调用的那段对话里。

// MCPMediaMaxBytes 是单个媒体的上限，和 send_attachment 发本地文件的上限一致。
const MCPMediaMaxBytes = 32 << 20

const (
	mcpMediaIDPrefix = "mcpm_"
	// mcpMediaTTL 够一轮对话里看完再发，也够用户隔几分钟回一句「发一下」。
	mcpMediaTTL = 30 * time.Minute
	// mcpMediaStoreMaxBytes 超出后先淘汰最早存进来的，免得一个狂出图的服务把内存吃满。
	mcpMediaStoreMaxBytes = 256 << 20
	// mcpMediaPerCallLimit 挡住一次调用里列出一整个目录的情况。
	mcpMediaPerCallLimit = 16
	// mcpMediaPathClockSlack 容忍文件系统时间戳精度和两边时钟的细微差别。
	mcpMediaPathClockSlack = 2 * time.Second
	// mcpMediaModelImageMaxBytes 以内的图才附给模型看，再大就只给 id。
	mcpMediaModelImageMaxBytes = 8 << 20
)

type MCPMediaKind string

const (
	MCPMediaImage MCPMediaKind = "image"
	MCPMediaAudio MCPMediaKind = "audio"
	MCPMediaFile  MCPMediaKind = "file"
)

// MCPMedia 是一份暂存的 MCP 媒体。Data 是原始字节，不是 base64。
type MCPMedia struct {
	ID       string
	Kind     MCPMediaKind
	MIMEType string
	Name     string
	Server   string
	Data     []byte
	stored   time.Time
}

var mcpMediaStore = struct {
	sync.Mutex
	items map[string]*MCPMedia
	order []string
	bytes int
}{items: map[string]*MCPMedia{}}

var mcpMediaNow = time.Now

// StoreMCPMedia 暂存一份媒体并返回新的 media_id；ID 和存入时间由这里决定。
func StoreMCPMedia(media MCPMedia) (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	media.ID = mcpMediaIDPrefix + hex.EncodeToString(raw[:])
	media.stored = mcpMediaNow()
	mcpMediaStore.Lock()
	defer mcpMediaStore.Unlock()
	pruneMCPMediaLocked(len(media.Data))
	mcpMediaStore.items[media.ID] = &media
	mcpMediaStore.order = append(mcpMediaStore.order, media.ID)
	mcpMediaStore.bytes += len(media.Data)
	return media.ID, nil
}

// pruneMCPMediaLocked 清掉过期的，再按先进先出腾出 incoming 的空间。
func pruneMCPMediaLocked(incoming int) {
	now := mcpMediaNow()
	kept := mcpMediaStore.order[:0]
	for _, id := range mcpMediaStore.order {
		item := mcpMediaStore.items[id]
		if item == nil {
			continue
		}
		expired := now.Sub(item.stored) > mcpMediaTTL
		overflow := mcpMediaStore.bytes+incoming > mcpMediaStoreMaxBytes
		if expired || overflow {
			mcpMediaStore.bytes -= len(item.Data)
			delete(mcpMediaStore.items, id)
			continue
		}
		kept = append(kept, id)
	}
	mcpMediaStore.order = kept
}

// LookupMCPMedia 按 id 取暂存的媒体，过期或不存在时返回 false。
func LookupMCPMedia(id string) (MCPMedia, bool) {
	mcpMediaStore.Lock()
	defer mcpMediaStore.Unlock()
	item := mcpMediaStore.items[strings.TrimSpace(id)]
	if item == nil || mcpMediaNow().Sub(item.stored) > mcpMediaTTL {
		return MCPMedia{}, false
	}
	return *item, true
}

var mcpMediaIDPattern = regexp.MustCompile(mcpMediaIDPrefix + `[0-9a-f]{24}`)

// ToolResultParts 把这次结果里的图片附给下一轮模型。结果文本是这次调用自己的，
// 从里面认出 media_id 再去暂存区取，共用实例也不会串到别人的对话里。
func (t *MCPTool) ToolResultParts(output string) []llm.ContentPart {
	var parts []llm.ContentPart
	seen := map[string]bool{}
	for _, id := range mcpMediaIDPattern.FindAllString(output, -1) {
		if seen[id] {
			continue
		}
		seen[id] = true
		media, ok := LookupMCPMedia(id)
		if !ok || media.Kind != MCPMediaImage || !mcpModelReadableImage(media.MIMEType) || len(media.Data) > mcpMediaModelImageMaxBytes {
			continue
		}
		parts = append(parts, llm.ContentPart{
			Type:     llm.ContentPartImageURL,
			ImageURL: "data:" + media.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(media.Data),
			Detail:   "high",
		})
	}
	return parts
}

func mcpModelReadableImage(mimeType string) bool {
	switch mimeType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	}
	return false
}

// mcpMediaCollector 收集一次 tools/call 结果里的媒体。localDir 是 stdio 服务进程的
// 工作目录，HTTP 服务为空：那种服务说的路径在远端机器上，本机读到的只会是同名的
// 别的文件。
type mcpMediaCollector struct {
	server    string
	localDir  string
	protected protectedFiles
	callStart time.Time
	count     int
}

// binary 暂存一段二进制内容，返回写进文本结果的那一行。
func (c *mcpMediaCollector) binary(label, mimeType, name string, data []byte) string {
	mimeType = strings.TrimSpace(mimeType)
	if c == nil {
		return describeMCPBinaryContent(label, mimeType, len(data))
	}
	if len(data) > MCPMediaMaxBytes {
		return fmt.Sprintf("[MCP 返回了%s（%s，%d 字节），超过 %d MB，没有暂存，发不出去]", label, displayMIME(mimeType), len(data), MCPMediaMaxBytes>>20)
	}
	if c.count >= mcpMediaPerCallLimit {
		return fmt.Sprintf("[MCP 返回了%s（%s，%d 字节），这次调用返回的媒体超过 %d 个，没有暂存]", label, displayMIME(mimeType), len(data), mcpMediaPerCallLimit)
	}
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	media := MCPMedia{Kind: mcpMediaKindFor(mimeType), MIMEType: mimeType, Name: name, Server: c.server, Data: data}
	id, err := StoreMCPMedia(media)
	if err != nil {
		return describeMCPBinaryContent(label, mimeType, len(data))
	}
	c.count++
	line := fmt.Sprintf("[MCP 返回了%s（%s，%d 字节），media_id=%s", label, displayMIME(mimeType), len(data), id)
	if media.Kind == MCPMediaImage && mcpModelReadableImage(mimeType) && len(data) <= mcpMediaModelImageMaxBytes {
		line += "，画面已附给你看"
	}
	return line + "；要发到会话就用 mcp_media 工具和这个 media_id]"
}

// resourceLink 处理 resource_link：本机 file:// 读进来暂存，网上的图提示走 remote_image。
func (c *mcpMediaCollector) resourceLink(link *mcpsdk.ResourceLink) string {
	if c == nil || link == nil {
		return ""
	}
	parsed, err := url.Parse(strings.TrimSpace(link.URI))
	if err != nil {
		return ""
	}
	switch strings.ToLower(parsed.Scheme) {
	case "file":
		path := parsed.Path
		if parsed.Host != "" && parsed.Host != "localhost" {
			return ""
		}
		// 服务明确用 resource_link 指向的文件，是它有意交出来的，所以不要求是这次
		// 调用新生成的；类型、大小和凭据名单照样要过。
		if line, ok := c.localFile(path, link.MIMEType, false); ok {
			return line
		}
		return fmt.Sprintf("[MCP 给了本机文件 %s，但读不出来或不允许发送]", path)
	case "http", "https":
		if strings.HasPrefix(strings.ToLower(link.MIMEType), "image/") {
			return fmt.Sprintf("[MCP 给了网上的图片 %s；要发就用 remote_image 先 view 再 send]", link.URI)
		}
	}
	return ""
}

// textPaths 在文本结果里找本机文件路径（很多服务把图存到磁盘、只回一句「已保存到
// /tmp/x.png」），读进来暂存。只认这次调用期间新生成的媒体文件：否则群成员让一个
// 会回显输入的 MCP 念出「~/Pictures/私照.jpg」，就能把磁盘上早就有的文件要走。
func (c *mcpMediaCollector) textPaths(text string) []string {
	if c == nil || c.localDir == "" {
		return nil
	}
	var lines []string
	seen := map[string]bool{}
	for _, candidate := range mcpPathCandidates(text) {
		path := c.resolve(candidate)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		if line, ok := c.localFile(path, "", true); ok {
			lines = append(lines, line)
		}
	}
	return lines
}

func (c *mcpMediaCollector) resolve(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		if c.localDir == "" {
			return ""
		}
		path = filepath.Join(c.localDir, path)
	}
	return filepath.Clean(path)
}

func (c *mcpMediaCollector) localFile(path, mimeType string, requireFresh bool) (string, bool) {
	path = c.resolve(path)
	if path == "" || c.localDir == "" {
		return "", false
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	allowed := mcpMediaFileExts
	if requireFresh {
		allowed = mcpMediaPathExts
	}
	if _, ok := allowed[ext]; !ok {
		return "", false
	}
	if c.protected.blocked(path) {
		return "", false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > MCPMediaMaxBytes {
		return "", false
	}
	if requireFresh && info.ModTime().Before(c.callStart.Add(-mcpMediaPathClockSlack)) {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	if strings.TrimSpace(mimeType) == "" {
		mimeType = mime.TypeByExtension("." + ext)
		if cut, _, found := strings.Cut(mimeType, ";"); found {
			mimeType = cut
		}
	}
	return c.binary("文件 "+path, mimeType, filepath.Base(path), data), true
}

// mcpPathCandidates 把文本拆成看起来像文件路径的片段。只挑带媒体扩展名的，真假由
// localFile 去磁盘上核实。
func mcpPathCandidates(text string) []string {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\r', '"', '\'', '`', '<', '>', '(', ')', '[', ']', '{', '}', '|', ',', '，', '：', '、', '“', '”', '（', '）':
			return true
		}
		return false
	})
	var out []string
	for _, field := range fields {
		field = strings.TrimRight(field, ".。;；!！?？")
		field = strings.TrimPrefix(field, "file://")
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(field), "."))
		if _, ok := mcpMediaPathExts[ext]; ok {
			out = append(out, field)
		}
	}
	return out
}

// mcpMediaPathExts 是文本里的路径会被取回的类型：图片、音视频和 PDF。文本、配置
// 类文件不在里面，它们更可能是服务顺嘴提到的，而不是这次要交出来的结果。
var mcpMediaPathExts = map[string]struct{}{
	"png": {}, "jpg": {}, "jpeg": {}, "gif": {}, "webp": {}, "bmp": {}, "svg": {},
	"mp3": {}, "wav": {}, "ogg": {}, "oga": {}, "m4a": {}, "flac": {}, "aac": {}, "opus": {}, "silk": {}, "amr": {},
	"mp4": {}, "webm": {}, "mov": {},
	"pdf": {},
}

// mcpMediaFileExts 是 resource_link 指向时允许取回的类型，比文本路径宽：服务明确把
// 文件交出来了。可执行文件和脚本一律不收。
var mcpMediaFileExts = func() map[string]struct{} {
	exts := map[string]struct{}{
		"txt": {}, "md": {}, "markdown": {}, "json": {}, "csv": {}, "tsv": {}, "xml": {},
		"yaml": {}, "yml": {}, "toml": {}, "log": {}, "html": {}, "htm": {},
		"doc": {}, "docx": {}, "xls": {}, "xlsx": {}, "ppt": {}, "pptx": {},
		"tif": {}, "tiff": {},
		"zip": {}, "gz": {}, "tar": {}, "7z": {}, "rar": {},
	}
	for ext := range mcpMediaPathExts {
		exts[ext] = struct{}{}
	}
	return exts
}()

func mcpMediaKindFor(mimeType string) MCPMediaKind {
	lower := strings.ToLower(mimeType)
	switch {
	case strings.HasPrefix(lower, "image/"):
		return MCPMediaImage
	case strings.HasPrefix(lower, "audio/"):
		return MCPMediaAudio
	}
	return MCPMediaFile
}

func displayMIME(mimeType string) string {
	if strings.TrimSpace(mimeType) == "" {
		return "未知类型"
	}
	return mimeType
}

func mcpResourceName(uri string) string {
	parsed, err := url.Parse(strings.TrimSpace(uri))
	if err != nil {
		return ""
	}
	name := filepath.Base(parsed.Path)
	if name == "." || name == "/" {
		return ""
	}
	return name
}
