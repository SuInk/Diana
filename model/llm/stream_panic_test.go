// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecoverChatStreamPanicReturnsRequestError(t *testing.T) {
	t.Parallel()

	out := make(chan ChatEvent, 1)
	go func() {
		defer close(out)
		defer recoverChatStreamPanic(context.Background(), out, "test-provider")
		panic("malformed stream event")
	}()

	select {
	case event := <-out:
		if event.Type != ChatEventError || !strings.Contains(event.Error, "test-provider stream panicked: malformed stream event") {
			t.Fatalf("panic event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("panic guard did not return an error event")
	}
}

// Any new anonymous goroutine in the LLM package must choose an explicit panic
// boundary. SDK stream decoders run outside Gin's recovery middleware, so one
// missed goroutine can otherwise terminate the whole Diana process.
func TestProductionLLMGoroutinesHavePanicGuards(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") || filepath.Base(file) == "stream_panic.go" {
			continue
		}
		node, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(node, func(node ast.Node) bool {
			statement, ok := node.(*ast.GoStmt)
			if !ok {
				return true
			}
			function, ok := statement.Call.Fun.(*ast.FuncLit)
			if !ok {
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
				t.Errorf("unguarded LLM goroutine at %s", fset.Position(statement.Pos()))
			}
			return true
		})
	}
}
