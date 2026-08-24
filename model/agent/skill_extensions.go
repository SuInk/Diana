// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/netguard"
)

const (
	maxSkillDownloadBytes = 8 << 20
	maxSkillFiles         = 256
	maxSkillFileBytes     = 2 << 20
)

var (
	managedSkillNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	extensionPathLocks      sync.Map
)

type SkillsInstallTool struct {
	manager *ExtensionManager
}

func (t *SkillsInstallTool) Name() string { return "skills.install" }

func (t *SkillsInstallTool) Description() string {
	return `安装一个受 Diana 管理的 Skill 并立即刷新能力目录。首次调用会被拒绝并返回确认码，请把要装的内容讲清楚、等用户原样回复确认码后再重发本次调用。`
}

func (t *SkillsInstallTool) InputSchema() map[string]any {
	return toolObjectSchema(nil, map[string]any{
		"content":    toolStringParam("完整 SKILL.md 内容，与 source_url 二选一"),
		"source_url": toolStringParam("公开 HTTP(S) SKILL.md 或 zip 包地址，与 content 二选一"),
		"name":       toolStringParam("期望的 skill 名，可选"),
		"subdir":     toolStringParam("zip 包内的 skill 子目录，可选"),
		"replace":    toolBoolParam("同名 skill 已存在时是否覆盖"),
	})
}

func (t *SkillsInstallTool) ExplicitUserRequestKind() string { return "skill" }

func (t *SkillsInstallTool) Run(ctx context.Context, input map[string]any) (string, error) {
	state, err := t.manager.installSkill(ctx, skillInstallRequest{
		Name:      stringFromInput(input, "name"),
		Content:   rawStringFromInput(input, "content"),
		SourceURL: stringFromInput(input, "source_url"),
		Subdir:    stringFromInput(input, "subdir"),
		Replace:   boolFromInput(input, "replace", false),
	})
	if err != nil {
		return "", err
	}
	body, err := json.MarshalIndent(map[string]any{
		"installed": state,
		"message":   "Skill 已安装并在当前 Agent 会话中生效。使用前请调用 skills.read 读取完整说明。",
	}, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}

type SkillsUninstallTool struct {
	manager *ExtensionManager
}

func (t *SkillsUninstallTool) Name() string { return "skills.uninstall" }

func (t *SkillsUninstallTool) Description() string {
	return `卸载一个由 Diana 管理的 Skill；外部只读 Skill 不可卸载。首次调用会被拒绝并返回确认码，等用户原样回复后再重发本次调用。`
}

func (t *SkillsUninstallTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"name"}, map[string]any{
		"name": toolStringParam("要卸载的 skill 名称"),
	})
}

func (t *SkillsUninstallTool) ExplicitUserRequestKind() string { return "skill" }

