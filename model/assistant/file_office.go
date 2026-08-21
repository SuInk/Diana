// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
)

// OOXML、ODF 和 EPUB 都是 ZIP 容器，用标准库解压后按各自的 XML 结构取文本，
// 不引入额外依赖。解压有条目数和总字节上限，避免超高压缩比的压缩炸弹。
const (
	maxArchiveEntries    = 4096
	maxArchiveTotalBytes = 64 << 20
	maxArchiveEntryBytes = 32 << 20
)

// officeKind 标识一种基于 ZIP 的文档格式。
type officeKind string

const (
	officeKindDOCX officeKind = "docx"
	officeKindXLSX officeKind = "xlsx"
	officeKindPPTX officeKind = "pptx"
	officeKindEPUB officeKind = "epub"
	officeKindODT  officeKind = "odt"
	officeKindODS  officeKind = "ods"
	officeKindODP  officeKind = "odp"
)

var officeKindByExt = map[string]officeKind{
	".docx": officeKindDOCX,
	".xlsx": officeKindXLSX,
	".pptx": officeKindPPTX,
	".epub": officeKindEPUB,
	".odt":  officeKindODT,
	".ods":  officeKindODS,
	".odp":  officeKindODP,
}

var officeKindLabels = map[officeKind]string{
	officeKindDOCX: "Word 文档",
	officeKindXLSX: "Excel 表格",
	officeKindPPTX: "PowerPoint 演示文稿",
	officeKindEPUB: "EPUB 电子书",
	officeKindODT:  "ODF 文档",
	officeKindODS:  "ODF 表格",
	officeKindODP:  "ODF 演示文稿",
}

// officeKindFromName 根据扩展名判断文档格式，未知格式返回空。
func officeKindFromName(name string) officeKind {
	return officeKindByExt[strings.ToLower(path.Ext(name))]
}

// officeKindLabel 返回展示给 LLM 上下文的格式名称。
func officeKindLabel(kind officeKind) string {
	if label, ok := officeKindLabels[kind]; ok {
		return label
	}
	return string(kind)
}

// looksZipArchive 判断数据是否是 ZIP 容器。
func looksZipArchive(data []byte) bool {
	return bytes.HasPrefix(data, []byte("PK\x03\x04"))
}

// extractOfficeText 按格式提取纯文本；maxChars 用于提前停止，避免整本书都解压。
func extractOfficeText(kind officeKind, data []byte, maxChars int) (string, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	if len(reader.File) > maxArchiveEntries {
		return "", fmt.Errorf("archive has too many entries: %d", len(reader.File))
	}
	archive := &zipArchive{reader: reader, budget: maxArchiveTotalBytes}
	switch kind {
	case officeKindDOCX:
		return extractDOCXText(archive, maxChars)
	case officeKindXLSX:
		return extractXLSXText(archive, maxChars)
	case officeKindPPTX:
		return extractPPTXText(archive, maxChars)
	case officeKindEPUB:
		return extractEPUBText(archive, maxChars)
	case officeKindODT, officeKindODS, officeKindODP:
		return extractODFText(archive, maxChars)
	}
	return "", fmt.Errorf("unsupported document kind %q", kind)
}

// zipArchive 包一层解压预算，读取过的字节统一从预算里扣。
type zipArchive struct {
	reader *zip.Reader
	budget int64
}

func (a *zipArchive) open(name string) ([]byte, error) {
	for _, file := range a.reader.File {
		if file.Name == name {
			return a.read(file)
		}
	}
	return nil, fmt.Errorf("entry %q not found", name)
}

func (a *zipArchive) read(file *zip.File) ([]byte, error) {
	if file.FileInfo().IsDir() {
		return nil, fmt.Errorf("entry %q is a directory", file.Name)
	}
	if a.budget <= 0 {
		return nil, fmt.Errorf("archive decompression budget exhausted")
	}
	rc, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	limit := min(a.budget, int64(maxArchiveEntryBytes))
	data, err := io.ReadAll(io.LimitReader(rc, limit))
	a.budget -= int64(len(data))
	if err != nil {
		return nil, err
	}
	return data, nil
}

// names 返回符合前缀和后缀的条目名，按名称排序保证输出稳定。
func (a *zipArchive) names(prefix, suffix string) []string {
	out := make([]string, 0, 8)
	for _, file := range a.reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		if strings.HasPrefix(file.Name, prefix) && strings.HasSuffix(strings.ToLower(file.Name), suffix) {
			out = append(out, file.Name)
		}
	}
	sort.Strings(out)
	return out
}

// textBuilder 收集文本并在达到字符上限后停止，避免继续解压后面的内容。
type textBuilder struct {
	builder  strings.Builder
	runes    int
	maxChars int
}

