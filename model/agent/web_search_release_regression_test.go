package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestSearchRecoversUnindexedReleaseURLWithoutLosingVersion(t *testing.T) {
	var queries []string
	var queriesMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Query string `json:"query"`
		}
		json.NewDecoder(r.Body).Decode(&input)
		queriesMu.Lock()
		queries = append(queries, input.Query)
		queriesMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(input.Query, "site:") {
			w.Write([]byte(`{"results":[]}`))
			return
		}
		if !strings.Contains(input.Query, "gitea") || !strings.Contains(input.Query, "v1.27.3") {
			t.Errorf("lost repository/version: %q", input.Query)
		}
		w.Write([]byte(`{"results":[{"title":"Gitea 1.27.3 is released","url":"https://blog.gitea.com/release-of-1.27.3/","content":"Released on 2026-08-29","published_date":"2026-08-29"}]}`))
	}))
	defer server.Close()
	tool, err := NewWebSearchTool(WebSearchToolOptions{Config: WebSearchConfig{Providers: []WebSearchProviderConfig{{Name: "test", Type: "tavily", URL: server.URL}}}, APIKeys: map[string]string{"test": "test-key"}, MaxQueries: 2, MaxProviderCalls: 2})
	if err != nil {
		t.Fatal(err)
	}
	output, err := tool.Run(context.Background(), map[string]any{"query": "site:github.com/go-gitea/gitea/releases/tag/v1.27.3", "queries": []string{"gitea v1.27.3 release"}})
	if err != nil {
		t.Fatal(err)
	}
	var result webSearchResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "ok" || len(queries) != 2 || result.Queries[1].Strategy != "model_alternative" || result.SourceStage != sourceStageSearchCandidates || result.RetrievedAt == "" || result.StopReason != "queries_completed" {
		t.Fatalf("incorrect recovery: %s", output)
	}
	if !strings.Contains(result.SourceNotice, "不证明") || !strings.Contains(result.SourceNotice, "索引更新时间") {
		t.Fatalf("missing evidence limits: %s", result.SourceNotice)
	}
}