func (t *SkillsUninstallTool) Run(_ context.Context, input map[string]any) (string, error) {
	name := stringFromInput(input, "name")
	if name == "" {
		return "", errors.New("name is required")
	}
	recoveryPath, err := t.manager.uninstallSkill(name)
	if err != nil {
		return "", err
	}
	body, err := json.MarshalIndent(map[string]any{
		"uninstalled":   name,
		"recovery_path": recoveryPath,
		"message":       "Skill 已从能力目录卸载；文件已移入本地回收目录。",
	}, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}

type skillInstallRequest struct {
	Name      string
	Content   string
	SourceURL string
	Subdir    string
	Replace   bool
}

func (m *ExtensionManager) installSkill(ctx context.Context, req skillInstallRequest) (SkillMetadata, error) {
	req.Name = strings.TrimSpace(req.Name)
	req.SourceURL = strings.TrimSpace(req.SourceURL)
	req.Subdir = strings.TrimSpace(req.Subdir)
	hasContent := strings.TrimSpace(req.Content) != ""
	hasURL := req.SourceURL != ""
	if hasContent == hasURL {
		return SkillMetadata{}, errors.New("provide exactly one of content or source_url")
	}
	if req.Name != "" && !managedSkillNamePattern.MatchString(req.Name) {
		return SkillMetadata{}, errors.New("name must use only letters, numbers, dot, underscore, or hyphen")
	}

	var (
		packageBody []byte
		source      = "inline"
	)
	if hasURL {
		body, err := downloadSkillPackage(ctx, req.SourceURL)
		if err != nil {
			return SkillMetadata{}, err
		}
		packageBody = body
		source = redactedSourceURL(req.SourceURL)
	} else {
		packageBody = []byte(req.Content)
		if len(packageBody) > maxSkillFileBytes {
			return SkillMetadata{}, fmt.Errorf("SKILL.md exceeds %d bytes", maxSkillFileBytes)
		}
	}

	root := filepath.Clean(m.cfg.ManagedSkillRoot)
	lock := extensionPathLock(root)
	lock.Lock()
	defer lock.Unlock()
	if err := os.MkdirAll(root, 0o700); err != nil {
		return SkillMetadata{}, err
	}
	staging, err := os.MkdirTemp(root, ".install-*")
	if err != nil {
		return SkillMetadata{}, err
	}
	defer os.RemoveAll(staging)
	payload := filepath.Join(staging, "payload")
	if err := os.MkdirAll(payload, 0o700); err != nil {
		return SkillMetadata{}, err
	}

	if isZipPackage(packageBody) {
		unpacked := filepath.Join(staging, "unpacked")
		if err := extractSkillArchive(packageBody, unpacked); err != nil {
			return SkillMetadata{}, err
		}
		sourceDir, err := selectSkillDirectory(unpacked, req.Subdir, req.Name)
		if err != nil {
			return SkillMetadata{}, err
		}
		if err := copySkillTree(sourceDir, payload); err != nil {
			return SkillMetadata{}, err
		}
	} else {
		if req.Subdir != "" {
			return SkillMetadata{}, errors.New("subdir is only valid for zip skill packages")
		}
		if err := os.WriteFile(filepath.Join(payload, skillFileName), packageBody, 0o600); err != nil {
			return SkillMetadata{}, err
		}
	}

	skill, err := parseSkill(filepath.Join(payload, skillFileName))
	if err != nil {
		return SkillMetadata{}, fmt.Errorf("invalid SKILL.md: %w", err)
	}
	if !managedSkillNamePattern.MatchString(skill.Name) {
		return SkillMetadata{}, errors.New("skill frontmatter name must use only letters, numbers, dot, underscore, or hyphen")
	}
	if req.Name != "" && req.Name != skill.Name {
		return SkillMetadata{}, fmt.Errorf("skill name %q does not match requested name %q", skill.Name, req.Name)
	}
	for _, builtin := range normalizeBuiltinSkills(m.cfg.BuiltinSkills) {
		if builtin.Name == skill.Name {
			return SkillMetadata{}, fmt.Errorf("skill %q is built into Diana and cannot be replaced", skill.Name)
		}
	}
	for _, reserved := range m.cfg.ReservedSkillNames {
		if reserved == skill.Name {
			return SkillMetadata{}, fmt.Errorf("skill %q is reserved by Diana and cannot be replaced", skill.Name)
		}
	}
	metadataBody, err := json.MarshalIndent(skillInstallMetadata{
		Source:      source,
		InstalledAt: time.Now().UTC().Format(time.RFC3339),
		Managed:     true,
	}, "", "  ")
	if err != nil {
		return SkillMetadata{}, err
	}
	if err := os.WriteFile(filepath.Join(payload, skillInstallMetadataName), append(metadataBody, '\n'), 0o600); err != nil {
		return SkillMetadata{}, err
	}

	target := filepath.Join(root, skill.Name)
	if info, statErr := os.Stat(target); statErr == nil && info.IsDir() {
		if !req.Replace {
			return SkillMetadata{}, fmt.Errorf("skill %q already exists; set replace=true only when the user requested replacement", skill.Name)
		}
		if !managedSkillDirectory(target) {
			return SkillMetadata{}, fmt.Errorf("skill %q is not Diana-managed and cannot be replaced", skill.Name)
		}
		backup, err := moveSkillToTrash(root, target)
		if err != nil {
			return SkillMetadata{}, err
		}
		if err := os.Rename(payload, target); err != nil {
			_ = os.Rename(backup, target)
			return SkillMetadata{}, err
		}
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return SkillMetadata{}, statErr
	} else if err := os.Rename(payload, target); err != nil {
		return SkillMetadata{}, err
	}

	if err := m.reloadSkills(); err != nil {
		m.addWarning("skills: " + err.Error())
	}
	for _, installed := range m.Skills() {
		if installed.Name == skill.Name && installed.Managed {
			return installed, nil
		}
	}
	return SkillMetadata{}, fmt.Errorf("skill %q was written but could not be reloaded", skill.Name)
}

func (m *ExtensionManager) uninstallSkill(name string) (string, error) {
	name = strings.TrimSpace(name)
	var selected SkillMetadata
	for _, skill := range m.Skills() {
		if skill.Name == name {
			selected = skill
			break
		}
	}
	if selected.Name == "" {
		return "", fmt.Errorf("skill %q not found", name)
	}
	if !selected.Managed {
		return "", fmt.Errorf("skill %q is external/read-only and cannot be uninstalled", name)
	}
	root := filepath.Clean(m.cfg.ManagedSkillRoot)
	directory := filepath.Clean(filepath.Dir(selected.Path))
	if !pathWithinRoot(root, directory) || directory == root || !managedSkillDirectory(directory) {
		return "", errors.New("managed skill path validation failed")
	}
	lock := extensionPathLock(root)
	lock.Lock()
	recoveryPath, err := moveSkillToTrash(root, directory)
	lock.Unlock()
	if err != nil {
		return "", err
	}
	if err := m.reloadSkills(); err != nil {
		m.addWarning("skills: " + err.Error())
	}
	return recoveryPath, nil
}

func downloadSkillPackage(ctx context.Context, sourceURL string) ([]byte, error) {
	if err := netguard.ValidatePublicURL(ctx, sourceURL); err != nil {
		return nil, fmt.Errorf("skill source URL rejected: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Diana-Agent-Skill-Installer/1.0")
	resp, err := netguard.NewPublicHTTPClient(30 * time.Second).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8<<10))
		return nil, fmt.Errorf("skill source returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSkillDownloadBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxSkillDownloadBytes {
		return nil, fmt.Errorf("skill package exceeds %d bytes", maxSkillDownloadBytes)
	}
	return body, nil
}

