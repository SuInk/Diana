// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestClaimEvidenceLedgerKeepsPartialSuccess(t *testing.T) {
	ledger := newClaimEvidenceLedger()
	metadata := ledger.prepareSearch(map[string]any{
		"claims": []any{
			map[string]any{"id": "identity", "statement": "实体身份是否可确认"},
			map[string]any{"id": "availability", "statement": "指定条件下是否可用"},
		},
		"claim_ids": []any{"identity"},
	})
	if metadata["claim_count"] != 2 {
		t.Fatalf("prepare metadata=%#v", metadata)
	}
	result := webSearchResult{Status: "ok", StopReason: "sufficient_evidence", Sources: []string{"https://official.example/about"}}
	raw, _ := json.Marshal(result)
	observed := ledger.observeSearch(string(raw), nil)
	if observed["candidate_source_count"] != 1 {
		t.Fatalf("observe metadata=%#v", observed)
	}
	updates := []ClaimUpdate{
		{
			ID: "identity", Status: ClaimStatusSupported, Summary: "官方资料确认了实体身份。",
			Evidence: []ClaimEvidence{{URL: "https://official.example/about", Relation: "supports", SourceType: "first_party", Distance: "direct", Strength: "high"}},
		},
		{ID: "availability", Status: ClaimStatusNotSearched, Summary: "尚未检索指定条件。"},
	}
	if reason, ok := ledger.validateFinal(updates); !ok {
		t.Fatalf("valid partial result rejected: %s", reason)
	}
	traces := ledger.traces()
	if len(traces) != 2 || traces[0].Status != ClaimStatusSupported || traces[1].Status != ClaimStatusNotSearched {
		t.Fatalf("traces=%#v", traces)
	}
	if traces[0].Evidence[0].Domain != "official.example" {
		t.Fatalf("evidence=%#v", traces[0].Evidence)
	}
}

func TestClaimEvidenceLedgerRejectsUnsupportedAndUnknownSources(t *testing.T) {
	ledger := newClaimEvidenceLedger()
	ledger.prepareSearch(map[string]any{
		"claims":    []any{map[string]any{"id": "c1", "statement": "待验证事实"}},
		"claim_ids": []any{"c1"},
	})
	result := webSearchResult{Status: "no_results", StopReason: "all_queries_exhausted"}
	raw, _ := json.Marshal(result)
	ledger.observeSearch(string(raw), nil)
	updates := []ClaimUpdate{{
		ID: "c1", Status: ClaimStatusSupported, Summary: "声称已确认",
		Evidence: []ClaimEvidence{{URL: "https://invented.example/fact", Relation: "supports", SourceType: "secondary", Distance: "direct", Strength: "high"}},
	}}
	if reason, ok := ledger.validateFinal(updates); ok || !strings.Contains(reason, "invented.example") {
		t.Fatalf("unsupported final accepted reason=%q ok=%v traces=%#v", reason, ok, ledger.traces())
	}
	if got := ledger.traces()[0].Status; got != ClaimStatusInsufficient {
		t.Fatalf("status=%q, want insufficient", got)
	}
}

func TestClaimEvidenceLedgerKeepsAllowedSourceWithConservativeMetadata(t *testing.T) {
	ledger := newClaimEvidenceLedger()
	ledger.prepareSearch(map[string]any{
		"claims":    []any{map[string]any{"id": "c1", "statement": "待验证事实"}},
		"claim_ids": []any{"c1"},
	})
	result := webSearchResult{Status: "ok", StopReason: "sufficient_evidence", Sources: []string{"https://official.example/record"}}
	raw, _ := json.Marshal(result)
	ledger.observeSearch(string(raw), nil)
	updates := []ClaimUpdate{{
		ID: "c1", Status: ClaimStatusSupported, Summary: "官方记录支持该事实",
		Evidence: []ClaimEvidence{{
			URL: "https://official.example/record", Relation: "direct", SourceType: "官方原始资料", Distance: "primary", Strength: "strong",
		}},
	}}
	if reason, ok := ledger.validateFinal(updates); !ok {
		t.Fatalf("allowed source rejected: %s", reason)
	}
	evidence := ledger.traces()[0].Evidence[0]
	if evidence.Relation != "supports" || evidence.SourceType != "unknown" || evidence.Distance != "secondary" || evidence.Strength != "low" {
		t.Fatalf("evidence was not conservatively normalized: %#v", evidence)
	}
}

