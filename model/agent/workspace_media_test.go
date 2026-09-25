// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, width, height))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func testJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, width, height)), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestSniffMediaTypeCoversChatFormats(t *testing.T) {
	cases := map[string][]byte{
		"image/png":       testPNG(t, 2, 2),
		"image/jpeg":      testJPEG(t, 2, 2),
		"image/svg+xml":   []byte(`<?xml version="1.0"?><!-- logo --><svg xmlns="http://www.w3.org/2000/svg"></svg>`),
		"text/html":       []byte(`<html><body><svg></svg></body></html>`),
		"audio/silk":      []byte("\x02#!SILK_V3 rest"),
		"audio/amr":       []byte("#!AMR\n rest"),
		"audio/flac":      []byte("fLaC rest"),
		"audio/mpeg":      {0xFF, 0xFB, 0x90, 0x00, 0x01},
		"video/quicktime": append([]byte{0, 0, 0, 20}, []byte("ftypqt  0000")...),
		"image/heic":      append([]byte{0, 0, 0, 24}, []byte("ftypheic0000")...),
		"application/pdf": []byte("%PDF-1.7\n"),
		"text/plain":      []byte("你好，这是一段文本"),
	}
	for want, data := range cases {
		if got := SniffMediaType(data); got != want {
			t.Errorf("SniffMediaType(%q...) = %s, want %s", string(data[:min(len(data), 12)]), got, want)
		}
	}
}

func TestCorrectFileExtension(t *testing.T) {
	cases := []struct{ name, mediaType, want string }{
		{"photo.png", "image/jpeg", "photo.jpg"},
		{"photo.jpeg", "image/jpeg", "photo.jpeg"},
		// .jfif 永远纠正成 .jpg：send_attachment 和各平台都认 .jpg。
		{"image.jfif", "image/jpeg", "image.jpg"},
		{"notes.md", "text/plain", "notes.md"},
		{"fake.png", "text/plain", "fake.txt"},
		{"report.docx", "application/zip", "report.docx"},
		{"archive", "application/zip", "archive.zip"},
		{"voice", "audio/silk", "voice.silk"},
		{"mystery.dat", "application/octet-stream", "mystery.dat"},
		{"mystery", "application/octet-stream", "mystery.bin"},
		{"clip.m4v", "video/mp4", "clip.m4v"},
	}
	for _, c := range cases {
		if got := CorrectFileExtension(c.name, c.mediaType); got != c.want {
			t.Errorf("CorrectFileExtension(%q, %q) = %q, want %q", c.name, c.mediaType, got, c.want)
		}
	}
	if ext := CanonicalMediaExtension("image/jpeg"); ext != ".jpg" {
		t.Fatalf("image/jpeg 的扩展名应是 .jpg，得到 %s", ext)
	}
}

func TestWriteWorkspaceBytesUniqueNamesAndGuards(t *testing.T) {
	workDir := t.TempDir()
	cfg := Config{WorkDir: workDir}
	data := testPNG(t, 3, 3)
	first, err := WriteWorkspaceBytes(cfg, "downloads/cat.png", data, WorkspaceWriteOptions{})
	if err != nil || first != "downloads/cat.png" {
		t.Fatalf("first write: %q %v", first, err)
	}
	second, err := WriteWorkspaceBytes(cfg, "downloads/cat.png", data, WorkspaceWriteOptions{})
	if err != nil || second != "downloads/cat-2.png" {
		t.Fatalf("同名不覆盖时应自动加序号: %q %v", second, err)
	}
	overwritten, err := WriteWorkspaceBytes(cfg, "downloads/cat.png", []byte("x"), WorkspaceWriteOptions{Overwrite: true})
	if err != nil || overwritten != "downloads/cat.png" {
		t.Fatalf("overwrite: %q %v", overwritten, err)
	}
	if got, _ := os.ReadFile(filepath.Join(workDir, "downloads", "cat.png")); string(got) != "x" {
		t.Fatalf("overwrite 没生效: %q", got)
	}
	for _, rel := range []string{"../escape.png", "/abs.png", ".mcp.json", ".trash/x.png", "coding-runtime/auth/token.json"} {
		if saved, err := WriteWorkspaceBytes(cfg, rel, data, WorkspaceWriteOptions{}); err == nil {
			t.Fatalf("%s 应该被拒绝，却写到了 %s", rel, saved)
		}
	}
}

func TestWriteFileRejectsBinaryExtensions(t *testing.T) {
	workDir := t.TempDir()
	registry, err := NewDefaultToolRegistry(Config{WorkDir: workDir, FileWriteEnabled: true}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	tool, _ := registry.Get("write_file")
	for _, name := range []string{"image.png", "photo.JPG", "doc.pdf", "clip.mp4", "bundle.zip"} {
		_, err := tool.Run(context.Background(), map[string]any{"path": name, "content": "一段文字描述"})
		if err == nil || !strings.Contains(err.Error(), "save_to_workspace") {
			t.Fatalf("write_file 写 %s 应被拒绝并指向 save_to_workspace，得到 %v", name, err)
		}
		if _, statErr := os.Stat(filepath.Join(workDir, name)); statErr == nil {
			t.Fatalf("%s 被拒绝了却还是建出了文件", name)
		}
	}
	if _, err := tool.Run(context.Background(), map[string]any{"path": "logo.svg", "content": "<svg></svg>"}); err != nil {
		t.Fatalf("svg 是文本，应该能写: %v", err)
	}
}

func TestReadFileRejectsBinary(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "photo.png"), testPNG(t, 4, 4), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "notes.txt"), []byte("a\r\nb\nc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := NewDefaultToolRegistry(Config{WorkDir: workDir}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	tool, _ := registry.Get("read_file")
	_, err = tool.Run(context.Background(), map[string]any{"path": "photo.png"})
	if err == nil || !strings.Contains(err.Error(), "view_image") || !strings.Contains(err.Error(), "image/png") {
		t.Fatalf("read_file 读图片应被拒绝并提示 view_image，得到 %v", err)
	}
	out, err := tool.Run(context.Background(), map[string]any{"path": "notes.txt", "offset": 2, "limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "第 2-2 行（共 3 行）") || !strings.HasSuffix(out, "\n\nb") {
		t.Fatalf("按行读取结果不对: %q", out)
	}
}

// 线上出过没有任何成功调用就回「存好了」：有文件工具时，系统提示词必须写明存、发要以
// 本轮工具返回为准；没有文件工具时不必多这一句。
func TestSystemPromptRequiresToolEvidenceForSavedFiles(t *testing.T) {
	workDir := t.TempDir()
	registry, err := NewDefaultToolRegistry(Config{WorkDir: workDir, FileWriteEnabled: true}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(&scriptedClient{}, Config{WorkDir: workDir}, registry)
	if err != nil {
		t.Fatal(err)
	}
	if prompt := runner.systemPrompt(); !strings.Contains(prompt, "本轮必须有工具调用真的返回了它") || !strings.Contains(prompt, "save_to_workspace") {
		t.Fatalf("系统提示词缺少文件存发的证据规则:\n%s", prompt)
	}
	bare, err := NewRunner(&scriptedClient{}, Config{WorkDir: workDir}, NewToolRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(bare.systemPrompt(), "本轮必须有工具调用真的返回了它") {
		t.Fatal("没有文件工具时不该注入这条规则")
	}
}
