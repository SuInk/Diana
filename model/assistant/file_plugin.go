// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/SuInk/diana/model/llm"
	"github.com/SuInk/diana/model/netguard"
)

const (
	fileParserPluginID             = "official.file-parser-go"
	defaultFileParserMaxBytes      = 32 * 1024 * 1024
	defaultFileParserMaxVideoBytes = 200 * 1024 * 1024
	defaultFileParserMaxChars      = 24000

	fileParserSettingMaxFileBytes      = "max_file_bytes"
	fileParserSettingMaxVideoFileBytes = "max_video_file_bytes"
	fileParserSettingMaxChars          = "max_chars"
	// 扫描版 PDF 的识别方式：vision 是现在的本机 PDFKit + 逐页视觉模型，deepseek 先整份
	// 交给 DeepSeek OCR，失败再退回 vision。
	fileParserSettingScannedOCR     = "scanned_ocr_backend"
	fileParserSettingDeepSeekAPIKey = "deepseek_api_key"
	fileParserScannedOCRVision      = "vision"
	fileParserScannedOCRDeepSeek    = "deepseek"
)

type FileParserPlugin struct {
	client        *http.Client
	maxBytes      int64
	maxChars      int
	pdfRenderer   pdfPageRenderer
	trustedClient *http.Client
	// deepSeekBaseURL 只给测试换成假服务，留空走官方地址。
	deepSeekBaseURL string
}

type fileRef struct {
	MD5       string
	Name      string
	URL       string
	LocalPath string
	FileID    string
	BusID     string
	GroupID   string
	Trusted   bool
	// Sniff 表示扩展名看不出类型，下载后按内容判断是否为文本，见 file_text.go。
	Sniff bool
	// MessageID/FileIndex 是文件所在的消息和它在消息里的序号（从 1 起），一次放不下
	// 时写进续读说明；正文里的直链没有消息可指，两者为空。
	MessageID string
	FileIndex int
}

// NewFileParserPlugin 创建官方内置文件解析插件。
func NewFileParserPlugin(client *http.Client) *FileParserPlugin {
	trustedClient := client
	if client == nil {
		client = netguard.NewPublicDownloadClient()
		trustedClient = &http.Client{Timeout: 60 * time.Second}
	}
	return &FileParserPlugin{
		client:        client,
		maxBytes:      defaultFileParserMaxBytes,
		maxChars:      defaultFileParserMaxChars,
		pdfRenderer:   newWASMPDFRenderer(),
		trustedClient: trustedClient,
	}
}

// Manifest 返回文件解析插件清单。
func (p *FileParserPlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID:          fileParserPluginID,
		Name:        "文件解析",
		Version:     "0.3.4",
		Description: "官方内置文件解析插件；按内容识别文本与编码（UTF-8/UTF-16/GBK），支持文本、代码、SVG、PDF、Office（docx/xlsx/pptx）、ODF、EPUB 和视频关键帧。PDF 在 macOS 使用 PDFKit/Vision，本地原生路径不可用时回退沙盒 PDFium 和视觉 LLM；扫描件也可以改用 DeepSeek OCR 整份识别。",
		Official:    true,
		BuiltIn:     true,
		Permissions: []string{"network:http", "message:read", "file:parse", "llm:multiple", "task:notify"},
		Settings: []PluginSettingSpec{
			{
				Key:         fileParserSettingMaxFileBytes,
				Label:       "文档文件大小上限",
				Description: "超过该大小的文本、PDF、Office、ODF 和 EPUB 不下载、不解析。",
				Type:        PluginSettingTypeSize,
				Default:     defaultFileParserMaxBytes,
				Min:         settingRange(64 * 1024),
				Max:         settingRange(256 * 1024 * 1024),
			},
			{
				Key:         fileParserSettingMaxVideoFileBytes,
				Label:       "视频文件抽帧上限",
				Description: "超过该大小的视频文件不下载、不抽取关键帧。",
				Type:        PluginSettingTypeSize,
				Default:     defaultFileParserMaxVideoBytes,
				Min:         settingRange(64 * 1024),
				Max:         settingRange(256 * 1024 * 1024),
			},
			{
				Key:         fileParserSettingMaxChars,
				Label:       "提取文本上限",
				Description: "一条消息里的文件合计提取进 LLM 上下文的最大字符数。按文件顺序、按整行装满为止，放不下的部分写明从第几行（PDF 是第几页）续读，模型需要时用历史媒体工具接着读。",
				Type:        PluginSettingTypeNumber,
				Default:     defaultFileParserMaxChars,
				Min:         settingRange(500),
				Max:         settingRange(100000),
				Step:        500,
				Unit:        "字符",
			},
			{
				Key:         fileParserSettingScannedOCR,
				Label:       "扫描版 PDF 识别方式",
				Description: "DeepSeek OCR 整份上传识别，按页计费、通常比逐页视觉模型快；接口失败时自动退回本机识别。",
				Type:        PluginSettingTypeSelect,
				Default:     fileParserScannedOCRVision,
				Options: []PluginSettingOption{
					{Value: fileParserScannedOCRVision, Label: "本机识别 + 视觉模型逐页转写"},
					{Value: fileParserScannedOCRDeepSeek, Label: "DeepSeek OCR 接口"},
				},
			},
			{
				Key:         fileParserSettingDeepSeekAPIKey,
				Label:       "DeepSeek API Key",
				Description: "选 DeepSeek OCR 时必填，和对话用的是同一个 Key。",
				Type:        PluginSettingTypeString,
				Default:     "",
				Secret:      true,
			},
		},
	}
}

