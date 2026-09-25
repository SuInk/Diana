// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"
)

// 工作目录里的二进制文件（图片、音视频、压缩包）以前只有一种进来的方式：
// browser_screenshot。主人让机器人「把刚才那张图存下来」时，模型手里只有
// write_file，于是存了一份 .md 描述冒充图片，还回一句「存好了」。
//
// 这里放的是二进制文件共用的那几件事：按内容认类型、按类型定扩展名、
// 往工作目录里安全地落一份字节。save_to_workspace、manage_files 和生图落盘都走它，
// 不各写一套。

const (
	// WorkspaceDownloadsDir 是从聊天、网址存进来的文件默认落的目录。
	WorkspaceDownloadsDir = "downloads"
	// WorkspaceOutputsDir 是机器人自己产出的东西（生图、MCP 工具产物）默认落的目录。
	WorkspaceOutputsDir = "outputs"
	// WorkspaceTrashDir 是 manage_files delete 的去处：删除只是挪进这里，按时间戳分目录，
	// 误删了还能从这里捞回来。定期清理由维护任务按这个名字找。
	WorkspaceTrashDir = ".trash"
	// WorkspaceMediaMaxBytes 是存进工作目录的单个二进制文件上限，和 send_attachment
	// 发送本地文件、MCP 媒体暂存的上限一致：存得进来就发得出去。
	WorkspaceMediaMaxBytes = MCPMediaMaxBytes
)

// canonicalMediaExtensions 是类型到扩展名的固定表。不用 mime.ExtensionsByType：
// 它读系统的 mime 数据库，macOS 上 image/jpeg 排第一的是 .jfif，存出来的文件
// 连 send_attachment 自己的白名单都不认。
var canonicalMediaExtensions = map[string]string{
	"image/jpeg":                   ".jpg",
	"image/png":                    ".png",
	"image/gif":                    ".gif",
	"image/webp":                   ".webp",
	"image/bmp":                    ".bmp",
	"image/svg+xml":                ".svg",
	"image/avif":                   ".avif",
	"image/heic":                   ".heic",
	"image/tiff":                   ".tif",
	"image/x-icon":                 ".ico",
	"video/mp4":                    ".mp4",
	"video/webm":                   ".webm",
	"video/quicktime":              ".mov",
	"video/avi":                    ".avi",
	"video/x-msvideo":              ".avi",
	"audio/mpeg":                   ".mp3",
	"audio/ogg":                    ".ogg",
	"application/ogg":              ".ogg",
	"audio/wave":                   ".wav",
	"audio/wav":                    ".wav",
	"audio/x-wav":                  ".wav",
	"audio/flac":                   ".flac",
	"audio/mp4":                    ".m4a",
	"audio/aac":                    ".aac",
	"audio/amr":                    ".amr",
	"audio/silk":                   ".silk",
	"application/pdf":              ".pdf",
	"application/zip":              ".zip",
	"application/x-gzip":           ".gz",
	"application/gzip":             ".gz",
	"application/x-rar-compressed": ".rar",
	"application/x-7z-compressed":  ".7z",
	"application/json":             ".json",
	"text/plain":                   ".txt",
	"text/html":                    ".html",
	"text/xml":                     ".xml",
}

// equivalentExtensions 是同一种格式的几种常见写法：文件已经是其中之一时不改名。
// .jfif 故意不在 jpeg 这组里——它是要被纠正的那个。
var equivalentExtensions = map[string][]string{
	".jpg":  {".jpg", ".jpeg"},
	".tif":  {".tif", ".tiff"},
	".mp4":  {".mp4", ".m4v"},
	".ogg":  {".ogg", ".oga", ".opus"},
	".m4a":  {".m4a", ".m4b"},
	".html": {".html", ".htm"},
	".gz":   {".gz", ".tgz"},
}

// zipContainerExtensions 是本质上就是 zip 的格式：嗅探只会说 application/zip，
// 改成 .zip 反而把一份 docx 弄得打不开。
var zipContainerExtensions = map[string]bool{
	".docx": true, ".xlsx": true, ".pptx": true, ".odt": true, ".ods": true, ".odp": true,
	".epub": true, ".jar": true, ".apk": true, ".xpi": true,
}

// binaryFileExtensions 是 write_file 写不出来的格式：它只收字符串，写进去的永远是
// 文本，扩展名说是 .png 也只会得到一份打不开的「图片」。
var binaryFileExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".jfif": true, ".gif": true, ".webp": true,
	".bmp": true, ".ico": true, ".tif": true, ".tiff": true, ".avif": true, ".heic": true,
	".pdf": true, ".zip": true, ".gz": true, ".tgz": true, ".7z": true, ".rar": true, ".tar": true,
	".mp3": true, ".mp4": true, ".m4a": true, ".m4v": true, ".wav": true, ".ogg": true, ".oga": true,
	".opus": true, ".flac": true, ".aac": true, ".amr": true, ".silk": true, ".webm": true,
	".mov": true, ".avi": true, ".mkv": true,
	".doc": true, ".docx": true, ".xls": true, ".xlsx": true, ".ppt": true, ".pptx": true,
	".exe": true, ".dll": true, ".so": true, ".dylib": true, ".bin": true, ".wasm": true,
}

