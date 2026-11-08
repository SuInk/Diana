// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/assistant"

	"github.com/gin-gonic/gin"
)

// 本地群：聊天页里主人建的群，成员是几台机器人，只存在 Diana 里。运行时那一侧见
// model/assistant/local_room.go。这里多一件运行时做不了的事：「一句话建群」要新建
// 机器人配置档，而配置档归 WebUI 的档案存储管。
const (
	localRoomStreamTimeout = 10 * time.Minute
	localRoomPlanMaxText   = 500
)

type localRoomRuntime interface {
	LocalRooms(context.Context) []assistant.LocalRoom
	CreateLocalRoom(context.Context, assistant.LocalRoom) (assistant.LocalRoom, error)
	UpdateLocalRoom(context.Context, assistant.LocalRoom) (assistant.LocalRoom, error)
	DeleteLocalRoom(context.Context, string) error
	LocalRoomHistory(context.Context, string) (assistant.LocalRoom, []assistant.LocalRoomMessage, assistant.LocalRoomStatus, error)
	LocalRoomSend(context.Context, string, string, func(assistant.LocalRoomMessage)) (assistant.LocalRoomMessage, error)
	StopLocalRoom(context.Context, string) (assistant.LocalRoomStatus, error)
	PlanLocalRoom(context.Context, string, string) (assistant.LocalRoomPlan, error)
}

func (h *BotHandler) registerLocalRoomRoutes(router gin.IRouter) {
	group := router.Group("/api/assistant/rooms", func(c *gin.Context) {
		if !c.GetBool("webui_admin") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "需要 WebUI 管理员登录", "auth_required": true})
			return
		}
		c.Next()
	})
	group.GET("", h.listLocalRooms)
	group.POST("", h.createLocalRoom)
	group.POST("/plan", h.planLocalRoom)
	group.GET("/:id", h.localRoomHistory)
	group.PUT("/:id", h.updateLocalRoom)
	group.DELETE("/:id", h.deleteLocalRoom)
	group.POST("/:id/messages", h.localRoomSend)
	group.POST("/:id/stop", h.stopLocalRoom)
}

func (h *BotHandler) localRoomRuntime(c *gin.Context) (localRoomRuntime, bool) {
	runtime, ok := h.runtime.(localRoomRuntime)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "运行时不支持本地群"})
	}
	return runtime, ok
}

func (h *BotHandler) listLocalRooms(c *gin.Context) {
	runtime, ok := h.localRoomRuntime(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{"rooms": runtime.LocalRooms(c.Request.Context())})
}

func (h *BotHandler) createLocalRoom(c *gin.Context) {
	runtime, ok := h.localRoomRuntime(c)
	if !ok {
		return
	}
	var room assistant.LocalRoom
	if err := c.ShouldBindJSON(&room); err != nil {
		writeError(c, http.StatusBadRequest, errors.New("请求格式不对"))
		return
	}
	created, err := runtime.CreateLocalRoom(c.Request.Context(), room)
	if err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	recordRequestOperation(c, h.logs, "local_room_create", "本地群已创建", created.ID, map[string]any{"name": created.Name, "members": created.Members})
	c.JSON(http.StatusOK, gin.H{"room": created})
}

func (h *BotHandler) updateLocalRoom(c *gin.Context) {
	runtime, ok := h.localRoomRuntime(c)
	if !ok {
		return
	}
	var room assistant.LocalRoom
	if err := c.ShouldBindJSON(&room); err != nil {
		writeError(c, http.StatusBadRequest, errors.New("请求格式不对"))
		return
	}
	room.ID = c.Param("id")
	updated, err := runtime.UpdateLocalRoom(c.Request.Context(), room)
	if err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"room": updated})
}

func (h *BotHandler) deleteLocalRoom(c *gin.Context) {
	runtime, ok := h.localRoomRuntime(c)
	if !ok {
		return
	}
	if err := runtime.DeleteLocalRoom(c.Request.Context(), c.Param("id")); err != nil {
		writeError(c, http.StatusNotFound, err)
		return
	}
	recordRequestOperation(c, h.logs, "local_room_delete", "本地群已解散", c.Param("id"), nil)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *BotHandler) localRoomHistory(c *gin.Context) {
	runtime, ok := h.localRoomRuntime(c)
	if !ok {
		return
	}
	room, messages, status, err := runtime.LocalRoomHistory(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeError(c, http.StatusNotFound, err)
		return
	}
	if messages == nil {
		messages = []assistant.LocalRoomMessage{}
	}
	c.JSON(http.StatusOK, gin.H{"room": room, "messages": messages, "status": status})
}