// fileParserRemoteOCR 按设置返回扫描件的外部 OCR；没选 DeepSeek 或没填 Key 时为 nil。
func (p *FileParserPlugin) fileParserRemoteOCR(settings SettingValues) documentRemoteOCR {
	key := strings.TrimSpace(settings.String(fileParserSettingDeepSeekAPIKey, ""))
	if settings.String(fileParserSettingScannedOCR, fileParserScannedOCRVision) != fileParserScannedOCRDeepSeek || key == "" {
		return nil
	}
	baseURL := p.deepSeekBaseURL
	return func(ctx context.Context, document scannedPDFDocument) ([]string, error) {
		name := document.Name
		if !strings.HasSuffix(strings.ToLower(name), ".pdf") {
			name += ".pdf"
		}
		// 不用 trustedClient：它整体 60 秒超时，DeepSeek 识别一份文件最多要 180 秒。
		pages, err := llm.DeepSeekOCR(ctx, llm.ProviderConfig{APIKey: key, BaseURL: baseURL}, llm.DeepSeekOCRRequest{Data: document.Data, Filename: name})
		if err != nil {
			return nil, err
		}
		texts := make([]string, len(pages))
		for i, page := range pages {
			texts[i] = page.Text
		}
		return texts, nil
	}
}

func fileParserVideoMaxBytes(settings SettingValues) int64 {
	return settings.Bytes(fileParserSettingMaxVideoFileBytes, defaultFileParserMaxVideoBytes)
}

// Handle 解析消息里的文件并生成 LLM 上下文。
func (p *FileParserPlugin) Handle(ctx context.Context, req PluginRequest) (*PluginResponse, error) {
	refs := collectFileRefs(req)
	if len(refs) == 0 {
		return nil, nil
	}
	maxBytes := req.Settings.Bytes(fileParserSettingMaxFileBytes, p.maxBytes)
	// 照 Pi 的做法按文件顺序、按整行装满这一轮的预算：前面的文件给全文，装不下的那个
	// 给到最后一个装得下的整行，再后面的只列名字，都写明从哪续读。以前按文件数平分
	// 预算、每个文件掐头去尾，中间丢了多少模型只能看到一句「中间省略」，也没法接着读。
	budget := req.Settings.Int(fileParserSettingMaxChars, p.maxChars)
	used := 0

	parts := make([]string, 0, len(refs))
	scannedDocuments := make([]scannedPDFDocument, 0, len(refs))
	for _, ref := range refs {
		if budget > 0 && used >= budget {
			parts = append(parts, fmt.Sprintf("- %s\n  状态：这一轮的文件正文已经装满 %d 字，这个文件没有展开。%s", ref.Name, budget, fileContinuationHint(ref, textLineWindow{Start: 1}, 0)))
			continue
		}
		remaining := 0
		if budget > 0 {
			remaining = budget - used
		}
		result := p.parseRefLines(ctx, req.Channel, ref, maxBytes, 1, remaining)
		used += result.Used
		if result.Partial {
			used = max(used, budget)
		}
		if result.Context != "" {
			parts = append(parts, result.Context)
		}
		if result.ScannedPDF != nil {
			scannedDocuments = append(scannedDocuments, *result.ScannedPDF)
		}
	}
	contextText := ""
	if len(parts) > 0 {
		contextText = fileContentNotice + strings.Join(parts, "\n")
	}
	if len(scannedDocuments) > 0 {
		return &PluginResponse{
			Handled: true,
			Tasks: []PluginTask{
				newDocumentOCRTask(p.pdfRenderer, req.Text, scannedDocuments, contextText, p.fileParserRemoteOCR(req.Settings)),
			},
		}, nil
	}
	return &PluginResponse{
		Handled: true,
		Context: contextText,
	}, nil
}

