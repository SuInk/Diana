// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"github.com/SuInk/diana/model/assistant"

	"github.com/gin-gonic/gin"
)

// SillyTavern 角色卡导入：一张卡进来，人设进人设库，内嵌 character_book 并进
// 世界书。文件以 base64 递交——PNG 卡是二进制，走 JSON 接口这是最直接的载法，
// 也让前端对 JSON 卡和 PNG 卡只写一条上传路径。

type characterCardImportPayload struct {
	CardBase64 string `json:"card_base64"`
}

type characterCardImportResponse struct {
	// Persona 是导入后的那套人设；重复导入被跳过时为空。
	Persona  *assistant.Persona  `json:"persona,omitempty"`
	Personas []assistant.Persona `json:"personas"`
	// Skipped/Renamed 沿用人设导入的口径：同名同内容跳过，同名不同内容改名。
	Skipped  int    `json:"skipped"`
	Renamed  int    `json:"renamed"`
	BookName string `json:"book_name,omitempty"`
	// BookImported/BookDropped 是内嵌世界书的去向；卡里没有世界书时都是 0。
	BookImported int                       `json:"book_imported"`
	BookDropped  int                       `json:"book_dropped"`
	Nodes        []assistant.WorldBookNode `json:"nodes,omitempty"`
}

func (h *BotHandler) registerCharacterCardRoutes(router gin.IRouter, base string) {
	router.POST(base+"/personas/import-card", h.importCharacterCard)
}

func (h *BotHandler) importCharacterCard(c *gin.Context) {
	var payload characterCardImportPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.personas.import_card", err, "", nil)
		return
	}
	raw, err := base64.StdEncoding.DecodeString(payload.CardBase64)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.personas.import_card", errCharacterCardBase64, "", nil)
		return
	}
	card, err := assistant.ParseSillyTavernCharacterCard(raw)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.personas.import_card", err, "", nil)
		return
	}

	response := characterCardImportResponse{BookName: card.BookName}
	now := time.Now()

	// 人设和世界书分别加各自的锁、各自落库：两把锁不同时握着，谁也不等谁。
	// 中途失败时可能出现「人设进了、世界书没进」，返回值里两边的数各报各的，
	// 用户看得出来哪半边要重试。
	personaLibraryMu.Lock()
	set, ok := h.loadPersonaSet(c)
	if !ok {
		personaLibraryMu.Unlock()
		return
	}
	updated, personaResult := set.Import([]assistant.Persona{card.Persona}, now)
	if err := h.sqlite.SaveBotPersonas(c.Request.Context(), updated); err != nil {
		personaLibraryMu.Unlock()
		h.writeError(c, http.StatusInternalServerError, "assistant.personas.import_card", err, card.Persona.Name, nil)
		return
	}
	personaLibraryMu.Unlock()
	response.Personas = updated.Personas
	if response.Personas == nil {
		response.Personas = []assistant.Persona{}
	}
	response.Skipped = personaResult.Skipped
	response.Renamed = personaResult.Renamed
	if len(personaResult.Imported) > 0 {
		imported := personaResult.Imported[0]
		response.Persona = &imported
	}

	if len(card.BookNodes) > 0 {
		worldBookMu.Lock()
		book, ok := h.loadWorldBook(c)
		if !ok {
			worldBookMu.Unlock()
			return
		}
		updatedBook, bookResult := book.Import(card.BookNodes, now)
		if err := h.sqlite.SaveWorldBook(c.Request.Context(), updatedBook); err != nil {
			worldBookMu.Unlock()
			h.writeError(c, http.StatusInternalServerError, "assistant.personas.import_card", err, card.Persona.Name, nil)
			return
		}
		worldBookMu.Unlock()
		response.BookImported = bookResult.Imported
		response.BookDropped = bookResult.Dropped
		response.Nodes = updatedBook.Nodes
	}

	recordRequestOperation(c, h.logs, "assistant.personas.import_card", "角色卡已导入", card.Persona.Name, map[string]any{
		"skipped":       response.Skipped,
		"renamed":       response.Renamed,
		"book_imported": response.BookImported,
		"book_dropped":  response.BookDropped,
	})
	c.JSON(http.StatusOK, response)
}

var errCharacterCardBase64 = errors.New("card_base64 不是有效的 base64")
