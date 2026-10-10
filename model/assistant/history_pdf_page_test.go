// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPDFPageChunkReadsBeyond8000(t *testing.T) {
	original := strings.Repeat("测", 24956) + "最后测评结果"
	var got strings.Builder
	offset := 0
	for {
		raw, err := pdfPageChunk(original, 2, 3, offset)
		if err != nil {
			t.Fatal(err)
		}
		var chunk struct {
			Text             string `json:"text"`
			NextOffset       int    `json:"next_offset"`
			NextPage         int    `json:"next_page"`
			Complete         bool   `json:"page_complete"`
			DocumentComplete bool   `json:"document_complete"`
		}
		if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
			t.Fatal(err)
		}
		got.WriteString(chunk.Text)
		if chunk.DocumentComplete {
			t.Fatal("later page still exists")
		}
		if chunk.Complete {
			if chunk.NextPage != 3 || chunk.NextOffset != 0 {
				t.Fatal(raw)
			}
			break
		}
		if chunk.NextOffset <= offset || chunk.NextPage != 2 {
			t.Fatal(raw)
		}
		offset = chunk.NextOffset
	}
	if got.String() != original {
		t.Fatal("page content lost or duplicated")
	}
	raw, err := pdfPageChunk("末页", 3, 3, 0)
	if err != nil || !strings.Contains(raw, `"document_complete":true`) {
		t.Fatalf("%s %v", raw, err)
	}
	for _, offset := range []int{-1, 100} {
		if _, err := pdfPageChunk("内容", 1, 1, offset); err == nil {
			t.Fatal("invalid offset accepted")
		}
	}
}

func TestReadPDFPageTextAndBounds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.pdf")
	if err := os.WriteFile(path, minimalTextPDF(), 0600); err != nil {
		t.Fatal(err)
	}
	parser := NewFileParserPlugin(nil)
	tool := &dianaHistoryImagesTool{}
	ref := fileRef{Name: "report.pdf", LocalPath: path}
	raw, err := tool.readPDFPage(context.Background(), parser, nil, ref, 1<<20, 1, 0)
	if err != nil || !strings.Contains(raw, "Hello PDFium text") || !strings.Contains(raw, `"document_complete":true`) {
		t.Fatalf("%s %v", raw, err)
	}
	if _, err := tool.readPDFPage(context.Background(), parser, nil, ref, 1<<20, 2, 0); err == nil {
		t.Fatal("out of range page accepted")
	}
	tool = &dianaHistoryImagesTool{}
	parser.pdfRenderer = &fakePDFTextRenderer{texts: []string{"目录", strings.Repeat("测", 9000) + "测评结果"}}
	raw, err = tool.readPDFPage(context.Background(), parser, nil, ref, 1<<20, 2, 8000)
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
		raw, err := tool.readPDFPage(context.Background(), parser, nil, ref, 20<<20, page, offset)
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
	if _, err := tool.readPDFPage(context.Background(), parser, nil, ref, 1<<20, 1, 0); err != nil {
		t.Fatal(err)
	}
	raw, err := tool.readPDFPage(context.Background(), parser, nil, ref, 1<<20, 1, 4000)
	if err != nil || !strings.Contains(raw, "扫描页尾") {
		t.Fatalf("%s %v", raw, err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("OCR repeated: %d", len(provider.requests))
	}
}
