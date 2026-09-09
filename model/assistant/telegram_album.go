package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func (c *TelegramChannel) sendPhotoAlbum(ctx context.Context, msg OutgoingMessage, chatID string) (map[string]any, error) {
	if len(msg.ImageURLs) < 2 || len(msg.ImageURLs) > 10 {
		return nil, fmt.Errorf("telegram: photo album requires 2 to 10 images")
	}
	if msg.Text != "" || len(msg.VideoURLs)+len(msg.AudioURLs)+len(msg.Segments) > 0 {
		return nil, fmt.Errorf("telegram: photo albums must not mix separate text or other media")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	media := make([]map[string]string, 0, len(msg.ImageURLs))
	var total int64
	for index, source := range msg.ImageURLs {
		if source == "" {
			return nil, fmt.Errorf("telegram: empty album image")
		}
		item := map[string]string{"type": "photo", "media": source}
		if path := telegramLocalPath(source); path != "" {
			file, err := os.Open(path)
			if err != nil {
				return nil, err
			}
			info, err := file.Stat()
			if err != nil {
				file.Close()
				return nil, err
			}
			if !info.Mode().IsRegular() || info.Size() > 10<<20 {
				file.Close()
				return nil, fmt.Errorf("telegram: album photo must be a regular file up to 10 MiB")
			}
			name := fmt.Sprintf("photo%d", index)
			part, err := writer.CreateFormFile(name, filepath.Base(path))
			if err != nil {
				file.Close()
				return nil, err
			}
			n, err := io.Copy(part, io.LimitReader(file, (10<<20)+1))
			file.Close()
			if err != nil {
				return nil, err
			}
			total += n
			if n > 10<<20 || total > 50<<20 {
				return nil, fmt.Errorf("telegram: album upload exceeds size limit")
			}
			item["media"] = "attach://" + name
		}
		media = append(media, item)
	}
	encoded, _ := json.Marshal(media)
	fields := map[string]string{"chat_id": chatID, "media": string(encoded)}
	if msg.MessageThreadID != "" {
		fields["message_thread_id"] = msg.MessageThreadID
	}
	if msg.ReplyMessageID != "" {
		fields["reply_to_message_id"] = msg.ReplyMessageID
		fields["allow_sending_without_reply"] = "true"
	}
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	endpoint, err := c.methodURL("sendMediaGroup")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	raw, err := c.do(req)
	if err != nil {
		return nil, err
	}
	var messages []map[string]any
	if err = json.Unmarshal(raw, &messages); err != nil {
		return nil, fmt.Errorf("telegram: invalid album response: %w", err)
	}
	if len(messages) != len(media) {
		return nil, fmt.Errorf("telegram: incomplete album response")
	}
	for _, message := range messages {
		if apiMessageID(message) == "" {
			return nil, fmt.Errorf("telegram: album response missing message ID")
		}
	}
	result := make(map[string]any)
	for key, value := range messages[0] {
		result[key] = value
	}
	result["album_messages"] = messages
	return result, nil
}

func (r *Runtime) rememberTelegramPhotoResults(ctx context.Context, event MessageEvent, msg OutgoingMessage, result map[string]any) bool {
	if msg.Platform != PlatformTelegram || msg.Text != "" || len(msg.ImageURLs) == 0 {
		return false
	}
	responses := []map[string]any{result}
	if msg.ImageAlbum {
		var ok bool
		responses, ok = result["album_messages"].([]map[string]any)
		if !ok || len(responses) != len(msg.ImageURLs) {
			return false
		}
	} else if len(msg.ImageURLs) != 1 {
		return false
	}
	for index, response := range responses {
		data := map[string]string{"file": msg.ImageURLs[index]}
		if source := msg.ImageURLs[index]; telegramLocalPath(source) == "" && !strings.HasPrefix(source, "https://") && !strings.HasPrefix(source, "http://") {
			delete(data, "file")
			data["file_id"] = source
		}
		if photos, ok := response["photo"].([]any); ok && len(photos) > 0 {
			if photo, ok := photos[len(photos)-1].(map[string]any); ok {
				if fileID, ok := photo["file_id"].(string); ok {
					data["file_id"] = fileID
				}
			}
		}
		part := msg
		part.ImageAlbum = false
		part.ImageURLs = nil
		part.Segments = []MessageSegment{{Type: "image", Data: data}}
		if data["file"] == "" {
			history := r.outgoingHistoryEvent(event, part)
			history.MessageID = apiMessageID(response)
			r.remember(history)
			continue
		}
		r.rememberOutgoingWithMessageID(ctx, event, part, apiMessageID(response))
	}
	return true
}