// collectFileRefs 从消息 segment 和文本链接中收集文件引用。
func collectFileRefs(req PluginRequest) []fileRef {
	refs := make([]fileRef, 0, 4)
	seen := map[string]struct{}{}

	add := func(ref fileRef) {
		ref.Name = strings.TrimSpace(ref.Name)
		ref.URL = strings.TrimSpace(ref.URL)
		ref.LocalPath = strings.TrimSpace(strings.TrimPrefix(ref.LocalPath, "file://"))
		ref.FileID = strings.TrimSpace(ref.FileID)
		ref.BusID = strings.TrimSpace(ref.BusID)
		ref.GroupID = strings.TrimSpace(ref.GroupID)
		if ref.Name == "" {
			ref.Name = fileNameFromURL(firstNonEmpty(ref.URL, ref.LocalPath, ref.FileID))
		}
		if ref.Name == "" || ref.Name == "." {
			ref.Name = "文件"
		}
		if !ref.Sniff && !isSupportedFileName(ref.Name) && !isSupportedFileURL(ref.URL) && !isSupportedLocalFile(ref.LocalPath) {
			return
		}
		key := firstNonEmpty(ref.URL, ref.LocalPath, ref.FileID, ref.Name)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		refs = append(refs, ref)
	}

	addURL := func(name string, rawURL string) {
		rawURL = strings.TrimSpace(rawURL)
		if rawURL == "" {
			return
		}
		if !isSupportedFileURL(rawURL) {
			return
		}
		add(fileRef{Name: name, URL: rawURL})
	}

	addSegments := func(groupID, messageID string, segments []MessageSegment) {
		fileIndex := 0
		for _, segment := range segments {
			if segment.Type != "file" {
				continue
			}
			fileIndex++
			name := firstNonEmpty(segment.Data["name"], segment.Data["filename"], segment.Data["file"])
			raw := firstNonEmpty(segment.Data["url"], segment.Data["download_url"], segment.Data["file_url"], segment.Data["file"], segment.Data["path"])
			ref := fileRef{
				Name:    name,
				FileID:  firstNonEmpty(segment.Data["file_id"], segment.Data["id"], segment.Data["fid"]),
				BusID:   firstNonEmpty(segment.Data["busid"], segment.Data["bus_id"]),
				GroupID: groupID,
				URL:     raw,
				Trusted: true,
				Sniff:   isSniffableFileName(name, segment.Data["size"]),

				MessageID: strings.TrimSpace(messageID),
				FileIndex: fileIndex,
			}
			if req.Event.Platform == PlatformOneBotV11 {
				ref.MD5 = explicitMediaMD5(segment.Data)
			}
			if normalizedFileURL(raw) != "" {
				ref.URL = raw
			} else if isSupportedLocalFile(raw) || (ref.Sniff && filepath.IsAbs(strings.TrimPrefix(raw, "file://"))) {
				ref.URL = ""
				ref.LocalPath = strings.TrimSpace(strings.TrimPrefix(raw, "file://"))
			} else {
				ref.URL = ""
			}
			add(ref)
		}
	}

	addSegments(req.Event.GroupID, req.Event.MessageID, req.Event.Segments)
	if req.Event.Quoted != nil {
		addSegments(firstNonEmpty(req.Event.Quoted.GroupID, req.Event.GroupID), req.Event.Quoted.MessageID, req.Event.Quoted.Segments)
	}
	// 以前这里用一张 20 个词的表（文件/文档/附件/讲义…外加 pdf、docx、csv 等扩展名）
	// 去正文里匹配，命中就把最近所有消息里的附件全都拉进来解析。那张表匹配的是聊天
	// 散文而不是文件名，实际是在用关键词猜「用户是不是在指之前发过的文件」——说法一换
	// 就漏，词命中了又会平白下载并解析一堆无关文件。
	//
	// 「刚才那个文件」这类指代现在由前置指代解析（LLM）处理：它会把选中的历史消息填进
	// event.Quoted，上面那次 addSegments 就已经把文件带进来了，这里不再需要词表兜底。

	for _, raw := range extractURLs(req.Text) {
		// 文本里的直链也当作候选文件，方便用户直接丢 txt/md/json 链接给机器人解析。
		addURL(fileNameFromURL(raw), raw)
	}

	return refs
}

type parsedFileRef struct {
	Context    string
	ScannedPDF *scannedPDFDocument
	// Used 是这次放进上下文的正文字数，Handle 据此算后面的文件还剩多少预算。
	Used int
	// Partial 说明这个文件没给完：后面的文件不再展开，免得续读时顺序乱掉。
	Partial bool
}

