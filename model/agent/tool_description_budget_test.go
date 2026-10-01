// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// 工具描述每轮都随请求发出，写长了不会报错，只会悄悄变贵。这里静态扫一遍工具描述
// 字面量，按 docs/tool-descriptions.md 的上限卡住。
//
// 只量得到能在编译期拼出来的部分（字面量、包级常量和它们的 + 拼接），运行时拼的
// 片段按零长度算，所以量出来的是下限。
const (
	toolMainDescriptionMaxRunes  = 300
	toolParamDescriptionMaxRunes = 60
)

// 确实需要超长的写在这里并写明原因。行号会随编辑漂移，所以键用「文件名:描述开头十个字」。
var toolDescriptionBudgetExemptions = map[string]string{
	// 五种头像来源写法本身就占 70 多字，不能改成 enum（带 <user_id> 占位）。
	"image_agent_tool.go:点名头像，优先于当前": "头像来源取值清单",
}

var toolParamHelper = regexp.MustCompile(`^tool[A-Za-z]*Param$`)

func TestToolDescriptionsStayWithinBudget(t *testing.T) {
	var violations []string
	for _, dir := range []string{".", filepath.Join("..", "assistant")} {
		violations = append(violations, scanToolDescriptions(t, dir)...)
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("工具描述超出 docs/tool-descriptions.md 的上限（主描述 %d 字、参数 %d 字）：\n%s",
			toolMainDescriptionMaxRunes, toolParamDescriptionMaxRunes, strings.Join(violations, "\n"))
	}
}

func scanToolDescriptions(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
	}
	consts := packageStringConsts(files)
	var violations []string
	check := func(node ast.Node, expr ast.Expr, limit int, kind string) {
		text := evalStringExpr(expr, consts)
		if utf8.RuneCountInString(text) <= limit {
			return
		}
		pos := fset.Position(node.Pos())
		key := filepath.Base(pos.Filename) + ":" + string([]rune(text)[:10])
		if _, ok := toolDescriptionBudgetExemptions[key]; ok {
			return
		}
		violations = append(violations, pos.String()+" "+kind+" "+strconv.Itoa(utf8.RuneCountInString(text))+" 字（"+key+"）")
	}
	for _, file := range files {
		ast.Inspect(file, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.FuncDecl:
				if n.Recv == nil || n.Name.Name != "Description" || n.Type.Params.NumFields() != 0 || n.Body == nil {
					return true
				}
				ast.Inspect(n.Body, func(inner ast.Node) bool {
					if ret, ok := inner.(*ast.ReturnStmt); ok && len(ret.Results) == 1 {
						check(ret, ret.Results[0], toolMainDescriptionMaxRunes, "主描述")
					}
					return true
				})
			case *ast.CallExpr:
				if ident, ok := n.Fun.(*ast.Ident); ok && toolParamHelper.MatchString(ident.Name) && len(n.Args) > 0 {
					check(n, n.Args[0], toolParamDescriptionMaxRunes, "参数说明")
				}
			case *ast.KeyValueExpr:
				switch key := n.Key.(type) {
				case *ast.BasicLit:
					if key.Value == `"description"` {
						check(n, n.Value, toolParamDescriptionMaxRunes, "参数说明")
					}
				case *ast.Ident:
					if key.Name == "Description" {
						check(n, n.Value, toolMainDescriptionMaxRunes, "主描述")
					}
				}
			}
			return true
		})
	}
	return violations
}

// packageStringConsts 收集包级字符串常量，常量之间的引用反复求值直到不再变化。
func packageStringConsts(files []*ast.File) map[string]string {
	exprs := map[string]ast.Expr{}
	for _, file := range files {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || (gen.Tok != token.CONST && gen.Tok != token.VAR) {
				continue
			}
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok || len(value.Names) != len(value.Values) {
					continue
				}
				for i, name := range value.Names {
					exprs[name.Name] = value.Values[i]
				}
			}
		}
	}
	consts := map[string]string{}
	for changed := true; changed; {
		changed = false
		for name, expr := range exprs {
			if text := evalStringExpr(expr, consts); text != consts[name] {
				consts[name] = text
				changed = true
			}
		}
	}
	return consts
}

func evalStringExpr(expr ast.Expr, consts map[string]string) string {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return ""
		}
		value, err := strconv.Unquote(e.Value)
		if err != nil {
			return ""
		}
		return value
	case *ast.Ident:
		return consts[e.Name]
	case *ast.ParenExpr:
		return evalStringExpr(e.X, consts)
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return ""
		}
		return evalStringExpr(e.X, consts) + evalStringExpr(e.Y, consts)
	case *ast.CallExpr:
		// fmt.Sprintf / fmt.Sprint 的格式串本身就是描述正文。
		if sel, ok := e.Fun.(*ast.SelectorExpr); ok && strings.HasPrefix(sel.Sel.Name, "Sprint") && len(e.Args) > 0 {
			return evalStringExpr(e.Args[0], consts)
		}
	}
	return ""
}
