// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type pdfPageResult struct {
	StartPage        int    `json:"start_page"`
	EndPage          int    `json:"end_page"`
	Text             string `json:"text"`
	NextPage         int    `json:"next_page"`
	NextOffset       int    `json:"next_offset"`
	DocumentComplete bool   `json:"document_complete"`
}

func decodePDFPageResult(t *testing.T, raw string) pdfPageResult {
	t.Helper()
	var result pdfPageResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	return result
}

// 生产上 151 页讲义一次只给一页，模型续读一页就放弃；现在一次连读到装满预算，
// 按 next_page/next_offset 续读能不重不漏地读到末页。
func TestReadPDFPageReadsManyPagesPerCall(t *testing.T) {
	texts := make([]string, 151)
	for i := range texts {
		texts[i] = fmt.Sprintf("P%03d", i+1) + strings.Repeat("字", 1500)
	}
	texts[40] = strings.Repeat("长", 30000) + "长页尾"
	parser := NewFileParserPlugin(nil)
	parser.pdfRenderer = &fakePDFTextRenderer{texts: texts}
	path := filepath.Join(t.TempDir(), "guide.pdf")
	if err := os.WriteFile(path, minimalTextPDF(), 0600); err != nil {
		t.Fatal(err)
	}
	tool := &dianaHistoryImagesTool{}
	ref := fileRef{Name: "guide.pdf", LocalPath: path}
	raw, err := tool.readPDFPage(context.Background(), parser, nil, ref, 1<<20, 18, 0, 20000)
	if err != nil {
		t.Fatal(err)
	}
	first := decodePDFPageResult(t, raw)
	if first.StartPage != 18 || first.EndPage < 30 || first.DocumentComplete {
		t.Fatalf("one call should cover a dozen pages: %+v", first)
	}
	var got strings.Builder
	page, offset, calls := 1, 0, 0
	for {
		raw, err := tool.readPDFPage(context.Background(), parser, nil, ref, 1<<20, page, offset, 20000)
		if err != nil {
			t.Fatal(err)
		}
		result := decodePDFPageResult(t, raw)
		got.WriteString(pdfPageHeaderLine.ReplaceAllString(result.Text, ""))
		calls++
		if result.DocumentComplete {
			break
		}
		if result.NextPage < page || (result.NextPage == page && result.NextOffset <= offset) {
			t.Fatalf("no progress: %+v", result)
		}
		page, offset = result.NextPage, result.NextOffset
	}
	want := strings.Join(texts, "")
	if strings.ReplaceAll(got.String(), "\n", "") != want {
		t.Fatal("content lost or duplicated across calls")
	}
	if calls > 15 {
		t.Fatalf("%d calls for 151 pages", calls)
	}
	if _, err := tool.readPDFPage(context.Background(), parser, nil, ref, 1<<20, 1, 2000, 20000); err == nil {
		t.Fatal("offset beyond page accepted")
	}
}

var pdfPageHeaderLine = regexp.MustCompile(`(\n\n)?第 \d+ 页(（接第 \d+ 字）)?：\n`)

func TestReadPDFPageTextAndBounds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.pdf")
	if err := os.WriteFile(path, minimalTextPDF(), 0600); err != nil {
		t.Fatal(err)
	}
	parser := NewFileParserPlugin(nil)
	tool := &dianaHistoryImagesTool{}
	ref := fileRef{Name: "report.pdf", LocalPath: path}
	raw, err := tool.readPDFPage(context.Background(), parser, nil, ref, 1<<20, 1, 0, 20000)
	if err != nil || !strings.Contains(raw, "Hello PDFium text") || !strings.Contains(raw, `"document_complete":true`) {
		t.Fatalf("%s %v", raw, err)
	}
	if _, err := tool.readPDFPage(context.Background(), parser, nil, ref, 1<<20, 2, 0, 20000); err == nil {
		t.Fatal("out of range page accepted")
	}
	tool = &dianaHistoryImagesTool{}
	parser.pdfRenderer = &fakePDFTextRenderer{texts: []string{"目录", strings.Repeat("测", 9000) + "测评结果"}}
	raw, err = tool.readPDFPage(context.Background(), parser, nil, ref, 1<<20, 2, 8000, 20000)
	if err != nil || !strings.Contains(raw, "测评结果") || strings.Contains(raw, "目录") {
		t.Fatalf("%s %v", raw, err)
	}
}

