package webui

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
	"github.com/gin-gonic/gin"
)

const resolverCookieFileLimit = 1024 * 1024

var resolverCookieFileMu sync.Mutex

// Only the application's managed file is editable; request paths are never used.
func (h *BotHandler) resolverCookieFilePath() (string, error) {
	dbPath := strings.TrimSpace(os.Getenv("APP_DB_PATH"))
	if h.sqlite != nil {
		dbPath = h.sqlite.Path()
	}
	resolved, err := storage.ResolveDatabasePath(dbPath)
	if err != nil || resolved == "" {
		return "", errors.New("无法确定 Cookie 数据目录")
	}
	return filepath.Join(filepath.Dir(resolved), "resolver-cookies.txt"), nil
}

func validateResolverCookieContent(content string) error {
	first, _, _ := strings.Cut(content, "\n")
	first = strings.TrimSuffix(first, "\r")
	if first != "# Netscape HTTP Cookie File" && first != "# HTTP Cookie File" {
		return errors.New("请粘贴完整 Netscape Cookie 文件，首行应为 # Netscape HTTP Cookie File")
	}
	rows := 0
	for index, line := range strings.Split(content, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "#HttpOnly_") {
			continue
		}
		fields := strings.Split(strings.TrimPrefix(line, "#HttpOnly_"), "\t")
		invalid := len(fields) != 7
		if !invalid {
			_, err := strconv.ParseUint(fields[4], 10, 64)
			invalid = err != nil || fields[0] == "" || !strings.HasPrefix(fields[2], "/") || fields[5] == "" || (fields[1] != "TRUE" && fields[1] != "FALSE") || (fields[3] != "TRUE" && fields[3] != "FALSE")
		}
		if invalid {
			return fmt.Errorf("第 %d 行不是有效的 Netscape Cookie（需要 7 列，以制表符分隔）", index+1)
		}
		rows++
	}
	if rows == 0 {
		return errors.New("请粘贴包含 Cookie 条目的 Netscape 文件内容")
	}
	return nil
}

func (h *BotHandler) readResolverCookieFile(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	resolverCookieFileMu.Lock()
	defer resolverCookieFileMu.Unlock()
	path, err := h.resolverCookieFilePath()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		c.JSON(200, gin.H{"content": "", "configured": false})
		return
	}
	if err != nil || !info.Mode().IsRegular() {
		c.JSON(400, gin.H{"error": "无法读取托管 Cookie 文件"})
		return
	}
	file, err := os.Open(path)
	if err != nil {
		c.JSON(500, gin.H{"error": "无法读取托管 Cookie 文件"})
		return
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, resolverCookieFileLimit+1))
	if err != nil || len(body) > resolverCookieFileLimit {
		c.JSON(400, gin.H{"error": "Cookie 文件超过 1 MiB 或无法读取"})
		return
	}
	c.JSON(200, gin.H{"content": string(body), "configured": true})
}

func (h *BotHandler) saveResolverCookieFile(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2*resolverCookieFileLimit)
	var payload struct {
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(400, gin.H{"error": "Cookie 请求无效或过大"})
		return
	}
	content := strings.ReplaceAll(payload.Content, "\r\n", "\n")
	if len(content) > resolverCookieFileLimit {
		c.JSON(400, gin.H{"error": "Cookie 文件不能超过 1 MiB"})
		return
	}
	if err := validateResolverCookieContent(content); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	resolverCookieFileMu.Lock()
	defer resolverCookieFileMu.Unlock()
	path, err := h.resolverCookieFilePath()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	_, settings, ok := h.runtime.Plugins().PluginForConfiguration(assistant.ResolverPluginID, "")
	if !ok {
		c.JSON(400, gin.H{"error": "链接解析插件不可用"})
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		c.JSON(500, gin.H{"error": "无法创建 Cookie 数据目录"})
		return
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".resolver-cookies-*")
	if err != nil {
		c.JSON(500, gin.H{"error": "无法保存 Cookie 文件"})
		return
	}
	defer os.Remove(file.Name())
	if _, err = file.WriteString(content); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(file.Name(), path)
	}
	if err != nil {
		c.JSON(500, gin.H{"error": "无法保存 Cookie 文件"})
		return
	}
	values := make(map[string]any, len(settings)+1)
	for key, value := range settings {
		values[key] = value
	}
	values["ytdlp_cookies_path"] = path
	_, err = h.runtime.Plugins().UpdateSettingsForProfile(assistant.ResolverPluginID, "", values, nil)
	if err != nil {
		c.JSON(500, gin.H{"error": "Cookie 已保存，但启用失败，请重试"})
		return
	}
	h.persistState()
	recordRequestOperation(c, h.logs, "resolver_cookie_file", "已更新托管 Cookie 文件", assistant.ResolverPluginID, nil)
	c.JSON(200, gin.H{"path": path})
}
