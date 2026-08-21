// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

// buildZip 构造一个内存 ZIP，作为 OOXML/ODF/EPUB 测试样本。
func buildZip(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	buffer := &bytes.Buffer{}
	writer := zip.NewWriter(buffer)
	for name, content := range entries {
		file, err := writer.Create(name)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := file.Write([]byte(content)); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buffer.Bytes()
}

func TestExtractDOCXText(t *testing.T) {
	data := buildZip(t, map[string]string{
		"word/document.xml": `<?xml version="1.0"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
 <w:body>
  <w:p><w:r><w:t>第一段</w:t></w:r><w:r><w:t>继续</w:t></w:r></w:p>
  <w:p><w:r><w:t>第二段</w:t><w:tab/><w:t>制表</w:t></w:r></w:p>
  <w:p><w:pPr><w:rPr/></w:pPr></w:p>
 </w:body>
</w:document>`,
	})
	if !looksZipArchive(data) {
		t.Fatal("docx sample should look like a zip archive")
	}
	text, err := extractOfficeText(officeKindDOCX, data, 0)
	if err != nil {
		t.Fatalf("extractOfficeText() error = %v", err)
	}
	if text != "第一段继续\n第二段\t制表" {
		t.Fatalf("text = %q", text)
	}
}

func TestExtractXLSXTextUsesSharedStringsAndSheetNames(t *testing.T) {
	data := buildZip(t, map[string]string{
		"xl/workbook.xml":      `<workbook><sheets><sheet name="收入" sheetId="1"/></sheets></workbook>`,
		"xl/sharedStrings.xml": `<sst><si><t>项目</t></si><si><t>金额</t></si><si><t>服务器</t></si></sst>`,
		"xl/worksheets/sheet1.xml": `<worksheet><sheetData>
  <row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row>
  <row r="2"><c r="A2" t="s"><v>2</v></c><c r="B2"><v>128.5</v></c></row>
 </sheetData></worksheet>`,
	})
	text, err := extractOfficeText(officeKindXLSX, data, 0)
	if err != nil {
		t.Fatalf("extractOfficeText() error = %v", err)
	}
	want := "## 收入\n项目\t金额\n服务器\t128.5"
	if text != want {
		t.Fatalf("text = %q, want %q", text, want)
	}
}

func TestExtractPPTXTextOrdersSlidesNumerically(t *testing.T) {
	slide := func(body string) string {
		return `<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><p:cSld><p:spTree>` +
			`<a:p><a:r><a:t>` + body + `</a:t></a:r></a:p>` +
			`</p:spTree></p:cSld></p:sld>`
	}
	entries := map[string]string{
		"ppt/slides/slide1.xml":  slide("封面"),
		"ppt/slides/slide2.xml":  slide("正文"),
		"ppt/slides/slide10.xml": slide("结尾"),
		"ppt/notesSlides/notesSlide1.xml": `<p:notes xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">` +
			`<a:p><a:r><a:t>讲稿</a:t></a:r></a:p></p:notes>`,
	}
	text, err := extractOfficeText(officeKindPPTX, buildZip(t, entries), 0)
	if err != nil {
		t.Fatalf("extractOfficeText() error = %v", err)
	}
	want := "## 第 1 页\n封面\n备注：讲稿\n## 第 2 页\n正文\n## 第 3 页\n结尾"
	if text != want {
		t.Fatalf("text = %q, want %q", text, want)
	}
}

func TestExtractEPUBTextFollowsSpineOrder(t *testing.T) {
	entries := map[string]string{
		"META-INF/container.xml": `<container><rootfiles><rootfile full-path="OEBPS/book.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`,
		"OEBPS/book.opf": `<package><manifest>
   <item id="c1" href="chapter1.xhtml" media-type="application/xhtml+xml"/>
   <item id="c2" href="chapter2.xhtml" media-type="application/xhtml+xml"/>
  </manifest><spine><itemref idref="c2"/><itemref idref="c1"/></spine></package>`,
		"OEBPS/chapter1.xhtml": `<html><head><style>p{color:red}</style></head><body><p>第一章</p></body></html>`,
		"OEBPS/chapter2.xhtml": `<html><body><h1>序言</h1><p>正文&amp;注释</p><script>ignored()</script></body></html>`,
	}
	text, err := extractOfficeText(officeKindEPUB, buildZip(t, entries), 0)
	if err != nil {
		t.Fatalf("extractOfficeText() error = %v", err)
	}
	want := "序言\n正文&注释\n第一章"
	if text != want {
		t.Fatalf("text = %q, want %q", text, want)
	}
}

func TestExtractODFText(t *testing.T) {
	data := buildZip(t, map[string]string{
		"content.xml": `<office:document-content xmlns:office="urn:office" xmlns:text="urn:text">
  <office:body><office:text>
    <text:h>标题</text:h>
    <text:p>段落<text:tab/>结束</text:p>
  </office:text></office:body></office:document-content>`,
	})
	text, err := extractOfficeText(officeKindODT, data, 0)
	if err != nil {
		t.Fatalf("extractOfficeText() error = %v", err)
	}
	if text != "标题\n段落\t结束" {
		t.Fatalf("text = %q", text)
	}
}

func TestExtractOfficeTextStopsAtCharLimit(t *testing.T) {
	body := strings.Repeat("<w:p><w:r><w:t>一二三四五</w:t></w:r></w:p>", 200)
	data := buildZip(t, map[string]string{
		"word/document.xml": `<w:document xmlns:w="urn:w"><w:body>` + body + `</w:body></w:document>`,
	})
	text, err := extractOfficeText(officeKindDOCX, data, 20)
	if err != nil {
		t.Fatalf("extractOfficeText() error = %v", err)
	}
	if runes := []rune(text); len(runes) > 40 {
		t.Fatalf("text should stop near the limit, got %d runes", len(runes))
	}
}

func TestExtractOfficeTextRejectsNonArchive(t *testing.T) {
	if _, err := extractOfficeText(officeKindDOCX, []byte("not a zip"), 0); err == nil {
		t.Fatal("expected an error for a non-archive payload")
	}
}

func TestOfficeKindFromName(t *testing.T) {
	cases := map[string]officeKind{
		"报告.docx":     officeKindDOCX,
		"表格.XLSX":     officeKindXLSX,
		"slides.pptx": officeKindPPTX,
		"book.epub":   officeKindEPUB,
		"note.odt":    officeKindODT,
		"readme.md":   "",
		"scan.pdf":    "",
	}
	for name, want := range cases {
		if got := officeKindFromName(name); got != want {
			t.Fatalf("officeKindFromName(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestStripMarkupTextDropsTags(t *testing.T) {
	html := []byte(`<html><head><title>标题</title><script>bad()</script></head><body><div>正文<br/>换行</div></body></html>`)
	text := stripMarkupText(html, 0)
	if text != "标题\n正文\n换行" {
		t.Fatalf("text = %q", text)
	}
}
