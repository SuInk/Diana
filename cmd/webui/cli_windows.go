//go:build windows

package main

import (
	"fmt"
	"os"
)

func runUninstallScript(script, root string, args []string) error {
	cmd := uninstallCommand(script, root, args)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	fmt.Println("Diana uninstaller started.")
	return cmd.Process.Release()
}
