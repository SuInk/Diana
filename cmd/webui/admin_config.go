// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"go.yaml.in/yaml/v4"
)

// writeAdminCredentials 把 WebUI 改好的管理员账号密码写回 config.yaml。
// 只改 admin.username / admin.password 两行，其余内容（注释、顺序、缩进）原样保留；
// 原地写入而不是临时文件改名，单独挂载进容器的 config.yaml 改名会失败。
func writeAdminCredentials(path, username, password string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	updated, err := setAdminCredentialsYAML(data, username, password)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return os.WriteFile(path, updated, info.Mode().Perm())
}

// setAdminCredentialsYAML 按行改写 admin 段：已有的键替换整行，缺的键插在 admin
// 下面，没有 admin 段就追加到末尾。改完重新解析一遍，读出来不是目标值就报错，
// 不把改坏的文件写回去。
func setAdminCredentialsYAML(data []byte, username, password string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	text := string(data)
	var out string
	if len(doc.Content) == 0 {
		out = appendAdminSection(text, username, password)
	} else {
		root := doc.Content[0]
		if root.Kind != yaml.MappingNode {
			return nil, errors.New("顶层不是映射，无法写回 admin 段")
		}
		adminKey, adminValue := mappingEntry(root, "admin")
		if adminKey == nil {
			out = appendAdminSection(text, username, password)
		} else {
			edited, err := editAdminSection(text, adminKey, adminValue, username, password)
			if err != nil {
				return nil, err
			}
			out = edited
		}
	}
	var check struct {
		Admin adminConfig `yaml:"admin"`
	}
	if err := yaml.Unmarshal([]byte(out), &check); err != nil {
		return nil, fmt.Errorf("写回后无法解析: %w", err)
	}
	if check.Admin.Username != username || check.Admin.Password != password {
		return nil, errors.New("写回后读出的 admin 段与预期不一致")
	}
	return []byte(out), nil
}

func mappingEntry(node *yaml.Node, key string) (*yaml.Node, *yaml.Node) {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i], node.Content[i+1]
		}
	}
	return nil, nil
}

func appendAdminSection(text, username, password string) string {
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text + "admin:\n" +
		"  username: " + yamlQuoted(username) + "\n" +
		"  password: " + yamlQuoted(password) + "\n"
}

func editAdminSection(text string, adminKey, adminValue *yaml.Node, username, password string) (string, error) {
	lines := strings.Split(text, "\n")
	lineAt := func(n int) (string, bool) {
		if n < 1 || n > len(lines) {
			return "", false
		}
		return lines[n-1], true
	}
	// 行尾的 \r 跟着原文件走，Windows 上编辑过的配置不至于混出两种换行。
	setLine := func(n int, content string) {
		if strings.HasSuffix(lines[n-1], "\r") {
			content += "\r"
		}
		lines[n-1] = content
	}
	childIndent := strings.Repeat(" ", adminKey.Column-1+2)
	var missing []string
	switch {
	case adminValue.Kind == yaml.MappingNode && adminValue.Style&yaml.FlowStyle == 0:
		for _, field := range []struct{ key, value string }{{"username", username}, {"password", password}} {
			key, value := mappingEntry(adminValue, field.key)
			if key == nil {
				missing = append(missing, field.key+": "+yamlQuoted(field.value))
				continue
			}
			if value.Kind != yaml.ScalarNode || value.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 || value.Line != key.Line {
				return "", fmt.Errorf("admin.%s 不是单行写法，请手动修改", field.key)
			}
			if _, ok := lineAt(key.Line); !ok {
				return "", fmt.Errorf("admin.%s 行号越界", field.key)
			}
			line := strings.Repeat(" ", key.Column-1) + field.key + ": " + yamlQuoted(field.value)
			if comment := firstNonEmpty(value.LineComment, key.LineComment); comment != "" {
				line += " " + comment
			}
			setLine(key.Line, line)
		}
		if len(adminValue.Content) > 0 {
			childIndent = strings.Repeat(" ", adminValue.Content[0].Column-1)
		}
	case adminValue.Kind == yaml.ScalarNode && adminValue.Tag == "!!null":
		// 「admin:」后面什么都没写，或者写了 ~ / null：把这一行收成「admin:」再补两项。
		if _, ok := lineAt(adminKey.Line); !ok {
			return "", errors.New("admin 行号越界")
		}
		line := strings.Repeat(" ", adminKey.Column-1) + "admin:"
		if comment := firstNonEmpty(adminValue.LineComment, adminKey.LineComment); comment != "" {
			line += " " + comment
		}
		setLine(adminKey.Line, line)
		missing = []string{"username: " + yamlQuoted(username), "password: " + yamlQuoted(password)}
	default:
		return "", errors.New("admin 段不是块格式的映射，请手动修改")
	}
	if len(missing) > 0 {
		insert := make([]string, 0, len(missing))
		for _, entry := range missing {
			insert = append(insert, childIndent+entry)
		}
		// 补在 admin 段已有的最后一项后面；最后一项跨行（块标量）时退回到 admin 行下面。
		at := adminKey.Line
		if n := len(adminValue.Content); adminValue.Kind == yaml.MappingNode && n > 0 {
			last := adminValue.Content[n-1]
			if last.Kind == yaml.ScalarNode && last.Style&(yaml.LiteralStyle|yaml.FoldedStyle) == 0 && last.Line <= len(lines) {
				at = last.Line
			}
		}
		lines = append(lines[:at], append(insert, lines[at:]...)...)
	}
	return strings.Join(lines, "\n"), nil
}

// yamlQuoted 输出 YAML 双引号字符串。JSON 字符串是合法的 YAML 双引号标量，
// 密码里的冒号、井号、引号和反斜杠都不会被误解。
func yamlQuoted(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
