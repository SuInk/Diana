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
		if err != nil || value < 1 || value > 8760 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "hours 必须在1到8760之间"})
			return
		}
		hours = value
	}
	var since, until time.Time
	hasCustomBounds := strings.TrimSpace(c.Query("since")) != "" || strings.TrimSpace(c.Query("until")) != ""
	if hasCustomBounds {
		var err error
		if rawSince := strings.TrimSpace(c.Query("since")); rawSince != "" {
			since, err = time.Parse(time.RFC3339Nano, rawSince)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "since 时间格式无效"})
				return
			}
		}
		if rawUntil := strings.TrimSpace(c.Query("until")); rawUntil != "" {
			until, err = time.Parse(time.RFC3339Nano, rawUntil)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "until 时间格式无效"})
				return
			}
		} else {
			until = time.Now()
		}
		if since.IsZero() {
			since = until.Add(-time.Duration(hours) * time.Hour)
		}
		if !since.Before(until) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "since 必须早于 until"})
			return
		}
	}
	reader, ok := h.logs.(applog.UsageReportReader)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "用量日志存储不可用"})
		return
	}
	if !hasCustomBounds {
		until = time.Now()
		since = until.Add(-time.Duration(hours) * time.Hour)
	}
	filter := applog.UsageFilter{ProfileID: botProfileScope(c), GroupID: strings.TrimSpace(c.Query("group_id")), Platform: strings.TrimSpace(c.Query("platform"))}
	report, err := reader.LLMUsageReport(c.Request.Context(), filter, since, until)
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "llm_usage", err, "", nil)
		return
	}
	c.JSON(http.StatusOK, report)
}
