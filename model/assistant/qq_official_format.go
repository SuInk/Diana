// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/SuInk/diana/model/agent"
)

// QQ 官方机器人的消息结构平台一直在加东西，很多还不进文档，每发现一个就改代码
// 发版太慢，所以让模型自己写格式：收尾时填 qq_format，可以现写一段 JSON 合并进请求体，也可以存成命名
// 模板，下次直接报名字复用。
//
// 模型只能碰 markdown 和 keyboard 两块；msg_id、msg_seq、引用这些决定消息发给谁、
// 算哪次回复的字段一律由 Diana 填。合并后的消息被平台拒收就去掉格式重发，宁可丢
// 排版也不丢回复；同一个模板连续被拒几次就停用，免得每条回复都白挨一次拒。
const (
	qqFormatFinalizeFieldName = "qq_format"

	qqFormatMaxTemplates       = 20
	qqFormatNameMaxRunes       = 20
	qqFormatPatchMaxBytes      = 2048
	qqFormatDisableAfterFails  = 3
	qqFormatStoreTimeout       = 3 * time.Second
	qqFormatPromptTemplateList = 10
)

var errQQFormatTemplateFull = errors.New("格式模板已满")

// qqFormatAllowedTopKeys 是模型能写进请求体顶层的键。
var qqFormatAllowedTopKeys = map[string]bool{"markdown": true, "keyboard": true}

