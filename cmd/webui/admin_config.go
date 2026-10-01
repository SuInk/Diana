// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// ensureDataDirConfig 在哪都没找到 config.yaml 时，在默认数据目录生成一份只有 admin
// 段的配置。Docker 部署只挂 data/ 一个目录、镜像不带配置文件，不生成的话用户在挂载
// 目录里根本看不到 config.yaml，忘了密码也无处可改。
//
// 首次启动刚生成的账号密码原样写进去，之后由它管凭据；已有管理员的旧部署只知道
// 账号，密码留空并注明忘记时怎么填。--config / DIANA_CONFIG 指向别处、或者数据库
// 不在默认数据目录时不生成：下次启动按查找顺序找不到这份文件，生成了也不生效。
// 返回空路径表示没有生成。
func ensureDataDirConfig(explicitPath bool, dbPath, username, generatedPassword string) (string, error) {
	if explicitPath || strings.TrimSpace(dbPath) == "" {
		return "", nil
	}
	target, err := filepath.Abs(dataDirConfigPath)
	if err != nil {
		return "", err
	}
	if filepath.Dir(filepath.Clean(dbPath)) != filepath.Dir(target) {
		return "", nil
	}
	var b strings.Builder
	b.WriteString("# Diana 配置，启动时自动生成。完整字段见仓库里的 config.example.yaml，改完重启生效。\n")
	b.WriteString("# admin 段每次启动都以这里为准；在 WebUI 里改账号密码会同步写回这两项。\n")
	b.WriteString("# 这份文件里有管理员密码，别给别人。\n")
	b.WriteString("admin:\n")
	b.WriteString("  username: " + yamlQuoted(username) + "\n")
	if generatedPassword == "" {
		b.WriteString("  # 忘记密码时在这里填新密码（至少 8 位）再重启，启动时以它为准。\n")
	}
	b.WriteString("  password: " + yamlQuoted(generatedPassword) + "\n")
	// O_EXCL：并发或别人刚放进来的文件一律不覆盖。
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", nil
		}
		return "", err
	}
	if _, err := file.WriteString(b.String()); err != nil {
		_ = file.Close()
		_ = os.Remove(target)
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(target)
		return "", err
	}
	return target, nil
}
