// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/SuInk/diana/scripts"
)

func TestHandleCLILeavesServerArgumentsAlone(t *testing.T) {
	handled, err := handleCLI([]string{"--config", "config.yaml"})
	if err != nil || handled {
		t.Fatalf("handleCLI() = (%v, %v), want (false, nil)", handled, err)
	}
}

func TestHandleCLIRejectsUnknownUninstallOptionBeforeLookingForScript(t *testing.T) {
	handled, err := handleCLI([]string{"uninstall", "--everything"})
	if !handled || err == nil {
		t.Fatalf("handleCLI() = (%v, %v), want handled error", handled, err)
	}
}

func TestWindowsUninstallArgs(t *testing.T) {
	got := windowsUninstallArgs([]string{"--purge", "-y"})
	want := []string{"-Purge", "-Yes"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("windowsUninstallArgs() = %#v, want %#v", got, want)
	}
}

func TestWindowsUninstallScriptRequiresInstallerMarker(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	if _, err := windowsUninstallScript(root); err == nil || !strings.Contains(err.Error(), "one-click installations") {
		t.Fatalf("windowsUninstallScript() without marker error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".installed-version"), []byte("v0.9.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	script, err := windowsUninstallScript(root)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(script)
	got, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(script) != ".ps1" || !bytes.Equal(got, scripts.UninstallPowerShell) || !bytes.Contains(got, []byte("$InstallDir")) {
		t.Fatalf("staged uninstall script %s does not match the embedded one", script)
	}
}

func TestCLIHelpListsCommands(t *testing.T) {
	var output strings.Builder
	printCLIHelp(&output)
	for _, command := range []string{"logs", "passwd", "uninstall", "version", "help"} {
		if !strings.Contains(output.String(), command) {
			t.Fatalf("help output does not contain %q: %s", command, output.String())
		}
	}
}
