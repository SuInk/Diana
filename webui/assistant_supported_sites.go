package webui

import (
	"github.com/SuInk/diana/model/assistant"
	"github.com/gin-gonic/gin"
)

func (h *BotHandler) listYTDLPSupportedSites(c *gin.Context) {
	entries, err := assistant.YTDLPSupportedExtractors(c.Request.Context())
	if err != nil {
		c.JSON(503, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"extractors": entries})
}
