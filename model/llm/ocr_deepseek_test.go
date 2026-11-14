// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestDeepSeekOCRUploadsMultipartAndCleansLayoutTokens(t *testing.T) {
	server := vendorServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body []byte) {
		if r.URL.Path != "/v1/tools/ocr" || r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("path=%s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		text := string(body)
		purposeAt, fileAt := strings.Index(text, `name="purpose"`), strings.Index(text, `name="file"; filename="scan.pdf"`)
		if purposeAt < 0 || fileAt < 0 || !strings.Contains(text, "text_recognition") {
			t.Errorf("multipart body = %q", text)
		}
		// 识别期间先发换行保活。
		_, _ = w.Write([]byte("\n\n" + `{"object":"ocr.result","page_count":2,"pages":[` +
			`{"index":0,"text":"<｜ref｜>标题<｜/ref｜><｜det｜>[[10, 20, 300, 40]]<｜/det｜>\n正文第一行  \n\n\n\n正文第二行","finish_reason":"stop"},` +
			`{"index":1,"text":"<|ref|>第二页<|/ref|><|det|>[[0,0,1000,1000]]<|/det|>","finish_reason":"length"}]}`))
	})
	pages, err := DeepSeekOCR(context.Background(), ProviderConfig{APIKey: "sk-test", BaseURL: server.URL}, DeepSeekOCRRequest{Data: []byte("%PDF"), Filename: "scan.pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 || pages[0].Text != "标题\n正文第一行\n\n正文第二行" || pages[1].Text != "第二页" || pages[0].Truncated || !pages[1].Truncated {
		t.Fatalf("pages = %#v", pages)
	}
}

func TestDeepSeekOCRReportsErrorInsideOKBody(t *testing.T) {
	server := vendorServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request, body []byte) {
		_, _ = w.Write([]byte("\n" + `{"error":{"message":"recognition timed out","type":"gateway_timeout_error"}}`))
	})
	noMediaRetryWait(t)
	_, err := DeepSeekOCR(context.Background(), ProviderConfig{APIKey: "sk-test", BaseURL: server.URL + "/v1"}, DeepSeekOCRRequest{Data: []byte("x")})
	if err == nil || MediaErrorKindOf(err) != MediaErrorUpstream || !strings.Contains(err.Error(), "recognition timed out") {
		t.Fatalf("err = %v", err)
	}
}
