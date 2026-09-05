// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"path/filepath"
	"strings"
)

type historyMediaWriteRequest struct {
	Platform    string
	EventTime   int64
	TargetKind  string
	GroupID     string
	UserID      string
	MessageID   string
	Category    string
	Source      string
	FileName    string
	Body        []byte
	ContentType string
}

// History references share a content-addressed object across platforms and
// messages. Existing absolute paths remain readable without rewriting old rows.
func writeHistoryMedia(req historyMediaWriteRequest) (string, error) {
	if len(req.Body) == 0 {
		return "", fmt.Errorf("history media body is empty")
	}
	baseDir, err := historyMediaDir()
	if err != nil {
		return "", err
	}
	return persistMediaContent(baseDir, req.Body, req.ContentType, req.FileName)
}

func safeHistoryMediaFileName(value string) string {
	value = filepath.Base(strings.TrimSpace(value))
	if value == "" || value == "." {
		return ""
	}
	ext := strings.ToLower(filepath.Ext(value))
	stem := safeHistoryPart(strings.TrimSuffix(value, filepath.Ext(value)))
	if stem == "unknown" {
		stem = "media"
	}
	if len(ext) < 2 || len(ext) > 12 {
		return stem
	}
	for _, char := range ext[1:] {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') {
			return stem
		}
	}
	return stem + ext
}
