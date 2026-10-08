// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"errors"
	"net/http"
	"time"

	"github.com/SuInk/diana/model/assistant"

	"github.com/gin-gonic/gin"
)

// 小窝页面的接口：读余额和最近账目，主人发零花钱或调账。
//
// 控制台只能记 allowance 和 adjust 两种。spend 和 refund 只来自她自己的购买
// 提议，人从这里直接记一笔「花费」等于替她做了决定。
func (h *BotHandler) registerOwnSpaceRoutes(router gin.IRouter, base string) {
	router.GET(base+"/own-space", h.getOwnSpace)
	router.POST(base+"/own-space/wallet", h.recordOwnSpaceWallet)
}

type ownSpaceResponse struct {
	// Enabled 和自述一样单独给出：账本空着和小窝没开是两件事。
	Enabled bool                    `json:"enabled"`
	Wallet  assistant.WalletSummary `json:"wallet"`
}

type ownSpaceWalletPayload struct {
	Kind        assistant.WalletEntryKind `json:"kind"`
	AmountCents int64                     `json:"amount_cents"`
	Reason      string                    `json:"reason"`
}

func (h *BotHandler) getOwnSpace(c *gin.Context) {
	profileID := h.selfNoteProfileID(c)
	wallet, err := h.sqlite.WalletSummary(c.Request.Context(), profileID, 20)
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "own_space_get", err, "", nil)
		return
	}
	c.JSON(http.StatusOK, ownSpaceResponse{
		Enabled: assistant.OwnSpaceEnabled(h.runtime.ProfileConfig(profileID)),
		Wallet:  wallet,
	})
}

func (h *BotHandler) recordOwnSpaceWallet(c *gin.Context) {
	var payload ownSpaceWalletPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "own_space_wallet", err, "", nil)
		return
	}
	if payload.Kind != assistant.WalletEntryAllowance && payload.Kind != assistant.WalletEntryAdjust {
		c.JSON(http.StatusBadRequest, gin.H{"error": "控制台只能发零花钱或调账"})
		return
	}
	if payload.Kind == assistant.WalletEntryAllowance && payload.AmountCents < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "零花钱必须是正数，扣钱请用调账"})
		return
	}
	profileID := h.selfNoteProfileID(c)
	entry, err := h.sqlite.RecordWalletEntry(c.Request.Context(), assistant.WalletWriteRequest{
		ProfileID:   profileID,
		Kind:        payload.Kind,
		AmountCents: payload.AmountCents,
		Reason:      payload.Reason,
		ActorName:   "控制台",
		Now:         time.Now(),
	})
	if errors.Is(err, assistant.ErrWalletInsufficient) || errors.Is(err, assistant.ErrWalletAmount) {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "own_space_wallet", err, "", nil)
		return
	}
	recordRequestOperation(c, h.logs, "own_space_wallet", "小窝零花钱已记账", entry.ID, map[string]any{
		"kind": entry.Kind, "amount_cents": entry.AmountCents, "balance_after_cents": entry.BalanceAfter,
	})
	h.getOwnSpace(c)
}