// parseRefLines 下载并解析单个文件引用，从第 startLine 行起按整行装进 budget（<=0
// 不限）。正文一律给原文整行，不掐头去尾；没给完时在正文后写明给到哪、从哪续读。
func (p *FileParserPlugin) parseRefLines(ctx context.Context, channel Channel, ref fileRef, maxBytes int64, startLine, budget int) parsedFileRef {
	ref = p.resolveOneBotFile(ctx, channel, ref)
	if ref.Sniff {
		maxBytes = min(maxBytes, fileParserSniffMaxBytes)
	}
	data, source, contentType, err := p.readRef(ctx, ref, maxBytes)
	if err != nil {
		return parsedFileRef{Context: fmt.Sprintf("- %s\n  地址：%s\n  状态：%v", ref.Name, source, err)}
	}
	// header 是正文前的「类型」「编码」等说明行；pdfPages 不为 0 时续读按页给，
	// pdfMore 说明 PDF 后面还有页没提取（提取到预算就停了）。
	content := func(header, text string, pdfPages int, pdfMore bool) parsedFileRef {
		window := takeTextLines(text, startLine, fileParserLineBudget(budget), plainRunes)
		body := fmt.Sprintf("- %s\n  地址：%s%s\n%s", ref.Name, source, header, fileContentBlock(ref.Name, window.Text))
		complete := window.Complete() && !pdfMore
		if !complete || !window.Whole() {
			if window.Complete() && pdfMore {
				body += fmt.Sprintf("\n  范围：给到第 %d 页为止。", pdfContinuationPage(window, false)-1)
			} else {
				body += "\n  范围：" + window.Range() + "。"
			}
			if !complete {
				hintWindow := window
				if pdfPages > 0 {
					hintWindow.Start = pdfContinuationPage(window, !window.Complete())
				}
				body += "后面的内容没有放进来，不要把这段当成全文。" + fileContinuationHint(ref, hintWindow, pdfPages)
			}
		}
		return parsedFileRef{Context: body, Used: plainRunes(window.Text), Partial: !complete}
	}
	if looksPDF(ref.Name, contentType, data) {
		nativeResult, nativeAvailable, nativeErr := runNativeMacPDF(ctx, data, "text", defaultOCRMaxPagesPerFile, math.MaxInt32)
		if nativeAvailable && nativeErr == nil && nativeMacPDFHasText(nativeResult) {
			// 每页不按字数截（以前每页截到 12000 字），超出单次上限的按整行分页续读。
			pages := max(nativeResult.TotalPages, 1)
			return content("\n  类型：PDF（macOS PDFKit 文本层）", formatNativeMacPDFText(nativeResult, 0), pages, nativeResult.ProcessedPages < pages)
		}
		// 从第 1 行读时攒够预算就停，免得几百页的 PDF 为一页的上下文全部过一遍；
		// 续读时要数到起始行，整份读完。
		extractLimit := budget
		if startLine > 1 {
			extractLimit = 0
		}
		extracted := extractPDFText(ctx, p.pdfRenderer, data, extractLimit)
		if extracted.NeedsOCR() {
			hash := sha256.Sum256(data)
			return parsedFileRef{ScannedPDF: &scannedPDFDocument{
				Name:   ref.Name,
				Source: source,
				Data:   data,
				Hash:   hex.EncodeToString(hash[:]),
			}}
		}
		return content("\n  类型：PDF", sanitizeFileTextString(extracted.Text, 0), max(extracted.PageCount, 1), extracted.Stopped)
	}
	if kind := officeKindFromName(ref.Name); kind != "" && looksZipArchive(data) {
		extracted, err := extractOfficeText(kind, data, 0)
		if err != nil {
			return parsedFileRef{Context: fmt.Sprintf("- %s\n  地址：%s\n  类型：%s\n  状态：解析失败：%v", ref.Name, source, officeKindLabel(kind), err)}
		}
		text := sanitizeFileTextString(extracted, 0)
		if text == "" {
			return parsedFileRef{Context: fmt.Sprintf("- %s\n  地址：%s\n  类型：%s\n  状态：未提取到文本", ref.Name, source, officeKindLabel(kind))}
		}
		return content("\n  类型："+officeKindLabel(kind), text, 0, false)
	}
	if !looksTextual(ref.Name, contentType, data) {
		if ref.Sniff {
			return parsedFileRef{Context: fmt.Sprintf("- %s\n  地址：%s\n  状态：扩展名不认识，内容也不是文本，暂不支持", ref.Name, source)}
		}
		return parsedFileRef{Context: fmt.Sprintf("- %s\n  地址：%s\n  状态：暂不支持该文件类型", ref.Name, source)}
	}
	raw := string(data)
	encodingNote := ""
	if decoded, encoding, ok := decodeFileText(data); ok {
		raw = decoded
		if encoding != "UTF-8" {
			encodingNote = "\n  编码：" + encoding
		}
	}
	if looksMarkup(ref.Name, contentType) {
		// HTML 直接塞原始标签会把上下文撑爆，先抽正文。
		if text := sanitizeFileTextString(stripMarkupText([]byte(raw), 0), 0); text != "" {
			return content("\n  类型：网页文本"+encodingNote, text, 0, false)
		}
	}
	text := sanitizeFileTextString(raw, 0)
	if text == "" {
		return parsedFileRef{Context: fmt.Sprintf("- %s\n  地址：%s\n  状态：未提取到文本", ref.Name, source)}
	}
	if strings.EqualFold(path.Ext(ref.Name), ".svg") {
		// 标明是源码：模型拿到的是图形元素而不是画面，要从 path、文字和注释里推断画了什么。
		return content("\n  类型：SVG 矢量图源码"+encodingNote, text, 0, false)
	}
	return content(encodingNote, text, 0, false)
}