func newTextBuilder(maxChars int) *textBuilder {
	return &textBuilder{maxChars: maxChars}
}

func (b *textBuilder) full() bool {
	return b.maxChars > 0 && b.runes >= b.maxChars
}

func (b *textBuilder) write(text string) {
	if text == "" || b.full() {
		return
	}
	b.builder.WriteString(text)
	b.runes += len([]rune(text))
}

// line 写入一行，自动补换行并忽略空行。
func (b *textBuilder) line(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if b.builder.Len() > 0 {
		b.write("\n")
	}
	b.write(text)
}

func (b *textBuilder) String() string {
	return b.builder.String()
}

// xmlTextOptions 描述从 XML 里抽取文本时关心的元素局部名。
type xmlTextOptions struct {
	// text 是承载文本的元素，例如 Word 的 w:t、PPT 的 a:t。
	text map[string]bool
	// block 结束时输出换行，例如段落 w:p。
	block map[string]bool
	// tab 和 br 是空元素形式的制表符与换行。
	tab map[string]bool
	br  map[string]bool
	// skip 的子树整体跳过，例如 EPUB 里的 script/style。
	skip map[string]bool
}

// extractXMLText 遍历 XML，按 options 把文本拼成带换行的纯文本。
func extractXMLText(data []byte, options xmlTextOptions, out *textBuilder) error {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = false
	decoder.AutoClose = xml.HTMLAutoClose
	decoder.Entity = xml.HTMLEntity

	depth := 0
	skipDepth := -1
	textDepth := 0
	var line strings.Builder
	flush := func() {
		out.line(line.String())
		line.Reset()
	}
	for {
		if out.full() {
			break
		}
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			// XML 结构损坏时保留已经取到的文本，不整体失败。
			break
		}
		switch element := token.(type) {
		case xml.StartElement:
			depth++
			name := element.Name.Local
			if skipDepth >= 0 {
				continue
			}
			switch {
			case options.skip[name]:
				skipDepth = depth
			case options.text[name]:
				textDepth++
			case options.tab[name]:
				line.WriteString("\t")
			case options.br[name]:
				flush()
			}
		case xml.EndElement:
			name := element.Name.Local
			if skipDepth >= 0 {
				if depth == skipDepth {
					skipDepth = -1
				}
				depth--
				continue
			}
			if options.text[name] && textDepth > 0 {
				textDepth--
			}
			if options.block[name] {
				flush()
			}
			depth--
		case xml.CharData:
			if skipDepth >= 0 || textDepth == 0 {
				continue
			}
			line.Write(element)
		}
	}
	flush()
	return nil
}

func extractDOCXText(archive *zipArchive, maxChars int) (string, error) {
	options := xmlTextOptions{
		text:  map[string]bool{"t": true, "delText": true},
		block: map[string]bool{"p": true},
		tab:   map[string]bool{"tab": true},
		br:    map[string]bool{"br": true, "cr": true},
	}
	out := newTextBuilder(maxChars)
	// 正文之外的脚注和尾注同样可能带有正文信息，正文取完后按顺序补上。
	parts := []string{"word/document.xml", "word/footnotes.xml", "word/endnotes.xml"}
	found := false
	for _, name := range parts {
		data, err := archive.open(name)
		if err != nil {
			continue
		}
		found = true
		if err := extractXMLText(data, options, out); err != nil {
			return "", err
		}
		if out.full() {
			break
		}
	}
	if !found {
		return "", fmt.Errorf("word/document.xml not found")
	}
	return out.String(), nil
}

func extractXLSXText(archive *zipArchive, maxChars int) (string, error) {
	shared := readXLSXSharedStrings(archive)
	sheetNames := readXLSXSheetNames(archive)
	sheets := archive.names("xl/worksheets/", ".xml")
	if len(sheets) == 0 {
		return "", fmt.Errorf("xl/worksheets not found")
	}
	sortNumbered(sheets)
	out := newTextBuilder(maxChars)
	for index, name := range sheets {
		if out.full() {
			break
		}
		data, err := archive.open(name)
		if err != nil {
			continue
		}
		title := fmt.Sprintf("工作表 %d", index+1)
		if index < len(sheetNames) && strings.TrimSpace(sheetNames[index]) != "" {
			title = sheetNames[index]
		}
		out.line("## " + title)
		if err := writeXLSXSheetRows(data, shared, out); err != nil {
			return "", err
		}
	}
	return out.String(), nil
}

