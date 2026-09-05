// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
)

type rotatingLogWriter struct {
	mu      sync.Mutex
	path    string
	file    *os.File
	size    int64
	maxSize int64
	backups int
}

func newRotatingLogWriter(path string, maxSize int64, backups int) (*rotatingLogWriter, error) {
	if maxSize <= 0 || backups < 1 {
		return nil, fmt.Errorf("invalid log rotation limits")
	}
	w := &rotatingLogWriter{path: path, maxSize: maxSize, backups: backups}
	return w, w.open()
}

func (w *rotatingLogWriter) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	w.file, w.size = f, info.Size()
	return nil
}

func (w *rotatingLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return 0, os.ErrClosed
	}
	written := 0
	for len(p) > 0 {
		if w.size >= w.maxSize {
			if err := w.rotate(); err != nil {
				return written, err
			}
		}
		size := min(int64(len(p)), w.maxSize-w.size)
		n, err := w.file.Write(p[:size])
		written += n
		w.size += int64(n)
		if err != nil {
			return written, err
		}
		p = p[n:]
	}
	return written, nil
}

func (w *rotatingLogWriter) rotate() error {
	if err := w.file.Close(); err != nil {
		w.file = nil
		return errors.Join(err, w.open())
	}
	w.file = nil
	// Close before renaming so rotation also works on Windows. Reopen on
	// failure to avoid permanently disabling file logging after a transient error.
	err := func() error {
		if err := os.Remove(w.path + "." + strconv.Itoa(w.backups)); err != nil && !os.IsNotExist(err) {
			return err
		}
		for i := w.backups - 1; i >= 1; i-- {
			if err := os.Rename(w.path+"."+strconv.Itoa(i), w.path+"."+strconv.Itoa(i+1)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		return os.Rename(w.path, w.path+".1")
	}()
	return errors.Join(err, w.open())
}

func (w *rotatingLogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}
