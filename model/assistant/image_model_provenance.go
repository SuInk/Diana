package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/SuInk/diana/model/llm"
)

type GeneratedImageModel struct {
	ModelID         string `json:"model_id"`
	ReportedModelID string `json:"reported_model_id,omitempty"`
	Provider        string `json:"provider,omitempty"`
	Operation       string `json:"operation"`
}

type ImageModelRecord struct {
	MessageID string                `json:"message_id"`
	Models    []GeneratedImageModel `json:"images"`
	CreatedAt int64                 `json:"created_at"`
}

type ImageModelRecordStore interface {
	SaveImageModelRecord(context.Context, string, ImageModelRecord) error
	LoadImageModelRecord(context.Context, string, string) (ImageModelRecord, bool, error)
}

func generatedImageModels(cfg llm.ProviderConfig, operation string, count int, responses ...*llm.ImageGenerateResponse) []GeneratedImageModel {
	models := make([]GeneratedImageModel, count)
	for i := range models {
		models[i] = GeneratedImageModel{ModelID: cfg.ImageModelWithDefault(), Provider: string(cfg.Provider), Operation: operation}
		if len(responses) > 0 && responses[0] != nil {
			models[i].ReportedModelID = strings.TrimSpace(responses[0].Model)
		}
	}
	return models
}

func imageModelScope(event MessageEvent) string {
	// Do not share provenance when cross-platform context sharing is enabled.
	event.ContextNamespace = ""
	key, _ := json.Marshal([]string{NormalizePlatformID(event.Platform), strings.TrimSpace(event.ProfileID), sessionKey(event)})
	return string(key)
}

func (r *Runtime) imageModelStore() ImageModelRecordStore {
	r.mu.RLock()
	defer r.mu.RUnlock()
	store, _ := r.messageStore.(ImageModelRecordStore)
	return store
}

func (r *Runtime) rememberImageModels(event MessageEvent, msg OutgoingMessage, messageID string) {
	if messageID == "" || len(msg.GeneratedImageModels) == 0 || len(msg.GeneratedImageModels) != len(msg.ImageURLs) {
		return
	}
	store := r.imageModelStore()
	if store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	record := ImageModelRecord{MessageID: messageID, Models: append([]GeneratedImageModel(nil), msg.GeneratedImageModels...), CreatedAt: time.Now().UnixNano()}
	if err := store.SaveImageModelRecord(ctx, imageModelScope(event), record); err != nil {
		log.Printf("persist image model provenance: %v", err)
	}
}

func (t *dianaRuntimeModelTool) imageModelHistory(ctx context.Context, messageID string) (string, error) {
	store := t.provider.runtime.imageModelStore()
	if store == nil {
		return "", fmt.Errorf("图片模型执行记录存储未配置")
	}
	messageID = strings.TrimSpace(messageID)
	if messageID == "" && t.event.Quoted != nil {
		messageID = strings.TrimSpace(t.event.Quoted.MessageID)
	}
	if messageID == "" {
		messageID = strings.TrimSpace(t.event.SemanticSourceMessageID)
	}
	record, found, err := store.LoadImageModelRecord(ctx, imageModelScope(t.event), messageID)
	if err != nil {
		return "", fmt.Errorf("读取图片模型执行记录失败")
	}
	body, err := json.Marshal(struct {
		Found    bool              `json:"found"`
		Source   string            `json:"source"`
		Record   *ImageModelRecord `json:"record,omitempty"`
		Guidance string            `json:"reply_guidance"`
	}{found, "image_execution_record", func() *ImageModelRecord {
		if found {
			return &record
		}
		return nil
	}(), "images 按发送时图片顺序记录实际提交的模型 ID，包含后备切换结果。未找到时明确无法确认，旧图片不会自动补出记录，不能拿当前配置代替。未指定或引用消息时只表示本会话最近一次有记录的图片发送。"})
	return string(body), err
}
