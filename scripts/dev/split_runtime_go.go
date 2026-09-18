// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

//go:build ignore

// Split model/assistant/runtime.go into runtime_<theme>.go files by
// top-level function theme. Only FuncDecls move; types/vars/consts stay.
// Run from the repository root: go run ./scripts/dev/split_runtime_go.go
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const target = "model/assistant/runtime.go"

var themes = []struct {
	suffix string
	pat    *regexp.Regexp
}{
	{"outbound", regexp.MustCompile(`(?i)outbound|sendOutgoing|sendChunk|delivery|deliverMessage|broadcast|relayMessage|sendText|sendImage|sendVoice|typing|replyMerge|chunk`)},
	{"inbound", regexp.MustCompile(`(?i)inbound|ingest|enqueue|dequeue|processMessage|handleEvent|onMessage|queueItem|gap`)},
	{"llm", regexp.MustCompile(`(?i)llm|prompt|completion|chatEvent|buildMessage|messageHistory|contextHistory|token|budget|modelConfig|provider`)},
	{"memory", regexp.MustCompile(`(?i)memory|recall|semantic|notebook|persona|worldBook|favorability|relationship|portrait`)},
	{"image", regexp.MustCompile(`(?i)image|avatar|sticker|ocr|media|voice|audio|video`)},
	{"tool", regexp.MustCompile(`(?i)tool|plugin|agent|schedule|reminder|subscribe|repository|skill`)},
}

const header = `// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant
`

func classify(name string) string {
	for _, t := range themes {
		if t.pat.MatchString(name) {
			return t.suffix
		}
	}
	return ""
}

func main() {
	fset := token.NewFileSet()
	src, err := os.ReadFile(target)
	if err != nil {
		panic(err)
	}
	f, err := parser.ParseFile(fset, target, src, parser.ParseComments)
	if err != nil {
		panic(err)
	}

	groups := map[string][]*ast.FuncDecl{}
	var core []*ast.FuncDecl
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if s := classify(fn.Name.Name); s != "" {
			groups[s] = append(groups[s], fn)
		} else {
			core = append(core, fn)
		}
	}

	offset := func(p token.Pos) int { return fset.Position(p).Offset }
	end := func(fn *ast.FuncDecl) int {
		if fn.Body != nil {
			return offset(fn.Body.End())
		}
		// declaration without body (interface-style); use type end
		return offset(fn.Type.End())
	}
	start := func(fn *ast.FuncDecl) int {
		if fn.Doc != nil {
			return offset(fn.Doc.Pos())
		}
		return offset(fn.Pos())
	}

	// Write themed files.
	for suffix, fns := range groups {
		var b strings.Builder
		b.WriteString(header)
		for _, fn := range fns {
			b.WriteString("\n")
			b.Write(src[start(fn):end(fn)])
			b.WriteString("\n")
		}
		out := filepath.Join(filepath.Dir(target), "runtime_"+suffix+".go")
		if err := os.WriteFile(out, []byte(b.String()), 0o644); err != nil {
			panic(err)
		}
		fmt.Printf("wrote %s (%d funcs)\n", out, len(fns))
	}

	// Rebuild runtime.go: keep everything before the first moved decl's
	// position would appear, i.e. remove moved spans from src.
	remove := map[[2]int]bool{}
	for _, fns := range groups {
		for _, fn := range fns {
			remove[[2]int{start(fn), end(fn)}] = true
		}
	}
	var kept strings.Builder
	for i := 0; i < len(src); {
		matched := false
		for span := range remove {
			if i == span[0] {
				kept.WriteString("\n")
				i = span[1]
				matched = true
				break
			}
		}
		if !matched {
			kept.WriteByte(src[i])
			i++
		}
	}
	clean := regexp.MustCompile(`\n{3,}`).ReplaceAllString(kept.String(), "\n\n")
	clean = regexp.MustCompile(` +\n`).ReplaceAllString(clean, "\n")
	if err := os.WriteFile(target, []byte(clean), 0o644); err != nil {
		panic(err)
	}
	fmt.Printf("runtime.go rebuilt with %d core funcs\n", len(core))
}
