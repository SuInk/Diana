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
// Each response is small enough to pass to subtask without discarding the page tail.
func (t *dianaHistoryImagesTool) readPDFPage(ctx context.Context, parser *FileParserPlugin, channel Channel, ref fileRef, maxBytes int64, page, offset int) (string, error) {
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
	if page > session.PageCount() {
		return "", fmt.Errorf("PDF 共 %d 页，不能读取第 %d 页", session.PageCount(), page)
	}
	key := fmt.Sprintf("%x:%d", sha256.Sum256(data), page)
	t.mu.Lock()
	cached, found := t.pdfPageTexts[key]
	t.mu.Unlock()
	if found {
		return pdfPageChunk(cached, page, session.PageCount(), offset)
	}
	var content string
	if textSession, ok := session.(pdfTextPageSession); ok {
		content, err = textSession.PageText(ctx, page-1)
		if err != nil {
			return "", err
		}
	}
	if strings.TrimSpace(content) == "" {
		image, err := session.RenderJPEG(ctx, page-1)
		if err != nil {
			return "", err
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
		content, err = runPageVisionOCR(ctx, services, ref.Name, page, session.PageCount(), image)
		if err != nil {
			return "", err
		}
	}
	t.mu.Lock()
	if t.pdfPageTexts == nil {
		t.pdfPageTexts = make(map[string]string)
	}
	t.pdfPageTexts[key] = content
	t.mu.Unlock()
	return pdfPageChunk(content, page, session.PageCount(), offset)
}

func pdfPageChunk(content string, page, pages, offset int) (string, error) {
	text := []rune(content)
	if offset < 0 || offset > len(text) {
		return "", fmt.Errorf("page_offset 超出本页 %d 字范围", len(text))
	}
	end := min(offset+4000, len(text))
	result := map[string]any{"page": page, "total_pages": pages, "offset": offset, "total_chars": len(text), "text": string(text[offset:end]), "page_complete": end == len(text), "document_complete": end == len(text) && page == pages}
	if end < len(text) {
		result["next_page"] = page
		result["next_offset"] = end
	} else if page < pages {
		result["next_page"] = page + 1
		result["next_offset"] = 0
	}
	raw, err := json.Marshal(result)
	return string(raw), err
}
