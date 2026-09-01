// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func TestWorldTreeSaveAddsAndUpdates(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	var tree WorldTree

	tree, saved, err := tree.Save(WorldTreeNode{Title: " 枝江 ", Content: "故事发生在虚构城市枝江。", AlwaysOn: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID == "" || saved.Title != "枝江" {
		t.Fatalf("saved = %#v", saved)
	}
	if len(tree.Nodes) != 1 {
		t.Fatalf("nodes = %#v", tree.Nodes)
	}

	// 带同一个 ID 是改，不是再加一条。
	tree, updated, err := tree.Save(WorldTreeNode{ID: saved.ID, Title: "枝江", Content: "枝江靠海。", AlwaysOn: true}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Nodes) != 1 {
		t.Fatalf("update created a duplicate: %#v", tree.Nodes)
	}
	if updated.Content != "枝江靠海。" || !updated.UpdatedAt.After(saved.UpdatedAt) {
		t.Fatalf("updated = %#v", updated)
	}

	if _, _, err := tree.Save(WorldTreeNode{Content: "有正文但没标题"}, now); err == nil {
		t.Fatal("titleless node was accepted")
	}
}

func TestWorldTreeWithDefaultsBreaksCyclesAndDanglingParents(t *testing.T) {
	tree := WorldTree{Nodes: []WorldTreeNode{
		{ID: "a", ParentID: "b", Title: "甲"},
		{ID: "b", ParentID: "a", Title: "乙"},
		{ID: "c", ParentID: "missing", Title: "丙"},
		{ID: "d", ParentID: "d", Title: "丁"},
	}}.WithDefaults()
	// 环上至少要断开一处；悬空和自指父节点必须提回根。
	byID := map[string]WorldTreeNode{}
	for _, node := range tree.Nodes {
		byID[node.ID] = node
	}
	if byID["c"].ParentID != "" || byID["d"].ParentID != "" {
		t.Fatalf("dangling/self parents survived: %#v", tree.Nodes)
	}
	if byID["a"].ParentID == "b" && byID["b"].ParentID == "a" {
		t.Fatalf("cycle survived: %#v", tree.Nodes)
	}
	// 展开必须能正常终止并覆盖全部节点。
	if rows := tree.OrderedRows(); len(rows) != len(tree.Nodes) {
		t.Fatalf("ordered rows = %d, nodes = %d", len(rows), len(tree.Nodes))
	}
}

func TestWorldTreeDeleteReparentsChildren(t *testing.T) {
	tree := WorldTree{Nodes: []WorldTreeNode{
		{ID: "root", Title: "世界"},
		{ID: "chapter", ParentID: "root", Title: "枝江"},
		{ID: "leaf", ParentID: "chapter", Title: "港口", Content: "枝江港常年有雾。", Keywords: []string{"港口"}},
	}}.WithDefaults()

	tree = tree.Delete("chapter")
	if _, ok := tree.Find("chapter"); ok {
		t.Fatal("deleted node still present")
	}
	leaf, ok := tree.Find("leaf")
	if !ok || leaf.ParentID != "root" {
		t.Fatalf("child was not reparented: %#v", leaf)
	}
	// 重复删除不报错。
	if got := tree.Delete("chapter"); len(got.Nodes) != 2 {
		t.Fatalf("repeat delete changed nodes: %#v", got.Nodes)
	}
}

func TestWorldTreeImportRemapsParentReferences(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	var tree WorldTree
	tree, result := tree.Import([]WorldTreeNode{
		{ID: "file-root", Title: "世界", Content: "总纲", AlwaysOn: true},
		{ID: "file-child", ParentID: "file-root", Title: "枝江", Content: "城市设定", Keywords: []string{"枝江"}},
		{ParentID: "not-in-file", Title: "孤儿"},
		{Content: "没标题"},
	}, now)
	if result.Imported != 3 || result.Dropped != 1 {
		t.Fatalf("result = %#v", result)
	}
	rows := tree.OrderedRows()
	if len(rows) != 3 {
		t.Fatalf("rows = %#v", rows)
	}
	var child WorldTreeNode
	orphanIsRoot := false
	for _, node := range tree.Nodes {
		switch node.Title {
		case "枝江":
			child = node
		case "孤儿":
			orphanIsRoot = node.ParentID == ""
		}
		// 文件里的 ID 一律不落地：撞上本地条目就是静默覆盖。
		if node.ID == "file-root" || node.ID == "file-child" {
			t.Fatalf("file id was reused: %#v", node)
		}
	}
	parent, ok := tree.Find(child.ParentID)
	if !ok || parent.Title != "世界" {
		t.Fatalf("parent reference was not remapped: %#v", child)
	}
	if !orphanIsRoot {
		t.Fatal("orphan node was not promoted to root")
	}
}

func TestWorldTreeContextBlockInjectsAlwaysOnAndMatched(t *testing.T) {
	tree := WorldTree{Nodes: []WorldTreeNode{
		{ID: "world", Title: "世界", Content: "故事发生在虚构城市枝江。", AlwaysOn: true},
		{ID: "port", ParentID: "world", Title: "港口", Content: "枝江港常年有雾。", Keywords: []string{"港口", "码头"}},
		{ID: "tower", ParentID: "world", Title: "钟楼", Content: "钟楼午夜会自己敲钟。", Keywords: []string{"钟楼"}},
		{ID: "menu", ParentID: "world", Title: "目录"},
	}}.WithDefaults()

	block := tree.ContextBlock("明天去码头钓鱼", worldTreeContextTokenBudget)
	if !strings.Contains(block, "常驻设定") || !strings.Contains(block, "枝江") {
		t.Fatalf("always-on entry missing: %q", block)
	}
	if !strings.Contains(block, "世界 / 港口：枝江港常年有雾。") {
		t.Fatalf("matched entry missing path label: %q", block)
	}
	if strings.Contains(block, "钟楼") {
		t.Fatalf("unmatched entry injected: %q", block)
	}
	if strings.Contains(block, "目录") {
		t.Fatalf("catalog-only node injected: %q", block)
	}

	// 没命中触发词时只剩常驻段。
	quiet := tree.ContextBlock("随便聊聊", worldTreeContextTokenBudget)
	if strings.Contains(quiet, "相关的设定") {
		t.Fatalf("matched section rendered without hits: %q", quiet)
	}
	if !strings.Contains(quiet, "常驻设定") {
		t.Fatalf("always-on section missing: %q", quiet)
	}
}

func TestWorldTreeContextBlockDisabledChapterSkipsSubtree(t *testing.T) {
	off := boolPointer(false)
	tree := WorldTree{Nodes: []WorldTreeNode{
		{ID: "chapter", Title: "旧设定", Enabled: off},
		{ID: "leaf", ParentID: "chapter", Title: "细节", Content: "已废弃的设定。", AlwaysOn: true},
		{ID: "alive", Title: "现行", Content: "仍然生效的设定。", AlwaysOn: true},
	}}.WithDefaults()
	block := tree.ContextBlock("", worldTreeContextTokenBudget)
	if strings.Contains(block, "已废弃") {
		t.Fatalf("disabled subtree injected: %q", block)
	}
	if !strings.Contains(block, "仍然生效") {
		t.Fatalf("enabled entry missing: %q", block)
	}
}

func TestWorldTreeContextBlockHonorsTokenBudget(t *testing.T) {
	long := strings.Repeat("设定内容很长。", 40)
	tree := WorldTree{Nodes: []WorldTreeNode{
		{ID: "a", Title: "甲", Content: long, AlwaysOn: true},
		{ID: "b", Title: "乙", Content: long, AlwaysOn: true},
		{ID: "c", Title: "丙", Content: long, AlwaysOn: true},
	}}.WithDefaults()
	// 预算取「刚好装下开头和第一条」：后两条必须被裁掉。
	first := tree.ContextBlock("", 1<<20)
	budget := llm.EstimateTextTokens(first[:strings.Index(first, "乙")-len("\n- ")])
	block := tree.ContextBlock("", budget)
	if block == "" || !strings.Contains(block, "甲") {
		t.Fatal("budget dropped everything including the first entry")
	}
	if strings.Contains(block, "乙") || strings.Contains(block, "丙") {
		t.Fatalf("budget was not enforced: %d runes", len([]rune(block)))
	}
	// 一条都塞不下时不注入光杆开头。
	if got := tree.ContextBlock("", 10); got != "" {
		t.Fatalf("tiny budget produced header-only block: %q", got)
	}
}

func TestWorldTreeContextBlockEmptyTree(t *testing.T) {
	if got := (WorldTree{}).ContextBlock("枝江", worldTreeContextTokenBudget); got != "" {
		t.Fatalf("empty tree produced context: %q", got)
	}
	// 只有目录节点、没有可注入内容时同样安静。
	tree := WorldTree{Nodes: []WorldTreeNode{{ID: "menu", Title: "目录"}}}.WithDefaults()
	if got := tree.ContextBlock("目录", worldTreeContextTokenBudget); got != "" {
		t.Fatalf("catalog-only tree produced context: %q", got)
	}
}
