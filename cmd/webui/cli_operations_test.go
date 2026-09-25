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
	// Compare by file identity: on macOS the temp dir sits behind the
	// /var -> /private/var symlink, so the printed path and root may differ
	// as strings while naming the same directory.
	for _, check := range []struct{ prefix, want string }{
		{"[ok]   database directory writable: ", filepath.Join(root, "data")},
		{"[ok]   log directory writable: ", filepath.Join(root, "data", "logs")},
	} {
		got, ok := doctorLinePath(output.String(), check.prefix)
		if !ok {
			t.Errorf("doctor output is missing %q:\n%s", check.prefix, output.String())
			continue
		}
		if !filepath.IsAbs(got) || !sameDirectory(t, got, check.want) {
			t.Errorf("doctor checked %q, want %q:\n%s", got, check.want, output.String())
		}
	}
}

func TestListenWebUIReportsRunningInstance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(`{"status":"ok","version":"v1.2.3"}`))
	}))
	defer server.Close()
	host, port, _ := strings.Cut(strings.TrimPrefix(server.URL, "http://"), ":")
	_, err := listenWebUI(appConfig{Server: serverConfig{Host: host, Port: port}})
	if err == nil || !strings.Contains(err.Error(), "Diana v1.2.3 is already running") {
		t.Fatalf("expected already-running error, got %v", err)
	}
}

func TestListenWebUIKeepsBindErrorForForeignListener(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	host, port, _ := strings.Cut(strings.TrimPrefix(server.URL, "http://"), ":")
	_, err := listenWebUI(appConfig{Server: serverConfig{Host: host, Port: port}})
	if err == nil || strings.Contains(err.Error(), "already running") {
		t.Fatalf("expected raw bind error, got %v", err)
	}
}

func TestInstanceLockRejectsSecondInstanceOnSameData(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "data", "diana.db")
	first, err := acquireInstanceLock(dbPath, "http://127.0.0.1:18080")
	if err != nil {
		t.Fatal(err)
	}
	_, err = acquireInstanceLock(dbPath, "http://127.0.0.1:18081")
	want := fmt.Sprintf("already running at http://127.0.0.1:18080 (pid %d)", os.Getpid())
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("expected %q, got %v", want, err)
	}
	first.Release()
	second, err := acquireInstanceLock(dbPath, "http://127.0.0.1:18081")
	if err != nil {
		t.Fatalf("lock should be free after release: %v", err)
	}
	second.Release()
}

func doctorLinePath(output, prefix string) (string, bool) {
	for _, line := range strings.Split(output, "\n") {
		if path, ok := strings.CutPrefix(line, prefix); ok {
			return path, true
		}
	}
	return "", false
}

func sameDirectory(t *testing.T, a, b string) bool {
	t.Helper()
	infoA, err := os.Stat(a)
	if err != nil {
		return false
	}
	infoB, err := os.Stat(b)
	if err != nil {
		t.Fatal(err)
	}
	return os.SameFile(infoA, infoB)
}
