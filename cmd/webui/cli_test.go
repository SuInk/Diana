// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"reflect"
	"strings"
	"testing"
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

func TestCLIHelpListsCommands(t *testing.T) {
	var output strings.Builder
	printCLIHelp(&output)
	for _, command := range []string{"logs", "uninstall", "version", "help"} {
		if !strings.Contains(output.String(), command) {
			t.Fatalf("help output does not contain %q: %s", command, output.String())
		}
	}
}
