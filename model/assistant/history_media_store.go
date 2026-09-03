// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
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

// writeHistoryMedia 是所有平台共用的历史媒体落盘入口。目录只承载可运维的
// 分类信息，数据库仍保存绝对路径，因此旧版目录无需迁移也能继续读取。
func writeHistoryMedia(req historyMediaWriteRequest) (string, error) {
	if len(req.Body) == 0 {
		return "", fmt.Errorf("history media body is empty")
	}
	baseDir, err := historyMediaDir()
	if err != nil {
		return "", err
	}
	platform := safeHistoryPart(NormalizePlatformID(req.Platform))
	category := safeHistoryPart(firstNonEmpty(req.Category, "file"))
	date := time.Now().Format("2006-01-02")
	if req.EventTime > 0 {
		date = time.Unix(req.EventTime, 0).Local().Format("2006-01-02")
	}
	session := historyMediaSession(req.TargetKind, req.GroupID, req.UserID)
	messageID := safeHistoryPart(firstNonEmpty(req.MessageID, "no-message"))
	dir := filepath.Join(baseDir, platform, date, category, session, messageID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(session + ":" + messageID + ":" + req.Source))
	name := hex.EncodeToString(hash[:])[:16]
	if fileName := safeHistoryMediaFileName(req.FileName); fileName != "" {
		name += "-" + fileName
	}
	if filepath.Ext(name) == "" {
		name += imageExtension(firstNonEmpty(req.ContentType, http.DetectContentType(req.Body)), req.Body)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, req.Body, 0o600); err != nil {
		return "", err
	}
	return path, nil
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