// fileParserLineBudget 把「<=0 不限」换成 takeTextLines 认的上限。
func fileParserLineBudget(budget int) int {
	if budget <= 0 {
		return math.MaxInt
	}
	return budget
}

// fileContinuationHint 写明没放进来的内容怎么接着读：消息里的文件交给历史媒体工具，
// PDF 按页（pdfPages 不为 0 时 window.Start 是续读页）、其余按行；正文里的直链没有
// 消息可指，只能让用户换种方式发。
func fileContinuationHint(ref fileRef, window textLineWindow, pdfPages int) string {
	if ref.MessageID == "" || ref.FileIndex <= 0 {
		return "这是正文里的链接，没有续读入口；需要后面的内容就请用户把文件直接发上来。"
	}
	target := fmt.Sprintf("用 %s 传 message_id=%s、file_index=%d", dianaHistoryImagesToolName, ref.MessageID, ref.FileIndex)
	if pdfPages > 0 {
		return fmt.Sprintf("PDF 共 %d 页，%s、pdf_page=%d 续读，加 pdf_page_end 一次读连续多页。", pdfPages, target, max(window.Start, 1))
	}
	return fmt.Sprintf("%s、start_line=%d 续读。", target, max(window.NextLine(), window.Start))
}

var pdfPageHeader = regexp.MustCompile(`(?m)^第 (\d+) 页：$`)

// pdfContinuationPage 是续读应从哪一页开始。partial 表示已给出的最后一页可能只给了
// 一部分，从它重新读；否则从它的下一页读。一页都没给出就从第 1 页读。
func pdfContinuationPage(window textLineWindow, partial bool) int {
	last := 0
	for _, match := range pdfPageHeader.FindAllStringSubmatch(window.Text, -1) {
		if n, err := strconv.Atoi(match[1]); err == nil {
			last = n
		}
	}
	if last == 0 {
		return 1
	}
	if partial {
		return last
	}
	return last + 1
}

func (p *FileParserPlugin) readRef(ctx context.Context, ref fileRef, maxBytes int64) ([]byte, string, string, error) {
	if ref.URL != "" {
		client := p.client
		scope := "public"
		if ref.Trusted && p.trustedClient != nil {
			client = p.trustedClient
			scope = "adapter"
		}
		if ref.Trusted {
			path, contentType, release, err := acquireMediaDownload(ctx, client, ref.URL, ref.Name, ref.MD5, scope, maxBytes)
			defer release()
			if err != nil {
				return nil, ref.URL, "", err
			}
			data, _, err := p.readLocal(path, maxBytes)
			return data, ref.URL, contentType, err
		}
		data, contentType, err := p.readURLWithClient(ctx, client, ref.URL, maxBytes)
		return data, ref.URL, contentType, err
	}
	if ref.LocalPath != "" {
		data, contentType, err := p.readLocal(ref.LocalPath, maxBytes)
		return data, ref.LocalPath, contentType, err
	}
	return nil, firstNonEmpty(ref.Name, ref.FileID), "", fmt.Errorf("缺少可下载地址")
}

func (p *FileParserPlugin) resolveOneBotFile(ctx context.Context, channel Channel, ref fileRef) fileRef {
	if channel == nil || ref.FileID == "" || ref.URL != "" || ref.LocalPath != "" {
		return ref
	}
	callCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	requests := oneBotFileResolveRequests(ref)
	for _, req := range requests {
		data, err := channel.CallAPI(callCtx, req.action, req.params)
		if err == nil {
			if next := fileRefFromOneBotData(ref, data); next.URL != "" || next.LocalPath != "" {
				return next
			}
		}
	}
	if refreshed, ok := refreshOneBotFileFromGroupHistory(callCtx, channel, ref); ok {
		ref = refreshed
		if ref.URL != "" || ref.LocalPath != "" {
			return ref
		}
		requests = oneBotFileResolveRequests(ref)
	}
	for _, req := range requests {
		if req.action == "get_group_file_url" {
			continue
		}
		data, err := channel.CallAPI(callCtx, req.action, req.params)
		if err != nil {
			continue
		}
		if next := fileRefFromOneBotData(ref, data); next.URL != "" || next.LocalPath != "" {
			return next
		}
	}
	return ref
}

