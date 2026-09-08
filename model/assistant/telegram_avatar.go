package assistant

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

type MemberAvatarChannel interface {
	MemberAvatar(context.Context, string) (GroupAvatar, error)
}

func (c *TelegramChannel) MemberAvatar(ctx context.Context, userID string) (GroupAvatar, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(userID), 10, 64)
	if err != nil || id <= 0 {
		return GroupAvatar{}, fmt.Errorf("telegram: valid user_id is required")
	}
	raw, err := c.callRaw(ctx, "getUserProfilePhotos", map[string]any{"user_id": id, "limit": 1})
	if err != nil {
		return GroupAvatar{}, err
	}
	var photos struct {
		Photos [][]telegramPhoto `json:"photos"`
	}
	if err := json.Unmarshal(raw, &photos); err != nil {
		return GroupAvatar{}, err
	}
	if len(photos.Photos) == 0 || len(photos.Photos[0]) == 0 {
		return GroupAvatar{}, fmt.Errorf("telegram: no accessible profile photo")
	}
	photo := photos.Photos[0][0]
	for _, candidate := range photos.Photos[0][1:] {
		if candidate.Width*candidate.Height > photo.Width*photo.Height || candidate.Width*candidate.Height == photo.Width*photo.Height && candidate.FileSize >= photo.FileSize {
			photo = candidate
		}
	}
	body, _, err := c.downloadFileByID(ctx, photo.FileID, telegramGroupAvatarMaxBytes)
	if err != nil {
		return GroupAvatar{}, err
	}
	mime := http.DetectContentType(body)
	if !strings.HasPrefix(mime, "image/") {
		return GroupAvatar{}, fmt.Errorf("telegram: profile photo is not an image")
	}
	return GroupAvatar{Data: body, ContentType: mime}, nil
}

func (r *Runtime) avatarSourceURL(ctx context.Context, event MessageEvent, id string, group bool) string {
	if r.currentPlatform(event) == PlatformOneBotV11 {
		if group {
			return OneBotGroupAvatarURL(id)
		}
		return OneBotMemberAvatarURL(id)
	}
	var avatar GroupAvatar
	var err error
	if group {
		provider, ok := eventChannelFor[GroupAvatarChannel](r, event)
		if !ok {
			return ""
		}
		avatar, err = provider.GroupAvatar(ctx, id)
	} else {
		provider, ok := eventChannelFor[MemberAvatarChannel](r, event)
		if !ok {
			return ""
		}
		avatar, err = provider.MemberAvatar(ctx, id)
	}
	if err != nil || len(avatar.Data) == 0 || !strings.HasPrefix(avatar.ContentType, "image/") {
		return ""
	}
	// Credential-bearing Telegram URLs never leave the channel. Image providers
	// receive image bytes, not a URL containing the bot token.
	return "data:" + avatar.ContentType + ";base64," + base64.StdEncoding.EncodeToString(avatar.Data)
}