// QQFormatTemplate 是模型存下来的一份命名格式。
type QQFormatTemplate struct {
	Name      string         `json:"name"`
	Patch     map[string]any `json:"patch"`
	Failures  int            `json:"failures,omitempty"`
	Disabled  bool           `json:"disabled,omitempty"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// QQFormat 是挂在一条出站消息上的格式。Name 为空表示这是现写的一次性格式。
// Report 由发送端回报平台收没收，用来给模板记账；可以为空。
type QQFormat struct {
	Name   string
	Patch  map[string]any
	Report func(ok bool, err error)
}

// QQFormatTemplateStore 按机器人档案持久化格式模板，存的是整张表的 JSON。
type QQFormatTemplateStore interface {
	LoadQQFormatTemplates(ctx context.Context, profileID string) ([]byte, error)
	SaveQQFormatTemplates(ctx context.Context, profileID string, data []byte) error
}

// SetQQFormatTemplateStore 注入格式模板存储。没有它时现写格式照常可用，只是存不下。
func (r *Runtime) SetQQFormatTemplateStore(store QQFormatTemplateStore) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.qqFormatStore = store
}

func (r *Runtime) qqFormatTemplateStore() QQFormatTemplateStore {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.qqFormatStore
}

// qqFormatTemplates 串行化同一进程里对模板表的读改写，失败计数和保存都走它。
var qqFormatTemplatesMu sync.Mutex

func (r *Runtime) loadQQFormatTemplates(ctx context.Context, profileID string) ([]QQFormatTemplate, error) {
	store := r.qqFormatTemplateStore()
	if store == nil {
		return nil, nil
	}
	loadCtx, cancel := context.WithTimeout(ctx, qqFormatStoreTimeout)
	defer cancel()
	raw, err := store.LoadQQFormatTemplates(loadCtx, strings.TrimSpace(profileID))
	if err != nil || len(raw) == 0 {
		return nil, err
	}
	var templates []QQFormatTemplate
	if err := json.Unmarshal(raw, &templates); err != nil {
		return nil, fmt.Errorf("decode qq format templates: %w", err)
	}
	return templates, nil
}

func (r *Runtime) saveQQFormatTemplates(ctx context.Context, profileID string, templates []QQFormatTemplate) error {
	store := r.qqFormatTemplateStore()
	if store == nil {
		return fmt.Errorf("格式模板存储不可用")
	}
	data, err := json.Marshal(templates)
	if err != nil {
		return err
	}
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), qqFormatStoreTimeout)
	defer cancel()
	return store.SaveQQFormatTemplates(saveCtx, strings.TrimSpace(profileID), data)
}

// upsertQQFormatTemplate 新建或覆盖一个模板。覆盖会清掉失败计数和停用标记：模型
// 改写了格式，旧账不该算到新写法头上。
func (r *Runtime) upsertQQFormatTemplate(ctx context.Context, profileID, name string, patch map[string]any, now time.Time) error {
	qqFormatTemplatesMu.Lock()
	defer qqFormatTemplatesMu.Unlock()
	templates, err := r.loadQQFormatTemplates(ctx, profileID)
	if err != nil {
		return err
	}
	entry := QQFormatTemplate{Name: name, Patch: patch, UpdatedAt: now.UTC()}
	replaced := false
	for index := range templates {
		if templates[index].Name == name {
			templates[index] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		if len(templates) >= qqFormatMaxTemplates {
			return errQQFormatTemplateFull
		}
		templates = append(templates, entry)
	}
	return r.saveQQFormatTemplates(ctx, profileID, templates)
}

// recordQQFormatResult 给模板记一次发送结果：成功清零，连续失败到阈值就停用。
func (r *Runtime) recordQQFormatResult(ctx context.Context, profileID, name string, ok bool, sendErr error) {
	if strings.TrimSpace(name) == "" {
		return
	}
	qqFormatTemplatesMu.Lock()
	defer qqFormatTemplatesMu.Unlock()
	templates, err := r.loadQQFormatTemplates(ctx, profileID)
	if err != nil {
		return
	}
	for index := range templates {
		entry := &templates[index]
		if entry.Name != name {
			continue
		}
		if ok {
			if entry.Failures == 0 {
				return
			}
			entry.Failures = 0
		} else {
			entry.Failures++
			if entry.Failures >= qqFormatDisableAfterFails && !entry.Disabled {
				entry.Disabled = true
				log.Printf("qq: 格式模板「%s」连续 %d 次被平台拒收，已停用: %v", name, entry.Failures, sendErr)
			}
		}
		if err := r.saveQQFormatTemplates(ctx, profileID, templates); err != nil {
			log.Printf("qq: 保存格式模板失败: %v", err)
		}
		return
	}
}

// qqFormatFinalizeField 生成收尾字段，把能用的模板名列进说明里，模型才知道有哪些。
func qqFormatFinalizeField(templates []QQFormatTemplate) agent.FinalizeField {
	// 小号按钮的 font_size 官方文档没写，Yunzai 的 QQBot-Plugin 等社区插件都这么发。
	description := "QQ 官方机器人消息的自定义格式，平时不填。想要按钮、Markdown 用上平台支持但默认没开的样式时填：" +
		"写已存模板的名字直接复用；写一段 JSON 只用这一次；写「名字={JSON}」存成模板并立即使用，下次报名字即可。" +
		"JSON 会深度合并进发送请求体，只能有 markdown 和 keyboard 两个顶层键，数组整段替换，例如小号按钮 " +
		`{"keyboard":{"content":{"style":{"font_size":"small"}}}}` + "。" +
		"按钮还是照 buttons 字段写，颜色也在那里选；平台拒收会自动退回默认格式，连续被拒的模板会停用。"
	names := make([]string, 0, len(templates))
	for _, entry := range templates {
		if !entry.Disabled {
			names = append(names, entry.Name)
		}
	}
	sort.Strings(names)
	if len(names) > qqFormatPromptTemplateList {
		names = names[:qqFormatPromptTemplateList]
	}
	if len(names) > 0 {
		description += "已存模板：" + strings.Join(names, "、") + "。"
	}
	return agent.FinalizeField{Name: qqFormatFinalizeFieldName, Description: description}
}

// parseQQFormatValue 拆开收尾字段：返回模板名和 JSON 补丁，二者至少有一个。
func parseQQFormatValue(value string) (name string, patch map[string]any, err error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil, nil
	}
	rawPatch := ""
	switch {
	case strings.HasPrefix(value, "{"):
		rawPatch = value
	case strings.Contains(value, "={"):
		index := strings.Index(value, "={")
		name, rawPatch = strings.TrimSpace(value[:index]), value[index+1:]
	default:
		name = value
	}
	if name != "" {
		if utf8.RuneCountInString(name) > qqFormatNameMaxRunes || strings.ContainsAny(name, "{}=\n") {
			return "", nil, fmt.Errorf("模板名最多 %d 个字，不能含 { } =", qqFormatNameMaxRunes)
		}
	}
	if rawPatch == "" {
		return name, nil, nil
	}
	if len(rawPatch) > qqFormatPatchMaxBytes {
		return "", nil, fmt.Errorf("格式 JSON 超过 %d 字节", qqFormatPatchMaxBytes)
	}
	if err := json.Unmarshal([]byte(rawPatch), &patch); err != nil {
		return "", nil, fmt.Errorf("格式不是合法的 JSON 对象: %w", err)
	}
	patch, err = sanitizeQQFormatPatch(patch)
	if err != nil {
		return "", nil, err
	}
	return name, patch, nil
}

// sanitizeQQFormatPatch 只留 markdown 和 keyboard，并拿掉 markdown.content：正文由
// 回复本身决定，格式不能把它换掉。
func sanitizeQQFormatPatch(patch map[string]any) (map[string]any, error) {
	clean := make(map[string]any, len(patch))
	for key, value := range patch {
		if !qqFormatAllowedTopKeys[key] {
			continue
		}
		object, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if key == "markdown" {
			delete(object, "content")
			if len(object) == 0 {
				continue
			}
		}
		clean[key] = object
	}
	if len(clean) == 0 {
		return nil, fmt.Errorf("格式里没有可用的 markdown 或 keyboard 字段")
	}
	return clean, nil
}

// applyQQFormatPatch 把补丁深度合并进一份 Markdown 请求体的副本。原请求体没有按钮、
// 补丁也没自带按钮行或模板 id 时丢掉 keyboard 部分，免得发出一个空键盘。
func applyQQFormatPatch(body, patch map[string]any) map[string]any {
	merged := cloneQQFormatValue(body).(map[string]any)
	for key, value := range patch {
		if key == "keyboard" {
			if _, has := merged["keyboard"]; !has && !qqFormatKeyboardSelfContained(value) {
				continue
			}
		}
		merged[key] = mergeQQFormatValue(merged[key], value)
	}
	return merged
}

func qqFormatKeyboardSelfContained(value any) bool {
	keyboard, _ := value.(map[string]any)
	if _, ok := keyboard["id"]; ok {
		return true
	}
	content, _ := keyboard["content"].(map[string]any)
	_, ok := content["rows"]
	return ok
}

func mergeQQFormatValue(base, patch any) any {
	patchObject, ok := patch.(map[string]any)
	if !ok {
		return cloneQQFormatValue(patch)
	}
	baseObject, ok := base.(map[string]any)
	if !ok {
		return cloneQQFormatValue(patch)
	}
	for key, value := range patchObject {
		baseObject[key] = mergeQQFormatValue(baseObject[key], value)
	}
	return baseObject
}

func cloneQQFormatValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = cloneQQFormatValue(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = cloneQQFormatValue(item)
		}
		return out
	default:
		return value
	}
}

type finalizeQQFormatKey struct{}

// finalizeQQFormat 把收尾时填的格式带到发送阶段，和按钮一样只挂在最后一条文字上。
type finalizeQQFormat struct {
	mu     sync.Mutex
	format *QQFormat
}

func withFinalizeQQFormat(ctx context.Context) (context.Context, *finalizeQQFormat) {
	holder := &finalizeQQFormat{}
	return context.WithValue(ctx, finalizeQQFormatKey{}, holder), holder
}

func finalizeQQFormatFromContext(ctx context.Context) *finalizeQQFormat {
	holder, _ := ctx.Value(finalizeQQFormatKey{}).(*finalizeQQFormat)
	return holder
}

func (f *finalizeQQFormat) put(format *QQFormat) {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.format = format
	f.mu.Unlock()
}

// take 取出格式并清空，同一轮只挂一次。
func (f *finalizeQQFormat) take() *QQFormat {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	format := f.format
	f.format = nil
	return format
}

// resolveQQFormat 把收尾字段落成本轮要用的格式：存模板、查模板都在这里做。出错只
// 记日志，格式是锦上添花，不能拖垮回复。
func (r *Runtime) resolveQQFormat(ctx context.Context, event MessageEvent, value string) *QQFormat {
	name, patch, err := parseQQFormatValue(value)
	if err != nil {
		log.Printf("qq: 忽略写坏的格式: %v", err)
		return nil
	}
	profileID := event.ProfileID
	switch {
	case name != "" && patch != nil:
		if err := r.upsertQQFormatTemplate(ctx, profileID, name, patch, time.Now()); err != nil {
			log.Printf("qq: 保存格式模板「%s」失败，本次照用: %v", name, err)
		}
	case name != "":
		templates, err := r.loadQQFormatTemplates(ctx, profileID)
		if err != nil {
			log.Printf("qq: 读取格式模板失败: %v", err)
			return nil
		}
		for _, entry := range templates {
			if entry.Name == name && !entry.Disabled {
				patch = entry.Patch
				break
			}
		}
		if patch == nil {
			return nil
		}
	case patch == nil:
		return nil
	}
	format := &QQFormat{Name: name, Patch: patch}
	if name != "" {
		format.Report = func(ok bool, sendErr error) {
			r.recordQQFormatResult(context.WithoutCancel(ctx), profileID, name, ok, sendErr)
		}
	}
	return format
}
