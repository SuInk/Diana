package webui

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type trustedLoginRecord struct {
	AppID     string    `json:"app_id"`
	ProfileID string    `json:"profile_id"`
	Platform  string    `json:"platform"`
	UserID    string    `json:"user_id"`
	At        time.Time `json:"at"`
}
type trustedLoginPolicy struct {
	Enabled bool                 `json:"enabled"`
	Apps    []TrustedLoginApp    `json:"apps"`
	Records []trustedLoginRecord `json:"records"`
}

// NewManagedTrustedLoginHandler imports legacy configuration only on first use.
// The managed file stores hashes, never raw credentials; UI updates apply live.
func NewManagedTrustedLoginHandler(owner *OwnerLoginHandler, path string, legacy []TrustedLoginApp) (*TrustedLoginHandler, error) {
	p := trustedLoginPolicy{Enabled: len(legacy) > 0, Apps: legacy}
	data, err := os.ReadFile(path)
	if err == nil {
		if err = json.Unmarshal(data, &p); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	h, err := NewTrustedLoginHandler(owner, p.Apps)
	if err != nil {
		return nil, err
	}
	h.enabled = p.Enabled
	h.policyPath = path
	h.records = p.Records
	if len(h.records) > 100 {
		h.records = h.records[len(h.records)-100:]
	}
	if err = h.persistPolicy(h.policy()); err != nil {
		return nil, err
	}
	return h, nil
}

// Caller holds h.mu. Persist first, then publish the new state in memory.
func (h *TrustedLoginHandler) policy() trustedLoginPolicy {
	p := trustedLoginPolicy{Enabled: h.enabled, Apps: []TrustedLoginApp{}, Records: append([]trustedLoginRecord{}, h.records...)}
	for _, a := range h.apps {
		p.Apps = append(p.Apps, a)
	}
	sort.Slice(p.Apps, func(i, j int) bool { return p.Apps[i].ID < p.Apps[j].ID })
	return p
}
func (h *TrustedLoginHandler) persistPolicy(p trustedLoginPolicy) error {
	if h.policyPath == "" {
		return nil
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(h.policyPath), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(h.policyPath), ".trusted-login-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(name, h.policyPath)
}
func (h *TrustedLoginHandler) applyPolicy(p trustedLoginPolicy, changedApp string) error {
	if err := h.persistPolicy(p); err != nil {
		return err
	}
	h.enabled = p.Enabled
	h.apps = map[string]TrustedLoginApp{}
	for _, a := range p.Apps {
		h.apps[a.ID] = a
	}
	h.records = p.Records
	for k, v := range h.pairs {
		if changedApp == "" || v.appID == changedApp {
			delete(h.pairs, k)
			delete(h.lastPoll, k)
		}
	}
	return nil
}
func (h *TrustedLoginHandler) activeLocked(a TrustedLoginApp) bool {
	current, ok := h.apps[a.ID]
	return h.enabled && ok && !current.Disabled && current == a
}
func (h *TrustedLoginHandler) registerSettings(r gin.IRouter) {
	r.GET("/api/auth/apps/settings", h.adminGuard, h.settings)
	r.PUT("/api/auth/apps/settings", h.adminGuard, h.saveEnabled)
	r.POST("/api/auth/apps/settings/apps", h.adminGuard, h.saveApp)
	r.PATCH("/api/auth/apps/settings/apps/:id", h.adminGuard, h.saveApp)
	r.POST("/api/auth/apps/settings/apps/:id/rotate", h.adminGuard, h.rotateApp)
}
func (h *TrustedLoginHandler) adminGuard(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	cookie, _ := c.Cookie(authCookieName)
	if h.owner == nil || h.owner.auth == nil || !h.owner.auth.Authenticate(cookie) {
		c.AbortWithStatusJSON(401, gin.H{"error": "需要登录"})
		return
	}
	if c.Request.Method != http.MethodGet {
		origin := c.GetHeader("Origin")
		u, err := url.Parse(origin)
		if (origin != "" && (err != nil || u.Host != c.Request.Host)) || c.GetHeader("Sec-Fetch-Site") == "cross-site" || !strings.HasPrefix(c.GetHeader("Content-Type"), "application/json") {
			c.AbortWithStatusJSON(403, gin.H{"error": "请求来源或格式无效"})
			return
		}
	}
}
func trustedDecode(c *gin.Context, v any) error {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	d := json.NewDecoder(c.Request.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("unexpected trailing JSON")
	}
	return nil
}
func (h *TrustedLoginHandler) settings(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	apps := []gin.H{}
	for _, a := range h.policy().Apps {
		var last *time.Time
		for i := len(h.records) - 1; i >= 0; i-- {
			if h.records[i].AppID == a.ID {
				v := h.records[i].At
				last = &v
				break
			}
		}
		apps = append(apps, gin.H{"id": a.ID, "name": a.Name, "profile_id": a.ProfileID, "disabled": a.Disabled, "last_login_at": last})
	}
	profiles := []gin.H{}
	if h.owner.runtime != nil {
		for _, p := range h.owner.runtime.ProfileConfigs() {
			reason := ""
			if err := ownerLoginEligible(p); err != nil {
				reason = err.Error()
			}
			profiles = append(profiles, gin.H{"id": p.ID, "name": p.Name, "platform": p.Platform, "available": reason == "", "reason": reason})
		}
	}
	c.JSON(200, gin.H{"enabled": h.enabled, "apps": apps, "profiles": profiles, "records": h.records})
}
func (h *TrustedLoginHandler) saveEnabled(c *gin.Context) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if trustedDecode(c, &body) != nil || body.Enabled == nil {
		c.JSON(400, gin.H{"error": "请指定是否启用应用登录"})
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.policy()
	p.Enabled = *body.Enabled
	if err := h.applyPolicy(p, ""); err != nil {
		c.JSON(500, gin.H{"error": "保存失败，请重试"})
		return
	}
	recordRequestOperation(c, h.owner.logs, "auth_app_settings", "已更新应用登录总开关", "", nil)
	c.JSON(200, gin.H{"ok": true})
}
func (h *TrustedLoginHandler) saveApp(c *gin.Context) {
	var b struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		ProfileID string `json:"profile_id"`
		Disabled  bool   `json:"disabled"`
	}
	if trustedDecode(c, &b) != nil {
		c.JSON(400, gin.H{"error": "应用信息格式不正确"})
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.policy()
	id := c.Param("id")
	fresh := id == ""
	var a TrustedLoginApp
	secret := ""
	if fresh {
		id = b.ID
		if _, ok := h.apps[id]; ok {
			c.JSON(409, gin.H{"error": "应用标识已存在"})
			return
		}
		if len(h.apps) >= 32 {
			c.JSON(400, gin.H{"error": "最多添加 32 个应用"})
			return
		}
		var err error
		secret, err = randomOwnerPairingToken()
		if err != nil {
			c.Status(500)
			return
		}
		a = TrustedLoginApp{ID: id, SecretSHA256: hashToken(secret)}
	} else {
		var ok bool
		a, ok = h.apps[id]
		if !ok {
			c.Status(404)
			return
		}
		if b.ID != "" && b.ID != id {
			c.JSON(400, gin.H{"error": "应用标识不可修改"})
			return
		}
	}
	a.Name = strings.TrimSpace(b.Name)
	a.ProfileID = b.ProfileID
	a.Disabled = b.Disabled
	if validateTrustedApps([]TrustedLoginApp{a}) != nil {
		c.JSON(400, gin.H{"error": "请填写有效的应用名称、标识和机器人"})
		return
	}
	validProfile := false
	if h.owner.runtime != nil {
		for _, cfg := range h.owner.runtime.ProfileConfigs() {
			if cfg.ID == a.ProfileID && (a.Disabled || ownerLoginEligible(cfg) == nil) {
				validProfile = true
				break
			}
		}
	}
	if !a.Disabled && !validProfile {
		c.JSON(400, gin.H{"error": "请选择已启用管理员快速登录的机器人"})
		return
	}
	if fresh {
		p.Apps = append(p.Apps, a)
	} else {
		for i := range p.Apps {
			if p.Apps[i].ID == id {
				p.Apps[i] = a
			}
		}
	}
	if err := h.applyPolicy(p, id); err != nil {
		c.JSON(500, gin.H{"error": "保存失败，请重试"})
		return
	}
	recordRequestOperation(c, h.owner.logs, "auth_app_settings", "已保存受信应用："+id, "", nil)
	c.JSON(200, gin.H{"ok": true, "id": id, "secret": secret})
}
func (h *TrustedLoginHandler) rotateApp(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	id := c.Param("id")
	p := h.policy()
	if _, ok := h.apps[id]; !ok {
		c.Status(404)
		return
	}
	secret, err := randomOwnerPairingToken()
	if err != nil {
		c.Status(500)
		return
	}
	for i := range p.Apps {
		if p.Apps[i].ID == id {
			p.Apps[i].SecretSHA256 = hashToken(secret)
		}
	}
	if err = h.applyPolicy(p, id); err != nil {
		c.JSON(500, gin.H{"error": "保存失败，请重试"})
		return
	}
	recordRequestOperation(c, h.owner.logs, "auth_app_rotate", "已重置受信应用密钥："+id, "", nil)
	c.JSON(200, gin.H{"id": id, "secret": secret})
}
