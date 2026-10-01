// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/storage"
	"github.com/SuInk/diana/webui"
)

func writeResetPasswordFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	dbPath := filepath.Join(root, "data", "diana.db")
	store, err := storage.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	manager := webui.NewAuthManager(store)
	if _, err := manager.Bootstrap("owner", "old-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Login("owner", "old-password"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, []byte("storage:\n  db_path: data/diana.db\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath, dbPath
}

func TestResetPasswordCommandReplacesCredentials(t *testing.T) {
	configPath, dbPath := writeResetPasswordFixture(t)
	var output strings.Builder
	if err := runResetPasswordCommand([]string{"--config", configPath}, &output); err != nil {
		t.Fatalf("reset-password error = %v", err)
	}
	password := ""
	for _, line := range strings.Split(output.String(), "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "password: "); ok {
			password = value
		}
	}
	if !strings.Contains(output.String(), "username: owner") || password == "" {
		t.Fatalf("unexpected output: %s", output.String())
	}

	store, err := storage.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if sessions, _, err := store.LoadWebUISessions(context.Background()); err != nil || len(sessions.Sessions) != 0 {
		t.Fatalf("sessions after reset = %+v, %v", sessions, err)
	}
	manager := webui.NewAuthManager(store)
	if _, err := manager.Login("owner", "old-password"); err == nil {
		t.Fatal("old password still works")
	}
	if _, err := manager.Login("owner", password); err != nil {
		t.Fatalf("new password rejected: %v", err)
	}
}

func TestResetPasswordCommandRefusesWhileDianaRuns(t *testing.T) {
	configPath, dbPath := writeResetPasswordFixture(t)
	lock, err := acquireInstanceLock(dbPath, "http://127.0.0.1:18080")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	err = runResetPasswordCommand([]string{"--config", configPath}, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "stop it first") {
		t.Fatalf("expected running-instance refusal, got %v", err)
	}
}

func TestResetPasswordCommandDoesNotCreateMissingDatabase(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, []byte("storage:\n  db_path: data/diana.db\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runResetPasswordCommand([]string{"--config", configPath}, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "database was not found") {
		t.Fatalf("expected missing database error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "data", "diana.db")); !os.IsNotExist(statErr) {
		t.Fatalf("reset-password created a database: %v", statErr)
	}
}

func TestParseResetPasswordOptions(t *testing.T) {
	options, err := parseResetPasswordOptions([]string{"--username=admin", "--config", "x.yaml"})
	if err != nil || options.username != "admin" || options.configPath != "x.yaml" {
		t.Fatalf("options = %+v, %v", options, err)
	}
	for _, args := range [][]string{{"--username"}, {"--username="}, {"--bogus"}} {
		if _, err := parseResetPasswordOptions(args); err == nil {
			t.Fatalf("expected error for %v", args)
		}
	}
}