func refreshOneBotFileFromGroupHistory(ctx context.Context, channel Channel, ref fileRef) (fileRef, bool) {
	if strings.TrimSpace(ref.GroupID) == "" {
		return ref, false
	}
	messageSeq := ""
	for page := 0; page < 6; page++ {
		params := map[string]any{
			"group_id":        oneBotIDParam(ref.GroupID),
			"count":           100,
			"reverse_order":   messageSeq != "",
			"disable_get_url": true,
		}
		if messageSeq != "" {
			params["message_seq"] = messageSeq
		}
		data, err := channel.CallAPI(ctx, "get_group_msg_history", params)
		if err != nil {
			return ref, false
		}
		if next, ok := fileRefFromOneBotHistory(ref, data); ok {
			return next, true
		}
		nextSeq := oldestOneBotHistoryMessageID(data)
		if nextSeq == "" || nextSeq == messageSeq {
			break
		}
		messageSeq = nextSeq
	}
	return ref, false
}

func oldestOneBotHistoryMessageID(data map[string]any) string {
	for _, key := range []string{"messages", "items", "list"} {
		switch messages := data[key].(type) {
		case []any:
			if len(messages) > 0 {
				if message, ok := messages[0].(map[string]any); ok {
					return stringFromAny(message["message_id"])
				}
			}
		case []map[string]any:
			if len(messages) > 0 {
				return stringFromAny(messages[0]["message_id"])
			}
		}
	}
	if nested, ok := data["data"].(map[string]any); ok {
		return oldestOneBotHistoryMessageID(nested)
	}
	return ""
}

type oneBotFileResolveRequest struct {
	action string
	params map[string]any
}

func oneBotFileResolveRequests(ref fileRef) []oneBotFileResolveRequest {
	params := make([]oneBotFileResolveRequest, 0, 4)
	seen := map[string]struct{}{}
	appendRequest := func(action, key, value string, extra map[string]any) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		dedupeKey := action + "\x00" + key + "\x00" + value
		if _, ok := seen[dedupeKey]; ok {
			return
		}
		seen[dedupeKey] = struct{}{}
		requestParams := map[string]any{key: value}
		for extraKey, extraValue := range extra {
			requestParams[extraKey] = extraValue
		}
		params = append(params, oneBotFileResolveRequest{action: action, params: requestParams})
	}
	if ref.GroupID != "" && strings.TrimSpace(ref.FileID) != "" {
		extra := map[string]any{"group_id": oneBotIDParam(ref.GroupID)}
		if ref.BusID != "" {
			extra["busid"] = ref.BusID
			extra["bus_id"] = ref.BusID
		}
		appendRequest("get_group_file_url", "file_id", ref.FileID, extra)
	}
	if name := strings.TrimSpace(ref.Name); name != "" && name != "文件" {
		// Some OneBot clients keep the mapping behind incoming file IDs in memory. After a
		// restart, its local filename search can still recover an already cached file.
		appendRequest("get_file", "file", name, nil)
	}
	appendRequest("get_file", "file_id", ref.FileID, nil)
	appendRequest("get_file", "file", ref.FileID, nil)
	return params
}

func fileRefFromOneBotData(base fileRef, data map[string]any) fileRef {
	digests := make(map[string]string)
	for _, key := range []string{"md5", "file_md5", "fileMd5"} {
		digests[key] = stringFromAny(data[key])
	}
	if digest := explicitMediaMD5(digests); digest != "" {
		base.MD5 = digest
	}
	for _, key := range []string{"name", "file_name", "filename"} {
		if value := stringFromAny(data[key]); strings.TrimSpace(value) != "" && base.Name == "" {
			base.Name = value
		}
	}
	for _, key := range []string{"url", "download_url", "file_url"} {
		if value := stringFromAny(data[key]); normalizedFileURL(value) != "" {
			base.URL = strings.TrimSpace(value)
			base.LocalPath = ""
			return base
		}
	}
	for _, key := range []string{"file", "path", "file_path"} {
		value := strings.TrimSpace(strings.TrimPrefix(stringFromAny(data[key]), "file://"))
		if isSupportedLocalFile(value) {
			base.LocalPath = value
			base.URL = ""
			return base
		}
		if normalizedFileURL(value) != "" {
			base.URL = value
			base.LocalPath = ""
			return base
		}
	}
	return base
}

