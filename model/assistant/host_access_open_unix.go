//go:build !windows

package assistant

import (
	"os"
	"syscall"
)

// O_NONBLOCK prevents a raced replacement with a FIFO from parking a tool turn.
func openHostRead(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
