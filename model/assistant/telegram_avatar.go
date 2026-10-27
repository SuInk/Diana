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

// MemberAvatarForProfile 找到指定机器人对应的通道，取一个账号的头像。
// Telegram 的文件下载地址带 Bot Token，WebUI 只能通过运行时拿到图片字节，
// 不能把上游地址直接交给浏览器。
func (r *Runtime) MemberAvatarForProfile(ctx context.Context, profileID, userID string) (GroupAvatar, bool) {
	provider, ok := channelFor[MemberAvatarChannel](r, profileID)
	if !ok {
		return GroupAvatar{}, false
	}
	avatar, err := provider.MemberAvatar(ctx, userID)
	if err != nil || len(avatar.Data) == 0 || !strings.HasPrefix(avatar.ContentType, "image/") {
		return GroupAvatar{}, false
	}
	return avatar, true
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
	ctx = withDirectoryEvent(ctx, event)
	if r.currentPlatform(event) == PlatformOneBotV11 {
		if group {
			return OneBotGroupAvatarURL(id)
		}
		return OneBotMemberAvatarURL(id)
	}
	if !group && r.currentPlatform(event) == PlatformQQOfficial {
		return r.qqOfficialMemberAvatarURL(event, id)
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

// qqOfficialMemberAvatarURL 按事件所属机器人的 AppID 拼 QQ 官方机器人的成员头像。
func (r *Runtime) qqOfficialMemberAvatarURL(event MessageEvent, openID string) string {
	return QQOfficialMemberAvatarURL(r.profileConfig(r.eventProfileID(event)).QQAppID, openID)
}