func TestPDFProductionReplay(t *testing.T) {
	path := os.Getenv("DIANA_PDF_REPLAY_PATH")
	if path == "" {
		t.Skip("DIANA_PDF_REPLAY_PATH required")
	}
	parser := NewFileParserPlugin(nil)
	tool := &dianaHistoryImagesTool{}
	ref := fileRef{Name: "report.pdf", LocalPath: path}
	var material string
	page, offset, total := 1, 0, 0
	for {
		raw, err := tool.readPDFPage(context.Background(), parser, nil, ref, 20<<20, page, offset, 4000)
		if err != nil {
			t.Fatal(err)
		}
		var chunk struct {
			Text       string `json:"text"`
			NextPage   int    `json:"next_page"`
			NextOffset int    `json:"next_offset"`
			Done       bool   `json:"document_complete"`
		}
		if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
			t.Fatal(err)
		}
		total += len([]rune(chunk.Text))
		if page > 35 && len([]rune(material)) < 4000 {
			material += chunk.Text
		}
		if chunk.Done {
			break
		}
		page, offset = chunk.NextPage, chunk.NextOffset
	}
	if total <= 8000 || material == "" {
		t.Fatal("document tail not reached")
	}
	t.Logf("read %d pages, %d characters", page, total)
	if os.Getenv("DIANA_LIVE_LLM") != "1" {
		return
	}
	client := liveLLMClient(t)
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return client, nil })
	answer, err := runtime.runSubtask(context.Background(), BotConfig{}, "chat", "概括这段 PDF 测评正文的具体发现，不要把它当成目录。", string([]rune(material)[:min(4000, len([]rune(material)))]))
	if err != nil {
		message := err.Error()
		configData, _ := os.ReadFile(os.Getenv("DIANA_TEST_LLM_CONFIG"))
		var config map[string]any
		_ = json.Unmarshal(configData, &config)
		for _, key := range []string{"api_key", "base_url"} {
			if value, ok := config[key].(string); ok && value != "" {
				message = strings.ReplaceAll(message, value, "[redacted]")
			}
		}
		t.Fatal(message)
	}
	if strings.TrimSpace(answer) == "" {
		t.Fatal("empty answer")
	}
	t.Logf("production PDF tail summarized successfully (%d characters)", len([]rune(answer)))
}

func TestHistoryMediaPDFPageSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.pdf")
	if err := os.WriteFile(path, minimalTextPDF(), 0600); err != nil {
		t.Fatal(err)
	}
	parser := NewFileParserPlugin(nil)
	parser.pdfRenderer = &fakePDFTextRenderer{texts: []string{"目录", strings.Repeat("甲", 9000) + "尾部结论"}}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(parser), nil, nil, nil, nil)
	runtime.remember(MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "u", MessageID: "file", Segments: []MessageSegment{{Type: "file", Data: map[string]string{"name": "report.pdf", "file": path}}}})
	tool := newDianaHistoryImagesTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "u"})
	raw, err := tool.Run(context.Background(), map[string]any{"message_id": "file", "pdf_page": 2, "page_offset": 8000})
	if err != nil || !strings.Contains(raw, "尾部结论") || strings.Contains(raw, "目录") {
		t.Fatalf("%s %v", raw, err)
	}
	for _, input := range []map[string]any{{"pdf_page": 0}, {"pdf_page": 1.5}, {"page_offset": 4}, {"pdf_page": 1, "page_offset": -1}} {
		if _, err := tool.Run(context.Background(), input); err == nil {
			t.Fatalf("invalid input accepted: %v", input)
		}
	}
}

func TestReadPDFScannedPageUsesVisionAndCachesContinuation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scan.pdf")
	if err := os.WriteFile(path, minimalImageOnlyPDF(), 0600); err != nil {
		t.Fatal(err)
	}
	provider := &qualityTestProvider{reply: strings.Repeat("扫描正文", 1100) + "扫描页尾"}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	parser := NewFileParserPlugin(nil)
	parser.pdfRenderer = &fakePDFRenderer{pages: 1}
	tool := newDianaHistoryImagesTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "u"})
	ref := fileRef{Name: "scan.pdf", LocalPath: path}
	if _, err := tool.readPDFPage(context.Background(), parser, nil, ref, 1<<20, 1, 0, 20000); err != nil {
		t.Fatal(err)
	}
	raw, err := tool.readPDFPage(context.Background(), parser, nil, ref, 1<<20, 1, 4000, 20000)
	if err != nil || !strings.Contains(raw, "扫描页尾") {
		t.Fatalf("%s %v", raw, err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("OCR repeated: %d", len(provider.requests))
	}
}

// 已经读到正文时停在下一张扫描页前，扫描页留给下一次调用单独做视觉 OCR。
func TestReadPDFPageStopsBeforeScannedPage(t *testing.T) {
	parser := NewFileParserPlugin(nil)
	parser.pdfRenderer = &fakePDFTextRenderer{texts: []string{"第一页文字", "  ", "第三页文字"}}
	path := filepath.Join(t.TempDir(), "mixed.pdf")
	if err := os.WriteFile(path, minimalTextPDF(), 0600); err != nil {
		t.Fatal(err)
	}
	tool := &dianaHistoryImagesTool{}
	raw, err := tool.readPDFPage(context.Background(), parser, nil, fileRef{Name: "mixed.pdf", LocalPath: path}, 1<<20, 1, 0, 20000)
	if err != nil {
		t.Fatal(err)
	}
	result := decodePDFPageResult(t, raw)
	if result.EndPage != 1 || result.NextPage != 2 || result.NextOffset != 0 || result.DocumentComplete || strings.Contains(result.Text, "第三页") {
		t.Fatalf("%+v", result)
	}
}