// IsBinaryFileExtension 报告这个文件名的扩展名是不是只能装二进制内容的格式。
func IsBinaryFileExtension(name string) bool {
	return binaryFileExtensions[strings.ToLower(path.Ext(filepath.ToSlash(name)))]
}

// SniffMediaType 按内容判断真实类型，返回不带参数的 MIME。
//
// 在 http.DetectContentType 之上补了几种它不认、聊天里却常见的：ISO 媒体容器里的
// mov/m4a/heic/avif、没有 ID3 头的 mp3、flac、QQ 语音的 silk 和 amr，以及 SVG
// ——SVG 是文本，标准库只会说 text/xml 或 text/plain。
func SniffMediaType(data []byte) string {
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	if len(head) >= 12 && string(head[4:8]) == "ftyp" {
		switch brand := string(head[8:12]); {
		case brand == "qt  ":
			return "video/quicktime"
		case brand == "M4A " || brand == "M4B ":
			return "audio/mp4"
		case brand == "avif" || brand == "avis":
			return "image/avif"
		case brand == "heic" || brand == "heix" || brand == "mif1" || brand == "msf1" || brand == "hevc":
			return "image/heic"
		}
	}
	switch {
	case bytes.HasPrefix(head, []byte("fLaC")):
		return "audio/flac"
	case bytes.HasPrefix(head, []byte("#!AMR")):
		return "audio/amr"
	case bytes.HasPrefix(head, []byte("#!SILK_V3")), bytes.HasPrefix(head, []byte("\x02#!SILK_V3")):
		return "audio/silk"
	}
	detected := http.DetectContentType(data)
	mediaType := strings.TrimSpace(strings.SplitN(detected, ";", 2)[0])
	if strings.HasPrefix(mediaType, "text/") && looksLikeSVG(head) {
		return "image/svg+xml"
	}
	if mediaType == "application/octet-stream" && len(head) >= 2 && head[0] == 0xFF && head[1]&0xE0 == 0xE0 {
		// MPEG 音频帧同步字。标准库只认带 ID3 标签的 mp3，裸帧的会落到这里。
		return "audio/mpeg"
	}
	return mediaType
}

// looksLikeSVG 看文本开头（跳过 XML 声明、注释和 DOCTYPE）是不是 <svg 根元素。
func looksLikeSVG(head []byte) bool {
	text := strings.ToLower(string(bytes.TrimPrefix(head, []byte("\xef\xbb\xbf"))))
	index := strings.Index(text, "<svg")
	if index < 0 {
		return false
	}
	// 根元素前面只允许 <?…?>、<!--…-->、<!DOCTYPE …> 这类东西；出现别的标签说明
	// svg 只是嵌在 HTML 里。
	prefix := text[:index]
	for {
		open := strings.IndexByte(prefix, '<')
		if open < 0 {
			return true
		}
		if open+1 >= len(prefix) || (prefix[open+1] != '?' && prefix[open+1] != '!') {
			return false
		}
		closing := strings.IndexByte(prefix[open:], '>')
		if closing < 0 {
			return false
		}
		prefix = prefix[open+closing+1:]
	}
}

// CanonicalMediaExtension 返回类型对应的标准扩展名（带点）；表里没有的返回空串。
func CanonicalMediaExtension(mediaType string) string {
	mediaType = strings.ToLower(strings.TrimSpace(strings.SplitN(mediaType, ";", 2)[0]))
	return canonicalMediaExtensions[mediaType]
}

// CorrectFileExtension 按真实类型纠正文件名的扩展名，返回新文件名。
//
// 已经是同一格式的某种写法（.jpeg、.tiff）就不动；本质是 zip 的办公文档不改成 .zip；
// 内容是文本、扩展名也不是二进制格式（.md、.csv、.log……）时同样不动，文本类型的
// 嗅探分不出这些。认不出类型时保留原扩展名，一个扩展名都没有才补 .bin。
func CorrectFileExtension(name, mediaType string) string {
	name = strings.TrimSpace(name)
	ext := path.Ext(name)
	lowerExt := strings.ToLower(ext)
	stem := strings.TrimSuffix(name, ext)
	canonical := CanonicalMediaExtension(mediaType)
	textual := strings.HasPrefix(mediaType, "text/") || mediaType == "application/json"
	switch {
	case canonical == "":
		if ext == "" {
			return name + ".bin"
		}
		return name
	case textual && ext != "" && !binaryFileExtensions[lowerExt]:
		return name
	case mediaType == "application/zip" && zipContainerExtensions[lowerExt]:
		return name
	case lowerExt == canonical:
		return name
	}
	for _, alias := range equivalentExtensions[canonical] {
		if lowerExt == alias {
			return name
		}
	}
	return stem + canonical
}