// writeXLSXSheetRows 把工作表按行拼成制表符分隔的文本，空单元格保留占位。
func writeXLSXSheetRows(data []byte, shared []string, out *textBuilder) error {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = false
	cells := make([]string, 0, 16)
	cellType := ""
	inValue := false
	inInline := false
	var value strings.Builder
	for {
		if out.full() {
			return nil
		}
		token, err := decoder.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return nil
		}
		switch element := token.(type) {
		case xml.StartElement:
			switch element.Name.Local {
			case "row":
				cells = cells[:0]
			case "c":
				cellType = ""
				value.Reset()
				for _, attr := range element.Attr {
					if attr.Name.Local == "t" {
						cellType = attr.Value
					}
				}
			case "v":
				inValue = true
			case "is":
				inInline = true
			}
		case xml.CharData:
			if inValue || inInline {
				value.Write(element)
			}
		case xml.EndElement:
			switch element.Name.Local {
			case "v":
				inValue = false
			case "is":
				inInline = false
			case "c":
				cells = append(cells, resolveXLSXCell(cellType, value.String(), shared))
			case "row":
				out.line(strings.TrimRight(strings.Join(cells, "\t"), "\t"))
			}
		}
	}
}

// resolveXLSXCell 把单元格原始值翻译成展示文本，t="s" 指向共享字符串表。
func resolveXLSXCell(cellType string, raw string, shared []string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	switch cellType {
	case "s":
		index, err := strconv.Atoi(raw)
		if err != nil || index < 0 || index >= len(shared) {
			return ""
		}
		return shared[index]
	case "b":
		if raw == "1" {
			return "TRUE"
		}
		return "FALSE"
	}
	return raw
}

func readXLSXSharedStrings(archive *zipArchive) []string {
	data, err := archive.open("xl/sharedStrings.xml")
	if err != nil {
		return nil
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = false
	shared := make([]string, 0, 64)
	var current strings.Builder
	inItem := false
	inText := false
	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		switch element := token.(type) {
		case xml.StartElement:
			switch element.Name.Local {
			case "si":
				inItem = true
				current.Reset()
			case "t":
				inText = true
			}
		case xml.CharData:
			if inItem && inText {
				current.Write(element)
			}
		case xml.EndElement:
			switch element.Name.Local {
			case "t":
				inText = false
			case "si":
				shared = append(shared, current.String())
				inItem = false
			}
		}
	}
	return shared
}

// readXLSXSheetNames 按 workbook.xml 里的声明顺序返回工作表名称。
func readXLSXSheetNames(archive *zipArchive) []string {
	data, err := archive.open("xl/workbook.xml")
	if err != nil {
		return nil
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = false
	names := make([]string, 0, 8)
	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "sheet" {
			continue
		}
		for _, attr := range start.Attr {
			if attr.Name.Local == "name" {
				names = append(names, attr.Value)
				break
			}
		}
	}
	return names
}

func extractPPTXText(archive *zipArchive, maxChars int) (string, error) {
	slides := archive.names("ppt/slides/", ".xml")
	if len(slides) == 0 {
		return "", fmt.Errorf("ppt/slides not found")
	}
	sortNumbered(slides)
	options := xmlTextOptions{
		text:  map[string]bool{"t": true},
		block: map[string]bool{"p": true},
		br:    map[string]bool{"br": true},
	}
	out := newTextBuilder(maxChars)
	for index, name := range slides {
		if out.full() {
			break
		}
		data, err := archive.open(name)
		if err != nil {
			continue
		}
		out.line(fmt.Sprintf("## 第 %d 页", index+1))
		if err := extractXMLText(data, options, out); err != nil {
			return "", err
		}
		// 备注页常常写着讲稿，一并带上。
		notes := strings.Replace(name, "ppt/slides/slide", "ppt/notesSlides/notesSlide", 1)
		if notesData, notesErr := archive.open(notes); notesErr == nil {
			notesOut := newTextBuilder(maxChars)
			if err := extractXMLText(notesData, options, notesOut); err == nil {
				if text := strings.TrimSpace(notesOut.String()); text != "" {
					out.line("备注：" + text)
				}
			}
		}
	}
	return out.String(), nil
}

func extractODFText(archive *zipArchive, maxChars int) (string, error) {
	data, err := archive.open("content.xml")
	if err != nil {
		return "", err
	}
	options := xmlTextOptions{
		text:  map[string]bool{"p": true, "h": true, "span": true, "a": true, "list-item": true},
		block: map[string]bool{"p": true, "h": true, "table-row": true},
		tab:   map[string]bool{"tab": true},
		br:    map[string]bool{"line-break": true},
	}
	out := newTextBuilder(maxChars)
	if err := extractXMLText(data, options, out); err != nil {
		return "", err
	}
	return out.String(), nil
}

