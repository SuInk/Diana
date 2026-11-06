// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

// 多张源图要用 image[]：官方接口收到重复的 image 会回 400 duplicate_parameter，
// 线上 gpt-image-2.6 多图改图因此全部失败。一张图照旧发 image。
func TestOpenAICompatibleEditImageFieldNames(t *testing.T) {
	for _, tc := range []struct {
		name   string
		images []string
		want   string
	}{
		{"一张图", []string{"data:image/png;base64,aGVsbG8="}, "image:1"},
		{"多张图", []string{"data:image/png;base64,aGVsbG8=", "data:image/png;base64,d29ybGQ=", "data:image/png;base64,IQ=="}, "image[]:3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var fields []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseMultipartForm(4 << 20); err != nil {
					t.Fatalf("ParseMultipartForm error = %v", err)
				}
				for field, headers := range r.MultipartForm.File {
					fields = append(fields, field+":"+string(rune('0'+len(headers))))
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":[{"b64_json":"ZWRpdA=="}]}`))
			}))
			defer server.Close()

			_, err := EditImage(context.Background(), ProviderConfig{
				Provider: ProviderOpenAICompatible, APIKey: "secret", BaseURL: server.URL + "/v1",
				Model: "gpt-test", ImageModel: "gpt-image-2.6-sunburst",
			}, ImageEditRequest{Prompt: "合成一张", Images: tc.images}, WithHTTPClient(server.Client()))
			if err != nil {
				t.Fatal(err)
			}
			sort.Strings(fields)
			if got := strings.Join(fields, ","); got != tc.want {
				t.Fatalf("fields = %s, want %s", got, tc.want)
			}
		})
	}
}
