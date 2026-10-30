//go:build !windows

package main

import (
	"os"
)

func runUninstallScript(script, root string, args []string) error {
	cmd := uninstallCommand(script, root, args)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
