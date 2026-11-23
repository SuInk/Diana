// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/SuInk/diana/model/llm"
)

// readPDFPage keeps the existing session-scoped file resolution and download limits.
// 从 page/offset 起按页往后读，装满 budget 字为止：以前一次只给一页（最多 4000 字），
// 151 页的讲义要调一百多次，模型读一页就放弃说「一次最多拿十几页」。扫描页要逐页
// 走视觉 OCR，已经有正文时停在下一张扫描页前，免得一次工具调用串起十几次视觉请求。
func (t *dianaHistoryImagesTool) readPDFPage(ctx context.Context, parser *FileParserPlugin, channel Channel, ref fileRef, maxBytes int64, page, offset, budget int) (string, error) {
	if page < 1 || offset < 0 {
		return "", fmt.Errorf("pdf_page 必须大于 0，page_offset 不能为负数")
	}
	ref = parser.resolveOneBotFile(ctx, channel, ref)
	data, _, contentType, err := parser.readRef(ctx, ref, maxBytes)
	if err != nil {
		return "", err
	}
	if !looksPDF(ref.Name, contentType, data) {
		return "", fmt.Errorf("该文件不是 PDF")
	}
	if parser.pdfRenderer == nil {
		return "", fmt.Errorf("PDF 解析器不可用")
	}
	session, err := parser.pdfRenderer.Open(ctx, data)
	if err != nil {
		return "", err
	}
	defer session.Close()
	pages := session.PageCount()
	if page > pages {
		return "", fmt.Errorf("PDF 共 %d 页，不能读取第 %d 页", pages, page)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	reader := pdfPageReader{budget: max(budget, 1), pages: pages}
	for current := page; current <= pages && !reader.full(); current++ {
		content, ocr, err := t.pdfPageText(ctx, session, digest, ref.Name, current, reader.empty())
		if err != nil {
			if reader.empty() {
				return "", err
			}
			reader.stopAt(current, 0)
			break
		}
		if ocr {
			reader.stopAt(current, 0)
			break
		}
		if current != page {
			offset = 0
		}
		if err := reader.add(current, content, offset); err != nil {
			return "", err
		}
	}
	return reader.result()
}

// pdfPageText 取一页正文并缓存。allowOCR 为 false 时遇到没有文字层的页不识别，
// 返回 ocr=true 让调用方停在这一页。
func (t *dianaHistoryImagesTool) pdfPageText(ctx context.Context, session pdfRenderSession, digest, name string, page int, allowOCR bool) (string, bool, error) {
	key := fmt.Sprintf("%s:%d", digest, page)
	t.mu.Lock()
	cached, found := t.pdfPageTexts[key]
	t.mu.Unlock()
	if found {
		return cached, false, nil
	}
	var content string
	if textSession, ok := session.(pdfTextPageSession); ok {
		text, err := textSession.PageText(ctx, page-1)
		if err != nil {
			return "", false, err
		}
		content = text
	}
	if strings.TrimSpace(content) == "" {
		if !allowOCR {
			return "", true, nil
		}
		image, err := session.RenderJPEG(ctx, page-1)
		if err != nil {
			return "", false, err
		}
		services := PluginTaskServices{Generate: func(callCtx context.Context, req llm.GenerateRequest) (string, error) {
			callCtx = withLLMUsageContext(callCtx, t.event)
			return t.runtime.runLLMProviderForGroup(callCtx, llm.GroupVision, func(client LLMProvider) (string, error) {
				resp, err := client.Generate(callCtx, req)
				if err != nil {
					return "", err
				}
				return resp.Text, nil
			})
		}}
		content, err = runPageVisionOCR(ctx, services, name, page, session.PageCount(), image)
		if err != nil {
			return "", false, err
		}
	}
	t.mu.Lock()
	if t.pdfPageTexts == nil {
		t.pdfPageTexts = make(map[string]string)
	}
	t.pdfPageTexts[key] = content
	t.mu.Unlock()
	return content, false, nil
}

// pdfPageReader 把连续几页拼进一次结果，按字数装满 budget；最后一页装不下时给页内偏移。
type pdfPageReader struct {
	budget, pages      int
	used               int
	startPage, endPage int
	text               strings.Builder
	nextPage           int
	nextOffset         int
}

func (r *pdfPageReader) empty() bool { return r.startPage == 0 }

func (r *pdfPageReader) full() bool { return r.nextPage != 0 || r.used >= r.budget }

func (r *pdfPageReader) stopAt(page, offset int) { r.nextPage, r.nextOffset = page, offset }

func (r *pdfPageReader) add(page int, content string, offset int) error {
	runes := []rune(content)
	if offset > len(runes) {
		return fmt.Errorf("page_offset 超出第 %d 页 %d 字范围", page, len(runes))
	}
	header := fmt.Sprintf("第 %d 页：\n", page)
	if offset > 0 {
		header = fmt.Sprintf("第 %d 页（接第 %d 字）：\n", page, offset)
	}
	if !r.empty() {
		header = "\n\n" + header
	}
	room := max(r.budget-r.used, 1)
	end := min(offset+room, len(runes))
	if r.empty() {
		r.startPage = page
	}
	r.endPage = page
	r.text.WriteString(header)
	r.text.WriteString(string(runes[offset:end]))
	r.used += end - offset
	if end < len(runes) {
		r.stopAt(page, end)
	}
	return nil
}

func (r *pdfPageReader) result() (string, error) {
	if r.nextPage == 0 && r.endPage < r.pages {
		r.stopAt(r.endPage+1, 0)
	}
	result := map[string]any{"start_page": r.startPage, "end_page": r.endPage, "total_pages": r.pages, "text": r.text.String(), "document_complete": r.nextPage == 0}
	if r.nextPage != 0 {
		result["next_page"] = r.nextPage
		result["next_offset"] = r.nextOffset
	}
	raw, err := json.Marshal(result)
	return string(raw), err
}
