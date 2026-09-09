package webui

import (
	"context"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func TestTelegramUsernameOwnerPrivateLogin(t *testing.T) {
	runtime := ownerLoginRuntime()
	runtime.cfg.Platform = assistant.PlatformTelegram
	runtime.cfg.OwnerID = "@Ruaneko"
	router, _, handler := newOwnerLoginTestRouter(t, runtime)
	code, _ := createOwnerPairing(t, router)
	event := assistant.MessageEvent{Kind: assistant.EventKindPrivate, Platform: assistant.PlatformTelegram, UserID: "1061423117", SenderName: "ruaneko", SenderUsername: "someone"}
	if handler.ConsumePrivateMessage(context.Background(), event, code) {
		t.Fatal("display-name impostor approved login")
	}
	event.SenderUsername = "ruaneko"
	if !handler.ConsumePrivateMessage(context.Background(), event, code) {
		t.Fatal("username owner could not approve login")
	}
	if len(runtime.calls) != 1 || runtime.calls[0].action != "sendMessage" || runtime.calls[0].params["chat_id"] != int64(1061423117) {
		t.Fatalf("receipt did not use verified numeric ID: %#v", runtime.calls)
	}
	if runtime.cfg.OwnerID != "@Ruaneko" {
		t.Fatal("login rewrote persisted owner")
	}
}
