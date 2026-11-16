package agent

import (
	"strings"
	"testing"
)

// 照 Pi 的 inlineBudget：带签名的行从开销小的放起，放到预算用完；放不下的只在最后
// 一行列名字。FeaturedTools 不管预算总是写全，而且先占预算。
func TestDeferredCatalogFitsTokenBudget(t *testing.T) {
	long := strings.Repeat("很长的工具说明。", 2000)
	registry := NewToolRegistry(
		&countingTool{name: "common"},
		&describedTool{"small", "小工具"},
		&describedTool{"huge", long},
		&describedTool{"star", "重点工具"},
	)
	loader := newDeferredToolLoader(registry, []string{"common"}, []string{"star"})
	catalog := loader.catalog()
	if !strings.Contains(catalog, "- small(") || !strings.Contains(catalog, "- star(") {
		t.Fatalf("cheap and featured tools must be listed in full:\n%.300s", catalog)
	}
	if strings.Contains(catalog, "- huge(") || !strings.HasSuffix(catalog, "（只列名字，要用先 "+ToolsLoadToolName+" 取定义）：huge") {
		t.Fatalf("a tool over the budget must be listed by name only:\n%s", catalog)
	}
	if loader.catalog() != catalog {
		t.Fatal("catalog must be deterministic")
	}

	// 重点工具再长也写全；它把预算用完，其余只列名字。
	featuredLong := newDeferredToolLoader(registry, []string{"common"}, []string{"huge"}).catalog()
	if !strings.Contains(featuredLong, "- huge(") || strings.Contains(featuredLong, "- small(") || !strings.Contains(featuredLong, "small、star") {
		t.Fatalf("a long featured tool must stay in full and push the rest to names:\n%.200s", featuredLong[len(featuredLong)-200:])
	}
}
