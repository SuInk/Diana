package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunStatusCommand(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"status":"ok","version":"v1.2.3","uptime_seconds":65}`))
	}))
	defer server.Close()
	hostPort := strings.TrimPrefix(server.URL, "http://")
	parts := strings.Split(hostPort, ":")
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	config := fmt.Sprintf("server:\n  host: %s\n  port: %s\n", parts[0], parts[1])
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	if err := runStatusCommand([]string{"--config", configPath}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "v1.2.3") || !strings.Contains(output.String(), "1m5s") {
		t.Fatalf("status output = %q", output.String())
	}
}

func TestRunConfigCheckRejectsInvalidBotSection(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("bot:\n  - invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runConfigCommand([]string{"check", "--config", configPath}, &strings.Builder{}); err == nil {
		t.Fatal("config check accepted an invalid bot section")
	}
}

func TestLoadCLIConfigRejectsMissingExplicitFile(t *testing.T) {
	if _, _, err := loadCLIConfig([]string{"--config", filepath.Join(t.TempDir(), "missing.yaml")}); err == nil {
		t.Fatal("missing explicit config was accepted")
	}
}

// TestDoctorResolvesPathsLikeServerInDocker 容器里主程序按工作目录解析配置里的
// 相对路径；配置文件放在 data/config.yaml 时 doctor 也得检查同一个位置，而不是
// data/data。
func TestDoctorResolvesPathsLikeServerInDocker(t *testing.T) {
	t.Setenv("DIANA_DEPLOYMENT", "docker")
	root := t.TempDir()
	t.Chdir(root)
	for _, dir := range []string{"data", filepath.Join("data", "logs")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	body := "server:\n  port: \"1\"\nstorage:\n  db_path: data/diana.db\n  log_path: data/logs/diana.log\n"
	if err := os.WriteFile(filepath.Join("data", "config.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	// The frontend is absent in the fixture, so doctor reports a failure; only
	// the path checks matter here.
	_ = runDoctorCommand([]string{"--config", filepath.Join("data", "config.yaml")}, &output)
	want, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"[ok]   database directory writable: " + filepath.Join(want, "data"),
		"[ok]   log directory writable: " + filepath.Join(want, "data", "logs"),
	} {
		if !strings.Contains(output.String(), line) {
			t.Errorf("doctor output is missing %q:\n%s", line, output.String())
		}
	}
}
