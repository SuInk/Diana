package webui

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/SuInk/diana/model/applog"
	"github.com/gin-gonic/gin"
)

func (h *BotHandler) llmUsage(c *gin.Context) {
	hours := 24
	if raw := c.Query("hours"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 2160 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "hours 必须在1到2160之间"})
			return
		}
		hours = value
	}
	reader, ok := h.logs.(applog.UsageReportReader)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "用量日志存储不可用"})
		return
	}
	until := time.Now()
	filter := applog.UsageFilter{ProfileID: botProfileScope(c), GroupID: strings.TrimSpace(c.Query("group_id")), Platform: strings.TrimSpace(c.Query("platform"))}
	report, err := reader.LLMUsageReport(c.Request.Context(), filter, until.Add(-time.Duration(hours)*time.Hour), until)
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "llm_usage", err, "", nil)
		return
	}
	c.JSON(http.StatusOK, report)
}