func TestClaimEvidenceDigestKeepsProtocolOutOfUserContent(t *testing.T) {
	ledger := newClaimEvidenceLedger()
	ledger.prepareSearch(map[string]any{
		"claims":    []any{map[string]any{"id": "c1", "statement": "待验证事实"}},
		"claim_ids": []any{"c1"},
	})
	digest := ledger.digest()
	for _, expected := range []string{"仅供内部校验", "c1 [not_searched]", "不得出现在最终回复正文里"} {
		if !strings.Contains(digest, expected) {
			t.Fatalf("digest missing %q: %s", expected, digest)
		}
	}
}

func TestClaimEvidenceLedgerIsDomainNeutral(t *testing.T) {
	statements := []string{
		"一则事件报道是否准确",
		"一个软件故障原因是否成立",
		"某人物与组织的关系是否存在",
		"一项学术结论是否有原始研究支持",
		"某项商品或服务在指定条件下是否可用",
		"某地点的开放状态是否仍有效",
	}
	ledger := newClaimEvidenceLedger()
	claims := make([]any, 0, len(statements))
	ids := make([]any, 0, len(statements))
	for index, statement := range statements {
		id := "c" + string(rune('1'+index))
		claims = append(claims, map[string]any{"id": id, "statement": statement})
		ids = append(ids, id)
	}
	ledger.prepareSearch(map[string]any{"claims": claims, "claim_ids": ids})
	if !ledger.active || len(ledger.traces()) != len(statements) {
		t.Fatalf("domain-neutral claims=%#v", ledger.traces())
	}
	for _, trace := range ledger.traces() {
		if trace.Status != ClaimStatusNotSearched {
			t.Fatalf("initial trace=%#v", trace)
		}
	}
}

func TestClaimEvidenceLedgerRecordsRejectedQueryWithoutRawText(t *testing.T) {
	ledger := newClaimEvidenceLedger()
	ledger.prepareSearch(map[string]any{
		"claims": []any{map[string]any{"id": "c1", "statement": "待验证事实"}}, "claim_ids": []any{"c1"},
	})
	ledger.recordRejectedSearch(map[string]any{"query": "private raw query"}, "search_call_limit")
	digest := ledger.digest()
	if ledger.lastRejectedHash == "" || strings.Contains(digest, "private raw query") || !strings.Contains(digest, "last_rejected_query_hash") {
		t.Fatalf("rejected query digest=%s", digest)
	}
}
func TestClaimSchemaBindsEvidenceToRetrievedSources(t *testing.T) {
	ledger := newClaimEvidenceLedger()
	ledger.prepareSearch(map[string]any{
		"claims":    []any{map[string]any{"id": "c1", "statement": "待验证事实"}},
		"claim_ids": []any{"c1"},
	})
	result := webSearchResult{Status: "ok", StopReason: "sufficient_evidence", Sources: []string{"https://official.example/record"}}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	ledger.observeSearch(string(raw), nil)

	schema := claimUpdateSchema(ledger.declaredClaimIDs(), ledger.allowedSourceURLs())
	properties, _ := schema["properties"].(map[string]any)
	id, _ := properties["id"].(map[string]any)
	if enum, _ := id["enum"].([]string); len(enum) != 1 || enum[0] != "c1" {
		t.Fatalf("claim id enum=%#v", id["enum"])
	}
	evidence, _ := properties["evidence"].(map[string]any)
	items, _ := evidence["items"].(map[string]any)
	evidenceProperties, _ := items["properties"].(map[string]any)
	url, _ := evidenceProperties["url"].(map[string]any)
	enum, _ := url["enum"].([]string)
	if len(enum) != 1 || enum[0] != "https://official.example/record" {
		t.Fatalf("evidence url enum=%#v", url["enum"])
	}
}