func fileRefFromOneBotHistory(base fileRef, data map[string]any) (fileRef, bool) {
	for _, key := range []string{"messages", "message", "segments", "data"} {
		if next, ok := fileRefFromOneBotValue(base, data[key]); ok {
			return next, true
		}
	}
	return base, false
}

func fileRefFromOneBotValue(base fileRef, value any) (fileRef, bool) {
	switch item := value.(type) {
	case []any:
		for i := len(item) - 1; i >= 0; i-- {
			if next, ok := fileRefFromOneBotValue(base, item[i]); ok {
				return next, true
			}
		}
	case []map[string]any:
		for i := len(item) - 1; i >= 0; i-- {
			if next, ok := fileRefFromOneBotValue(base, item[i]); ok {
				return next, true
			}
		}
	case map[string]any:
		if strings.EqualFold(strings.TrimSpace(stringFromAny(item["type"])), "file") {
			if segmentData, ok := item["data"].(map[string]any); ok {
				return fileRefFromOneBotSegment(base, segmentData)
			}
		}
		for _, key := range []string{"message", "segments", "data"} {
			if next, ok := fileRefFromOneBotValue(base, item[key]); ok {
				return next, true
			}
		}
	}
	return base, false
}

func fileRefFromOneBotSegment(base fileRef, data map[string]any) (fileRef, bool) {
	name := firstNonEmpty(stringFromAny(data["name"]), stringFromAny(data["filename"]), stringFromAny(data["file"]))
	fileID := firstNonEmpty(stringFromAny(data["file_id"]), stringFromAny(data["id"]), stringFromAny(data["fid"]))
	baseName := strings.TrimSpace(base.Name)
	if baseName != "" && name != "" && !strings.EqualFold(filepath.Base(name), filepath.Base(baseName)) {
		return base, false
	}
	if baseName == "" && strings.TrimSpace(base.FileID) != "" && fileID != "" && fileID != base.FileID {
		return base, false
	}
	if name != "" {
		base.Name = name
	}
	if fileID != "" {
		base.FileID = fileID
	}
	if busID := firstNonEmpty(stringFromAny(data["busid"]), stringFromAny(data["bus_id"])); busID != "" {
		base.BusID = busID
	}
	base = fileRefFromOneBotData(base, data)
	return base, true
}

func (p *FileParserPlugin) readURLWithClient(ctx context.Context, client *http.Client, raw string, maxBytes int64) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "Diana/0.1")
	resp, err := client.Do(req)
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

func (p *FileParserPlugin) readLocal(localPath string, maxBytes int64) ([]byte, string, error) {
	localPath = strings.TrimSpace(strings.TrimPrefix(localPath, "file://"))
	if localPath == "" || !filepath.IsAbs(localPath) {
		return nil, "", fmt.Errorf("invalid local file path")
	}
	info, err := os.Stat(localPath)
	if err != nil {
		return nil, "", err
	}
	if info.IsDir() {
		return nil, "", fmt.Errorf("path is a directory")
	}
	if info.Size() > maxBytes {
		return nil, "", fmt.Errorf("file exceeds %d bytes", maxBytes)
	}
	data, err := os.ReadFile(localPath)
	if err != nil {
		return nil, "", err
	}
	return data, "", nil
}

var supportedFileExts = map[string]struct{}{
	// 纯文本与常见配置
	".txt":        {},
	".md":         {},
	".markdown":   {},
	".rst":        {},
	".tex":        {},
	".json":       {},
	".jsonl":      {},
	".ndjson":     {},
	".csv":        {},
	".tsv":        {},
	".log":        {},
	".yaml":       {},
	".yml":        {},
	".toml":       {},
	".ini":        {},
	".cfg":        {},
	".conf":       {},
	".properties": {},
	".xml":        {},
	// SVG 是 XML 写的矢量图，读源码就能知道画了什么；按图片处理反而拿不到内容。
	".svg":  {},
	".html": {},
	".htm":  {},
	".srt":  {},
	".vtt":  {},
	".ass":  {},
	// 代码：群里最常见的是直接丢一段脚本或源文件求解释
	".go":     {},
	".py":     {},
	".js":     {},
	".mjs":    {},
	".cjs":    {},
	".ts":     {},
	".tsx":    {},
	".jsx":    {},
	".vue":    {},
	".java":   {},
	".kt":     {},
	".c":      {},
	".h":      {},
	".cc":     {},
	".cpp":    {},
	".hpp":    {},
	".cs":     {},
	".rs":     {},
	".rb":     {},
	".php":    {},
	".swift":  {},
	".m":      {},
	".sh":     {},
	".bash":   {},
	".zsh":    {},
	".ps1":    {},
	".bat":    {},
	".sql":    {},
	".css":    {},
	".scss":   {},
	".lua":    {},
	".r":      {},
	".dart":   {},
	".gradle": {},
	".patch":  {},
	".diff":   {},
	// 二进制文档：PDF 走 PDFKit/PDFium，其余是 ZIP 容器，见 file_office.go
	".pdf":  {},
	".docx": {},
	".xlsx": {},
	".pptx": {},
	".epub": {},
	".odt":  {},
	".ods":  {},
	".odp":  {},
}

