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

// openHistoryPDF 沿用会话内文件的解析和下载上限，打开后可以连续读多页，不必每页
// 重新下载。key 是文件内容的摘要，拿来给每页正文做缓存。
func (t *dianaHistoryImagesTool) openHistoryPDF(ctx context.Context, parser *FileParserPlugin, channel Channel, ref fileRef, maxBytes int64) (pdfRenderSession, string, error) {
	ref = parser.resolveOneBotFile(ctx, channel, ref)
	data, _, contentType, err := parser.readRef(ctx, ref, maxBytes)
	if err != nil {
		return nil, "", err
	}
	if !looksPDF(ref.Name, contentType, data) {
		return nil, "", fmt.Errorf("该文件不是 PDF")
	}
	if parser.pdfRenderer == nil {
		return nil, "", fmt.Errorf("PDF 解析器不可用")
	}
	session, err := parser.pdfRenderer.Open(ctx, data)
	if err != nil {
		return nil, "", err
	}
	return session, fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

// pdfPageText 取一页的完整正文：有文字层直接取，没有就渲染后交给视觉模型识别。
// 结果按文件摘要和页码缓存，同一页续读不会再识别一遍。
func (t *dianaHistoryImagesTool) pdfPageText(ctx context.Context, session pdfRenderSession, digest, name string, page int) (string, error) {
	if page > session.PageCount() {
		return "", fmt.Errorf("PDF 共 %d 页，不能读取第 %d 页", session.PageCount(), page)
	}
	key := fmt.Sprintf("%s:%d", digest, page)
	t.mu.Lock()
	cached, found := t.pdfPageTexts[key]
	t.mu.Unlock()
	if found {
		return cached, nil
	}
	var content string
	if textSession, ok := session.(pdfTextPageSession); ok {
		var err error
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
		content, err = runPageVisionOCR(ctx, services, name, page, session.PageCount(), image)
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
	return content, nil
}

// readPDFPage 读一页里的一段。每次给的量够小，可以原样交给 subtask，不会丢掉页尾。
func (t *dianaHistoryImagesTool) readPDFPage(ctx context.Context, parser *FileParserPlugin, channel Channel, ref fileRef, maxBytes int64, page, offset int) (string, error) {
	if page < 1 || offset < 0 {
		return "", fmt.Errorf("pdf_page 必须大于 0，page_offset 不能为负数")
	}
	session, digest, err := t.openHistoryPDF(ctx, parser, channel, ref, maxBytes)
	if err != nil {
		return "", err
	}
	defer session.Close()
	content, err := t.pdfPageText(ctx, session, digest, ref.Name, page)
	if err != nil {
		return "", err
	}
	return pdfPageChunk(content, page, session.PageCount(), offset)
}

type pdfPageRangeItem struct {
	Page       int    `json:"page"`
	Offset     int    `json:"offset,omitempty"`
	TotalChars int    `json:"total_chars"`
	Text       string `json:"text"`
}

type pdfPageRangeResult struct {
	TotalPages int                `json:"total_pages"`
	Pages      []pdfPageRangeItem `json:"pages"`
	// NextPage 不为 0 时后面还有没给的内容，从这一页（NextOffset 是页内偏移）接着读。
	NextPage         int    `json:"next_page,omitempty"`
	NextOffset       int    `json:"next_offset,omitempty"`
	DocumentComplete bool   `json:"document_complete"`
	Message          string `json:"message,omitempty"`
}

// readPDFPages 一次读连续多页：从 page（页内 offset）读到 end，按整页装进 budget 字。
// 装不下的那一页留给下一次，结果里写明从哪一页接着读；第一页自己就超出时给到装得下
// 的位置，用 next_offset 续读。读到一半某页出错时已读的照给，写明停在哪一页。
func (t *dianaHistoryImagesTool) readPDFPages(ctx context.Context, parser *FileParserPlugin, channel Channel, ref fileRef, maxBytes int64, page, offset, end, budget int) (string, error) {
	if page < 1 || offset < 0 {
		return "", fmt.Errorf("pdf_page 必须大于 0，page_offset 不能为负数")
	}
	if end < page {
		return "", fmt.Errorf("pdf_page_end 不能小于 pdf_page")
	}
	session, digest, err := t.openHistoryPDF(ctx, parser, channel, ref, maxBytes)
	if err != nil {
		return "", err
	}
	defer session.Close()
	total := session.PageCount()
	if page > total {
		return "", fmt.Errorf("PDF 共 %d 页，不能读取第 %d 页", total, page)
	}
	end = min(end, total)
	result := pdfPageRangeResult{TotalPages: total, Pages: make([]pdfPageRangeItem, 0, end-page+1)}
	remaining := max(1, budget)
	for current := page; current <= end; current++ {
		content, err := t.pdfPageText(ctx, session, digest, ref.Name, current)
		if err != nil {
			if len(result.Pages) == 0 {
				return "", err
			}
			result.NextPage = current
			result.Message = fmt.Sprintf("第 %d 页读取失败：%s。前面的页已给出，从 next_page 重试。", current, err.Error())
			break
		}
		text := []rune(content)
		start := 0
		if current == page {
			if offset > len(text) {
				return "", fmt.Errorf("page_offset 超出本页 %d 字范围", len(text))
			}
			start = offset
		}
		if len(text)-start > remaining {
			if len(result.Pages) > 0 {
				result.NextPage = current
				result.Message = fmt.Sprintf("单次上限装到第 %d 页，第 %d–%d 页没给，从 next_page 接着读。", current-1, current, end)
				break
			}
			stop := start + remaining
			result.Pages = append(result.Pages, pdfPageRangeItem{Page: current, Offset: start, TotalChars: len(text), Text: string(text[start:stop])})
			result.NextPage, result.NextOffset = current, stop
			result.Message = fmt.Sprintf("第 %d 页超出单次上限，只给到第 %d 字，用 next_page、next_offset 接着读。", current, stop)
			break
		}
		result.Pages = append(result.Pages, pdfPageRangeItem{Page: current, Offset: start, TotalChars: len(text), Text: string(text[start:])})
		remaining -= len(text) - start
		if current == end && end < total {
			result.NextPage = end + 1
		}
	}
	result.DocumentComplete = result.NextPage == 0
	raw, err := json.Marshal(result)
	return string(raw), err
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
