// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type logsOptions struct {
	lines      int
	follow     bool
	configPath string
}

func runLogsCommand(args []string, output io.Writer) error {
	options, err := parseLogsOptions(args)
	if err != nil {
		return err
	}
	configPath := options.configPath
	if configPath == "" {
		configPath = resolveConfigPath(nil)
	}
	if configPath == "" {
		return fmt.Errorf("config.yaml was not found; pass --config to locate the Diana configuration")
	}
	config, err := loadAppConfig(configPath)
	if err != nil {
		return err
	}
	logPath := strings.TrimSpace(config.Storage.LogPath)
	if logPath == "" {
		return fmt.Errorf("storage.log_path is empty; this Diana instance only writes logs to standard output")
	}
	if !filepath.IsAbs(logPath) {
		logPath = filepath.Join(filepath.Dir(configPath), logPath)
	}
	return printLogTail(logPath, options.lines, options.follow, output)
}

func parseLogsOptions(args []string) (logsOptions, error) {
	options := logsOptions{lines: 100}
	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch {
		case argument == "-f" || argument == "--follow":
			options.follow = true
		case argument == "-n" || argument == "--lines":
			index++
			if index >= len(args) {
				return options, fmt.Errorf("%s requires a line count", argument)
			}
			lines, err := positiveLineCount(args[index])
			if err != nil {
				return options, err
			}
			options.lines = lines
		case strings.HasPrefix(argument, "--lines="):
			lines, err := positiveLineCount(strings.TrimPrefix(argument, "--lines="))
			if err != nil {
				return options, err
			}
			options.lines = lines
		case argument == "--config":
			index++
			if index >= len(args) || strings.TrimSpace(args[index]) == "" {
				return options, fmt.Errorf("--config requires a path")
			}
			options.configPath = args[index]
		case strings.HasPrefix(argument, "--config="):
			options.configPath = strings.TrimSpace(strings.TrimPrefix(argument, "--config="))
			if options.configPath == "" {
				return options, fmt.Errorf("--config requires a path")
			}
		default:
			return options, fmt.Errorf("unknown logs option: %s", argument)
		}
	}
	return options, nil
}

func positiveLineCount(value string) (int, error) {
	lines, err := strconv.Atoi(value)
	if err != nil || lines < 1 || lines > 100000 {
		return 0, fmt.Errorf("log line count must be between 1 and 100000")
	}
	return lines, nil
}

func printLogTail(path string, lines int, follow bool, output io.Writer) error {
	file, err := openFollowedLog(path)
	if err != nil {
		return fmt.Errorf("open Diana log %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	content, offset, err := readLastLines(file, lines)
	if err != nil {
		return fmt.Errorf("read Diana log %s: %w", path, err)
	}
	if len(content) > 0 {
		if _, err := output.Write(content); err != nil {
			return err
		}
	}
	if !follow {
		return nil
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	buffer := make([]byte, 32*1024)
	for {
		read, readErr := file.Read(buffer)
		if read > 0 {
			if _, err := output.Write(buffer[:read]); err != nil {
				return err
			}
		}
		if readErr == nil {
			continue
		}
		if readErr != io.EOF {
			return readErr
		}
		time.Sleep(400 * time.Millisecond)
		file, err = refreshFollowedLog(file, path)
		if err != nil {
			return err
		}
	}
}

// Follow the filename across rotation, and rewind after external truncation.
func refreshFollowedLog(file *os.File, path string) (*os.File, error) {
	current, err := file.Stat()
	if err != nil {
		return file, err
	}
	named, err := os.Stat(path)
	if os.IsNotExist(err) {
		return file, nil // The writer may be between rename and open.
	}
	if err != nil {
		return file, err
	}
	if !os.SameFile(current, named) {
		next, err := openFollowedLog(path)
		if os.IsNotExist(err) {
			return file, nil
		}
		if err != nil {
			return file, err
		}
		_ = file.Close()
		return next, nil
	}
	offset, err := file.Seek(0, io.SeekCurrent)
	if err == nil && named.Size() < offset {
		_, err = file.Seek(0, io.SeekStart)
	}
	return file, err
}

func readLastLines(file *os.File, lines int) ([]byte, int64, error) {
	size, err := file.Seek(0, io.SeekEnd)
	if err != nil || size == 0 {
		return nil, size, err
	}
	const blockSize int64 = 32 * 1024
	position := size
	data := make([]byte, 0, blockSize)
	for position > 0 && bytes.Count(data, []byte{'\n'}) <= lines {
		readSize := blockSize
		if position < readSize {
			readSize = position
		}
		position -= readSize
		block := make([]byte, readSize)
		if _, err := file.ReadAt(block, position); err != nil && err != io.EOF {
			return nil, 0, err
		}
		data = append(block, data...)
	}
	lineStart := 0
	lineCount := bytes.Count(data, []byte{'\n'})
	if len(data) > 0 && data[len(data)-1] != '\n' {
		lineCount++
	}
	for lineCount > lines {
		next := bytes.IndexByte(data[lineStart:], '\n')
		if next < 0 {
			break
		}
		lineStart += next + 1
		lineCount--
	}
	return data[lineStart:], size, nil
}
