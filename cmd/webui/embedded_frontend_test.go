package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// withEmbeddedFrontend 模拟发布包（带内嵌前端）或源码构建（没有）。
func withEmbeddedFrontend(t *testing.T, present bool) {
	t.Helper()
	previous := loadEmbeddedFrontend
	t.Cleanup(func() { loadEmbeddedFrontend = previous })
	loadEmbeddedFrontend = func() (fs.FS, bool) {
		if !present {
			return nil, false
		}
		return fstest.MapFS{"index.html": {Data: []byte("embedded")}}, true
	}
}

func writeFrontendIndex(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("disk"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSelectFrontendExplicitDirBeatsEmbedded(t *testing.T) {
	withEmbeddedFrontend(t, true)
	configDir := t.TempDir()
	writeFrontendIndex(t, filepath.Join(configDir, "web"))
	// 工作目录换到别处：相对路径要按配置文件所在目录解析，不能按工作目录。
	t.Chdir(t.TempDir())

	choice := selectFrontend(filepath.Join(configDir, "config.yaml"), "web")
	if choice.source != filepath.Join(configDir, "web") || !choice.ready || choice.note != "" {
		t.Fatalf("choice = %+v, want configured dir next to config.yaml", choice)
	}
}

func TestSelectFrontendFallsBackToEmbeddedWhenConfiguredDirIsEmpty(t *testing.T) {
	withEmbeddedFrontend(t, true)
	configDir := t.TempDir()

	choice := selectFrontend(filepath.Join(configDir, "config.yaml"), "missing")
	if choice.source != "embedded" || !choice.ready || !strings.Contains(choice.note, "no index.html") {
		t.Fatalf("choice = %+v, want embedded with a warning", choice)
	}
}

func TestSelectFrontendReportsMissingIndexWithoutEmbedded(t *testing.T) {
	withEmbeddedFrontend(t, false)
	configDir := t.TempDir()

	choice := selectFrontend(filepath.Join(configDir, "config.yaml"), "missing")
	if choice.source != filepath.Join(configDir, "missing") || choice.ready || choice.note == "" {
		t.Fatalf("choice = %+v, want configured dir reported as not ready", choice)
	}
}

// 旧版安装器把发布包里的 frontend-next/dist 写进了配置；升级到内嵌前端后那份目录
// 会被删掉或停在旧版本，不能盖住新程序自带的前端。
func TestSelectFrontendIgnoresLegacyInstallerSetting(t *testing.T) {
	withEmbeddedFrontend(t, true)
	installDir := t.TempDir()
	writeFrontendIndex(t, filepath.Join(installDir, "frontend-next", "dist"))

	choice := selectFrontend(filepath.Join(installDir, "config.yaml"), filepath.Join(installDir, "frontend-next", "dist"))
	if choice.source != "embedded" || !choice.ready || !strings.Contains(choice.note, "older installer") {
		t.Fatalf("choice = %+v, want embedded frontend", choice)
	}
}

func TestSelectFrontendFindsSourceTreeWithoutSetting(t *testing.T) {
	withEmbeddedFrontend(t, false)
	root := t.TempDir()
	writeFrontendIndex(t, filepath.Join(root, "frontend", "dist"))
	t.Chdir(root)

	choice := selectFrontend("", "")
	if choice.source != filepath.Join("frontend", "dist") || !choice.ready || choice.note != "" {
		t.Fatalf("choice = %+v, want source-tree frontend/dist", choice)
	}
}

func TestDoctorFailsWhenFrontendIsMissing(t *testing.T) {
	withEmbeddedFrontend(t, false)
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("server:\n  port: \"1\"\n  frontend_dist: web\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	_ = runDoctorCommand([]string{"--config", configPath}, &output)
	if !strings.Contains(output.String(), "[fail] server.frontend_dist has no index.html: "+filepath.Join(configDir, "web")) {
		t.Fatalf("doctor output = %q", output.String())
	}
}