// ImageDimensions 读图片的宽高，不解码整张图。认不出的格式返回 ok=false。
func ImageDimensions(data []byte) (width, height int, ok bool) {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return 0, 0, false
	}
	return config.Width, config.Height, true
}

// looksBinaryContent 判断一段文件开头是不是二进制内容：文本类型、SVG、JSON 之外的
// 一律算二进制。NUL 字节在文本里不会出现，是最可靠的一条。
func looksBinaryContent(head []byte) bool {
	if len(head) == 0 {
		return false
	}
	if bytes.IndexByte(head, 0) >= 0 {
		// UTF-16 文本带 BOM 时标准库会认成 text/plain，NUL 在它里面是正常的。
		return !bytes.HasPrefix(head, []byte{0xFE, 0xFF}) && !bytes.HasPrefix(head, []byte{0xFF, 0xFE})
	}
	mediaType := SniffMediaType(head)
	return !strings.HasPrefix(mediaType, "text/") && mediaType != "image/svg+xml" && mediaType != "application/json"
}

// WorkspaceWriteOptions 控制 WriteWorkspaceBytes 遇到同名文件时怎么办。
type WorkspaceWriteOptions struct {
	// Overwrite 为 true 时覆盖同名文件；否则在文件名后面加 -2、-3……另起一个名字。
	Overwrite bool
}

// WriteWorkspaceBytes 把一份字节写进工作目录内的 rel，返回实际写入的相对路径。
//
// 路径先过 safePath（不许逃出工作目录、软链接也不行）和凭据名单，再经 os.OpenRoot
// 落盘：校验和写入之间就算有人换了软链接，写入也出不了工作目录。
func WriteWorkspaceBytes(cfg Config, rel string, data []byte, opts WorkspaceWriteOptions) (string, error) {
	root, err := filepath.Abs(cfg.WorkDir)
	if err != nil {
		return "", err
	}
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	if rel == "" || strings.HasSuffix(rel, "/") {
		return "", errors.New("需要一个具体的文件路径")
	}
	if len(data) > WorkspaceMediaMaxBytes {
		return "", fmt.Errorf("文件 %d 字节，超过 %d MB 上限", len(data), WorkspaceMediaMaxBytes>>20)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	protected := agentProtectedFiles(cfg)
	dir := path.Dir(rel)
	base := path.Base(rel)
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	handle, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer handle.Close()
	for attempt := 1; attempt <= 1000; attempt++ {
		name := base
		if attempt > 1 {
			name = fmt.Sprintf("%s-%d%s", stem, attempt, ext)
		}
		candidate := path.Join(dir, name)
		target, err := safePath(root, candidate)
		if err != nil {
			return "", err
		}
		if protected.blocked(target) {
			return "", errProtectedFile(candidate)
		}
		if isTrashPath(candidate) {
			return "", errTrashPath(candidate)
		}
		local := filepath.FromSlash(candidate)
		if parent := filepath.Dir(local); parent != "." {
			if err := handle.MkdirAll(parent, 0o755); err != nil {
				return "", err
			}
		}
		flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
		if opts.Overwrite {
			if info, statErr := handle.Lstat(local); statErr == nil && !info.Mode().IsRegular() {
				return "", fmt.Errorf("%s 已存在且不是普通文件", candidate)
			}
			flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
		}
		file, err := handle.OpenFile(local, flags, 0o644)
		if errors.Is(err, fs.ErrExist) && !opts.Overwrite {
			continue
		}
		if err != nil {
			return "", err
		}
		if _, err := file.Write(data); err != nil {
			_ = file.Close()
			_ = handle.Remove(local)
			return "", err
		}
		if err := file.Close(); err != nil {
			_ = handle.Remove(local)
			return "", err
		}
		return candidate, nil
	}
	return "", fmt.Errorf("%s 附近的同名文件太多，换个文件名再试", rel)
}

// isTrashPath 判断相对路径是不是回收站本身或它里面的东西。
func isTrashPath(rel string) bool {
	rel = path.Clean(filepath.ToSlash(strings.TrimSpace(rel)))
	return rel == WorkspaceTrashDir || strings.HasPrefix(rel, WorkspaceTrashDir+"/")
}

func errTrashPath(rel string) error {
	return fmt.Errorf("%s 在回收站 %s/ 里：回收站只由 manage_files delete 往里放，不能直接读写或挪动；要找回文件请让主人在服务器上处理", rel, WorkspaceTrashDir)
}