// isSupportedFileURL 判断 URL 是否是支持的文本类文件链接。
func isSupportedFileURL(raw string) bool {
	parsed := normalizedFileURL(raw)
	if parsed == "" {
		return false
	}
	return isSupportedFileName(parsed)
}

func normalizedFileURL(raw string) string {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return ""
	}
	return parsed.Path
}

func isSupportedLocalFile(localPath string) bool {
	localPath = strings.TrimSpace(strings.TrimPrefix(localPath, "file://"))
	return filepath.IsAbs(localPath) && isSupportedFileName(localPath)
}

func isSupportedFileName(name string) bool {
	ext := strings.ToLower(path.Ext(name))
	if _, ok := supportedFileExts[ext]; ok {
		return true
	}
	// app.log.1、app.log.2026-09-23 这类轮转文件：去掉序号或日期后缀再看一次。
	if !isRotationSuffix(ext) {
		return false
	}
	_, ok := supportedFileExts[strings.ToLower(path.Ext(strings.TrimSuffix(name, path.Ext(name))))]
	return ok
}

func isRotationSuffix(ext string) bool {
	if len(ext) < 2 {
		return false
	}
	for _, r := range ext[1:] {
		if (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func looksPDF(name string, contentType string, data []byte) bool {
	contentType = strings.ToLower(contentType)
	return strings.EqualFold(path.Ext(name), ".pdf") ||
		strings.Contains(contentType, "application/pdf") ||
		bytes.HasPrefix(bytes.TrimSpace(data), []byte("%PDF-"))
}

// looksMarkup 判断是否是需要抽正文的 HTML 文档。
func looksMarkup(name string, contentType string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".html", ".htm":
		return true
	}
	return strings.Contains(strings.ToLower(contentType), "text/html")
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
	if officeKindFromName(name) != "" || strings.EqualFold(path.Ext(name), ".pdf") {
		// 这两类是二进制容器，交给各自的解析分支，不能当纯文本读。
		return false
	}
	if _, ok := supportedFileExts[strings.ToLower(path.Ext(name))]; ok {
		return true
	}
	// 没有可靠扩展名或 content-type 时，按字节内容判断，规则见 decodeFileText。
	_, _, ok := decodeFileText(data)
	return ok
}

type pdfTextExtraction struct {
	Text       string
	PageCount  int
	Meaningful int
	// Stopped 说明攒够 maxChars 后提前停了，后面还有页没提取。
	Stopped bool
}

func (e pdfTextExtraction) NeedsOCR() bool {
	if e.PageCount <= 0 || strings.TrimSpace(e.Text) == "" {
		return true
	}
	threshold := max(e.PageCount*16, 32)
	return e.Meaningful < threshold
}

func extractPDFText(ctx context.Context, renderer pdfPageRenderer, data []byte, maxChars int) pdfTextExtraction {
	if renderer == nil {
		return pdfTextExtraction{}
	}
	session, err := renderer.Open(ctx, data)
	if err != nil {
		return pdfTextExtraction{}
	}
	defer session.Close()
	textSession, ok := session.(pdfTextPageSession)
	if !ok {
		return pdfTextExtraction{PageCount: session.PageCount()}
	}

	result := pdfTextExtraction{PageCount: session.PageCount()}
	threshold := max(result.PageCount*16, 32)
	var builder strings.Builder
	for page := 0; page < result.PageCount; page++ {
		pageText, pageErr := textSession.PageText(ctx, page)
		if pageErr != nil {
			continue
		}
		for _, r := range pageText {
			if !unicode.IsSpace(r) {
				result.Meaningful++
			}
		}
		if maxChars <= 0 || len([]rune(builder.String())) < maxChars {
			pageText = strings.TrimSpace(pageText)
			if pageText != "" {
				if builder.Len() > 0 {
					builder.WriteString("\n\n")
				}
				builder.WriteString(fmt.Sprintf("第 %d 页：\n", page+1))
				builder.WriteString(pageText)
			}
		}
		if result.Meaningful >= threshold && maxChars > 0 && len([]rune(builder.String())) >= maxChars {
			result.Stopped = page+1 < result.PageCount
			break
		}
	}
	result.Text = builder.String()
	return result
}

func sanitizeFileTextString(text string, maxChars int) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = stripControlChars(text)
	text = strings.TrimSpace(text)
	return clipHeadTail(text, maxChars)
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
