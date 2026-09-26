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

	fset, files := parseProductionFiles(t)
	for _, node := range files {
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

// recover() only stops a panic when called directly by the deferred function.
// Every recover* helper is deferred at call sites, so each one must call the
// builtin itself; delegating to safego.Recover recovers nothing.
func TestEveryRecoverHelperCallsRecoverDirectly(t *testing.T) {
	t.Parallel()

	fset, files := parseProductionFiles(t)
	for _, node := range files {
		for _, declaration := range node.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Recv != nil || function.Body == nil || !strings.HasPrefix(function.Name.Name, "recover") {
				continue
			}
			if node.Name.Name == "safego" && function.Name.Name == "Recover" {
				continue
			}
			if !callsBuiltinRecover(function.Body) {
				t.Errorf("%s at %s must call recover() directly and pass the value to safego.Report",
					function.Name.Name, fset.Position(function.Pos()))
			}
		}
	}
}

// callsBuiltinRecover ignores nested function literals: a recover() inside
// them runs in a different frame and does not count.
func callsBuiltinRecover(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(node ast.Node) bool {
		if found {
			return false
		}
		switch node := node.(type) {
		case *ast.FuncLit:
			return false
		case *ast.CallExpr:
			if identifier, ok := node.Fun.(*ast.Ident); ok && identifier.Name == "recover" && len(node.Args) == 0 {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func parseProductionFiles(t *testing.T) (*token.FileSet, []*ast.File) {
	t.Helper()
	root := filepath.Clean(filepath.Join("..", ".."))
	// filepath.Glob does not make ** recursive; enumerate the known Go roots.
	var paths []string
	for _, pattern := range []string{"cmd/*/*.go", "webui/*.go", "model/*/*.go", "internal/*/*.go", "internal/*/*/*.go"} {
		matched, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, matched...)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		node, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, node)
	}
	return fset, files
}
