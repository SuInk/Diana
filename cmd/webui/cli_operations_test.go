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
