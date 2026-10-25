package assistant

import (
	"fmt"
	"os"
)

func openHostRead(path string) (*os.File, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("只支持普通文件或目录")
	}
	return os.Open(path)
}