func extractEPUBText(archive *zipArchive, maxChars int) (string, error) {
	documents := epubReadingOrder(archive)
	if len(documents) == 0 {
		return "", fmt.Errorf("epub content documents not found")
	}
	out := newTextBuilder(maxChars)
	for _, name := range documents {
		if out.full() {
			break
		}
		data, err := archive.open(name)
		if err != nil {
			continue
		}
		out.line(stripMarkupText(data, maxChars))
	}
	return out.String(), nil
}

// epubReadingOrder 依次解析 container.xml 和 OPF，按 spine 顺序返回正文文件；
// 结构异常时退回按名称排序的 xhtml 列表。
func epubReadingOrder(archive *zipArchive) []string {
	fallback := func() []string {
		names := make([]string, 0, 16)
		for _, suffix := range []string{".xhtml", ".html", ".htm"} {
			names = append(names, archive.names("", suffix)...)
		}
		sort.Strings(names)
		return names
	}
	container, err := archive.open("META-INF/container.xml")
	if err != nil {
		return fallback()
	}
	opfPath := ""
	decoder := xml.NewDecoder(bytes.NewReader(container))
	decoder.Strict = false
	for {
		token, tokenErr := decoder.Token()
		if tokenErr != nil {
			break
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "rootfile" {
			continue
		}
		for _, attr := range start.Attr {
			if attr.Name.Local == "full-path" {
				opfPath = attr.Value
			}
		}
		if opfPath != "" {
			break
		}
	}
	if opfPath == "" {
		return fallback()
	}
	opf, err := archive.open(opfPath)
	if err != nil {
		return fallback()
	}
	base := path.Dir(opfPath)
	hrefByID := map[string]string{}
	spine := make([]string, 0, 16)
	decoder = xml.NewDecoder(bytes.NewReader(opf))
	decoder.Strict = false
	for {
		token, tokenErr := decoder.Token()
		if tokenErr != nil {
			break
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "item":
			id, href, mediaType := "", "", ""
			for _, attr := range start.Attr {
				switch attr.Name.Local {
				case "id":
					id = attr.Value
				case "href":
					href = attr.Value
				case "media-type":
					mediaType = attr.Value
				}
			}
			if id != "" && href != "" && strings.Contains(mediaType, "html") {
				hrefByID[id] = href
			}
		case "itemref":
			for _, attr := range start.Attr {
				if attr.Name.Local == "idref" {
					spine = append(spine, attr.Value)
				}
			}
		}
	}
	documents := make([]string, 0, len(spine))
	for _, id := range spine {
		href, ok := hrefByID[id]
		if !ok {
			continue
		}
		documents = append(documents, path.Clean(path.Join(base, href)))
	}
	if len(documents) == 0 {
		return fallback()
	}
	return documents
}

// stripMarkupText 去掉 HTML/XHTML 标签，只保留正文文本。
func stripMarkupText(data []byte, maxChars int) string {
	options := xmlTextOptions{
		text: map[string]bool{
			"p": true, "div": true, "span": true, "a": true, "li": true, "td": true, "th": true,
			"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
			"em": true, "strong": true, "b": true, "i": true, "code": true, "pre": true,
			"blockquote": true, "title": true, "figcaption": true, "caption": true, "dt": true, "dd": true,
		},
		block: map[string]bool{
			"p": true, "div": true, "li": true, "tr": true, "br": true, "pre": true,
			"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
			"blockquote": true, "title": true, "figcaption": true, "caption": true, "dt": true, "dd": true,
		},
		br: map[string]bool{"br": true},
		// head 不整体跳过，<title> 往往是这份文档唯一的标题。
		skip: map[string]bool{"script": true, "style": true, "svg": true},
	}
	out := newTextBuilder(maxChars)
	if err := extractXMLText(data, options, out); err != nil {
		return ""
	}
	return html.UnescapeString(out.String())
}

// sortNumbered 按名字里的数字排序，避免 slide10 排在 slide2 前面。
func sortNumbered(names []string) {
	sort.SliceStable(names, func(i, j int) bool {
		left, leftOK := trailingNumber(names[i])
		right, rightOK := trailingNumber(names[j])
		if leftOK && rightOK && left != right {
			return left < right
		}
		return names[i] < names[j]
	})
}

func trailingNumber(name string) (int, bool) {
	base := strings.TrimSuffix(path.Base(name), path.Ext(name))
	end := len(base)
	for end > 0 && base[end-1] >= '0' && base[end-1] <= '9' {
		end--
	}
	if end == len(base) {
		return 0, false
	}
	value, err := strconv.Atoi(base[end:])
	if err != nil {
		return 0, false
	}
	return value, true
}