func isZipPackage(body []byte) bool {
	return len(body) >= 4 && bytes.Equal(body[:4], []byte{'P', 'K', 3, 4})
}

func extractSkillArchive(body []byte, destination string) error {
	reader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return fmt.Errorf("invalid skill zip: %w", err)
	}
	if len(reader.File) > maxSkillFiles {
		return fmt.Errorf("skill package contains more than %d files", maxSkillFiles)
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	total := int64(0)
	for _, item := range reader.File {
		name := filepath.Clean(filepath.FromSlash(item.Name))
		if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe skill archive path %q", item.Name)
		}
		if item.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("skill archive symlink %q is not allowed", item.Name)
		}
		target := filepath.Join(destination, name)
		if !pathWithinRoot(destination, target) {
			return fmt.Errorf("unsafe skill archive path %q", item.Name)
		}
		if item.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			continue
		}
		if item.UncompressedSize64 > maxSkillFileBytes {
			return fmt.Errorf("skill file %q exceeds %d bytes", item.Name, maxSkillFileBytes)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		source, err := item.Open()
		if err != nil {
			return err
		}
		mode := os.FileMode(0o600)
		if item.Mode().Perm()&0o111 != 0 {
			mode = 0o700
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			_ = source.Close()
			return err
		}
		written, copyErr := io.Copy(output, io.LimitReader(source, maxSkillFileBytes+1))
		closeErr := output.Close()
		_ = source.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if written > maxSkillFileBytes {
			return fmt.Errorf("skill file %q exceeds %d bytes", item.Name, maxSkillFileBytes)
		}
		total += written
		if total > maxSkillDownloadBytes {
			return fmt.Errorf("expanded skill package exceeds %d bytes", maxSkillDownloadBytes)
		}
	}
	return nil
}

func selectSkillDirectory(root, subdir, expectedName string) (string, error) {
	if subdir != "" {
		cleaned := filepath.Clean(filepath.FromSlash(subdir))
		if filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
			return "", errors.New("subdir must remain inside the skill package")
		}
		selected := filepath.Join(root, cleaned)
		if !pathWithinRoot(root, selected) || !fileExists(filepath.Join(selected, skillFileName)) {
			return "", fmt.Errorf("subdir %q does not contain SKILL.md", subdir)
		}
		return selected, nil
	}
	var candidates []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && entry.Name() == skillFileName {
			candidates = append(candidates, filepath.Dir(path))
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if expectedName != "" {
		for _, candidate := range candidates {
			skill, parseErr := parseSkill(filepath.Join(candidate, skillFileName))
			if parseErr == nil && skill.Name == expectedName {
				return candidate, nil
			}
		}
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	if len(candidates) == 0 {
		return "", errors.New("skill package does not contain SKILL.md")
	}
	return "", errors.New("skill package contains multiple skills; provide name or subdir")
}

func copySkillTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("skill symlink %q is not allowed", relative)
		}
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if len(body) > maxSkillFileBytes {
			return fmt.Errorf("skill file %q exceeds %d bytes", relative, maxSkillFileBytes)
		}
		mode := os.FileMode(0o600)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().Perm()&0o111 != 0 {
			mode = 0o700
		}
		return os.WriteFile(target, body, mode)
	})
}

func moveSkillToTrash(root, target string) (string, error) {
	trash := filepath.Join(root, ".trash")
	if err := os.MkdirAll(trash, 0o700); err != nil {
		return "", err
	}
	name := filepath.Base(target) + "-" + time.Now().UTC().Format("20060102T150405.000000000")
	recoveryPath := filepath.Join(trash, name)
	if err := os.Rename(target, recoveryPath); err != nil {
		return "", err
	}
	return recoveryPath, nil
}

func managedSkillDirectory(directory string) bool {
	body, err := os.ReadFile(filepath.Join(directory, skillInstallMetadataName))
	if err != nil {
		return false
	}
	var metadata skillInstallMetadata
	return json.Unmarshal(body, &metadata) == nil && metadata.Managed
}

func pathWithinRoot(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func redactedSourceURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "remote"
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.User = nil
	return parsed.String()
}

func extensionPathLock(path string) *sync.Mutex {
	key := filepath.Clean(path)
	lock, _ := extensionPathLocks.LoadOrStore(key, &sync.Mutex{})
	return lock.(*sync.Mutex)
}
