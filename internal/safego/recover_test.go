// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package safego

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// Go has no process-wide panic recovery: a panic must be recovered in the same
// goroutine. Keep this repository-wide so new background work cannot silently
// reintroduce a process-killing boundary.
func TestEveryProductionGoroutineHasPanicRecovery(t *testing.T) {
	t.Parallel()

	root := filepath.Clean(filepath.Join("..", ".."))
	files, err := filepath.Glob(filepath.Join(root, "**", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	// filepath.Glob does not make ** recursive; enumerate the known Go roots.
	files = nil
	for _, pattern := range []string{"cmd/*/*.go", "webui/*.go", "model/*/*.go", "internal/*/*.go"} {
		matched, globErr := filepath.Glob(filepath.Join(root, pattern))
		if globErr != nil {
			t.Fatal(globErr)
		}
		files = append(files, matched...)
	}
	fset := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		node, parseErr := parser.ParseFile(fset, file, nil, 0)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		ast.Inspect(node, func(node ast.Node) bool {
			statement, ok := node.(*ast.GoStmt)
			if !ok {
				return true
			}
			function, ok := statement.Call.Fun.(*ast.FuncLit)
			if !ok {
				t.Errorf("named goroutine has no visible panic boundary at %s", fset.Position(statement.Pos()))
				return true
			}
			guarded := false
			for _, item := range function.Body.List {
				deferred, ok := item.(*ast.DeferStmt)
				if !ok {
					continue
				}
				identifier, ok := deferred.Call.Fun.(*ast.Ident)
				if ok && strings.HasPrefix(identifier.Name, "recover") {
					guarded = true
					break
				}
			}
			if !guarded {
				t.Errorf("goroutine has no panic recovery at %s", fset.Position(statement.Pos()))
			}
			return true
		})
	}
}