func (h *BotHandler) stopLocalRoom(c *gin.Context) {
	runtime, ok := h.localRoomRuntime(c)
	if !ok {
		return
	}
	status, err := runtime.StopLocalRoom(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeError(c, http.StatusNotFound, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": status})
}

// localRoomSend 主人在群里说一句，按行流式返回这一轮讨论里的消息：message、
// heartbeat、done。讨论停下（没有成员在想）就结束；页面关了讨论照样继续。
func (h *BotHandler) localRoomSend(c *gin.Context) {
	runtime, ok := h.localRoomRuntime(c)
	if !ok {
		return
	}
	var payload struct {
		Text string `json:"text"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		writeError(c, http.StatusBadRequest, errors.New("请求格式不对"))
		return
	}
	payload.Text = strings.TrimSpace(payload.Text)
	if payload.Text == "" || len([]rune(payload.Text)) > webChatMaxText {
		writeError(c, http.StatusBadRequest, errors.New("消息不能为空，也不能超过 4000 字"))
		return
	}
	if _, _, _, err := runtime.LocalRoomHistory(c.Request.Context(), c.Param("id")); err != nil {
		writeError(c, http.StatusNotFound, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), localRoomStreamTimeout)
	defer cancel()

	c.Header("Content-Type", "application/x-ndjson; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	var streamMu sync.Mutex
	emit := func(value any) {
		streamMu.Lock()
		defer streamMu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if err := json.NewEncoder(c.Writer).Encode(value); err != nil {
			cancel()
			return
		}
		c.Writer.Flush()
	}
	heartbeatStop, heartbeatDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer recoverGoroutinePanic("local_room_heartbeat")
		defer close(heartbeatDone)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatStop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				emit(gin.H{"type": "heartbeat"})
			}
		}
	}()
	defer func() { close(heartbeatStop); <-heartbeatDone }()

	count := 0
	owner, err := runtime.LocalRoomSend(ctx, c.Param("id"), payload.Text, func(msg assistant.LocalRoomMessage) {
		if msg.SenderID == "owner" {
			return
		}
		count++
		emit(gin.H{"type": "message", "message": msg})
	})
	if err != nil {
		emit(gin.H{"type": "done", "error": err.Error()})
		return
	}
	_, _, status, _ := runtime.LocalRoomHistory(context.Background(), c.Param("id"))
	emit(gin.H{"type": "done", "owner": owner, "messages": count, "status": status})
}

// planLocalRoom 是「一句话建群」：模型拆出群名和几台带人设的机器人，以 template
// 那台机器人为模板（借用它的模型分配）建出内置机器人，再建群把它们拉进来。
func (h *BotHandler) planLocalRoom(c *gin.Context) {
	runtime, ok := h.localRoomRuntime(c)
	if !ok {
		return
	}
	var payload struct {
		Text     string `json:"text"`
		Template string `json:"template"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		writeError(c, http.StatusBadRequest, errors.New("请求格式不对"))
		return
	}
	payload.Text = strings.TrimSpace(payload.Text)
	if payload.Text == "" || len([]rune(payload.Text)) > localRoomPlanMaxText {
		writeError(c, http.StatusBadRequest, fmt.Errorf("说说要建什么样的群，不超过 %d 字", localRoomPlanMaxText))
		return
	}
	set := h.profiles.Profiles().WithDefaults()
	template, ok := localRoomTemplate(set, payload.Template)
	if !ok {
		writeError(c, http.StatusBadRequest, errors.New("先建一台机器人配好模型，新机器人照它的模型分配来"))
		return
	}
	plan, err := runtime.PlanLocalRoom(c.Request.Context(), template.ID, payload.Text)
	if err != nil {
		writeError(c, http.StatusBadGateway, err)
		return
	}
	next := set
	var members []string
	for _, bot := range plan.Bots {
		cfg := localBotFromTemplate(template, bot)
		var id string
		next, id = upsertBotProfileSet(next, assistant.ConfigPayload{Name: cfg.Name}, cfg)
		members = append(members, id)
	}
	if err := h.applyProfileSet(next); err != nil && !errors.Is(err, assistant.ErrBotDisabled) {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	if err := h.profiles.SaveProfiles(next); err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	room, err := runtime.CreateLocalRoom(c.Request.Context(), assistant.LocalRoom{Name: plan.RoomName, Members: members})
	if err != nil {
		writeError(c, http.StatusInternalServerError, fmt.Errorf("机器人已经建好了，但建群失败：%w", err))
		return
	}
	recordRequestOperation(c, h.logs, "local_room_plan", "一句话建群：已创建内置机器人和本地群", room.ID, map[string]any{"name": room.Name, "members": members, "template": template.ID})
	c.JSON(http.StatusOK, gin.H{"room": room, "plan": plan})
}

// localRoomTemplate 挑借模型分配的那台机器人：指定了就用它，否则第一台启用着的。
func localRoomTemplate(set assistant.ProfileSet, id string) (assistant.BotConfig, bool) {
	id = strings.TrimSpace(id)
	for _, profile := range set.Profiles {
		if id != "" && profile.ID == id {
			return profile, true
		}
	}
	for _, profile := range set.Profiles {
		if id == "" && profile.Enabled {
			return profile, true
		}
	}
	return assistant.BotConfig{}, false
}

// localBotFromTemplate 造一台内置机器人：名字和人设来自建群方案，模型分配和模型
// 出错后的恢复策略照模板，其余（连接、主人、群设置）一律用新建机器人的默认值。
func localBotFromTemplate(template assistant.BotConfig, bot assistant.LocalRoomPlanBot) assistant.BotConfig {
	cfg := assistant.DefaultBotConfig()
	cfg.Name = bot.Name
	cfg.Platform = assistant.PlatformLocal
	cfg.SystemPrompt = bot.Persona
	cfg.Enabled = true
	if len(template.ModelRoles) > 0 {
		cfg.ModelRoles = make(map[string]assistant.ModelRole, len(template.ModelRoles))
		for key, role := range template.ModelRoles {
			cfg.ModelRoles[key] = role
		}
	}
	cfg.ModelRecoveryPolicy = template.ModelRecoveryPolicy
	return cfg
}
