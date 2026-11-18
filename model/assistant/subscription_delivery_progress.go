package assistant

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
)

// deliveryProgress 记一次订阅推送里已经送达的分段，补投时跳过它们。
//
// 一条推送在平台上可能是好几次发送：分条的每一段，以及 Telegram 上一段里的文字和
// 每张图、每个视频各发一次。以前只按「目标投完没有」记，文字发出去之后图片被拒，
// 整个目标算失败，下一轮补投连文字一起再发，群里就一遍遍刷同一段话。
//
// 键由分段序号和分段原文（带 CQ 码的文本）算出，同一份待投内容每轮切出来的分段
// 一样，键就一样；Telegram 子步骤再在分段键后面接「text」「image:0」这类名字。
type deliveryProgress struct {
	mu   sync.Mutex
	done map[string]bool
	mark func(key string) error
}

type deliveryProgressContextKey struct{}
type deliveryProgressScopeContextKey struct{}

func newDeliveryProgress(done []string, mark func(key string) error) *deliveryProgress {
	progress := &deliveryProgress{done: make(map[string]bool, len(done)), mark: mark}
	for _, key := range done {
		progress.done[key] = true
	}
	return progress
}

func withDeliveryProgress(ctx context.Context, progress *deliveryProgress) context.Context {
	if progress == nil {
		return ctx
	}
	return context.WithValue(ctx, deliveryProgressContextKey{}, progress)
}

// deliveryProgressFromContext 返回当前推送的进度和所在分段的键；没在订阅推送里时返回 nil。
func deliveryProgressFromContext(ctx context.Context) (*deliveryProgress, string) {
	progress, _ := ctx.Value(deliveryProgressContextKey{}).(*deliveryProgress)
	scope, _ := ctx.Value(deliveryProgressScopeContextKey{}).(string)
	return progress, scope
}

func withDeliveryProgressScope(ctx context.Context, scope string) context.Context {
	if strings.TrimSpace(scope) == "" {
		return ctx
	}
	return context.WithValue(ctx, deliveryProgressScopeContextKey{}, scope)
}

func deliveryChunkKey(index int, chunk string) string {
	return fmt.Sprintf("chunk:%d:%s", index, fingerprintOf(chunk))
}

func (p *deliveryProgress) delivered(key string) bool {
	if p == nil || key == "" {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.done[key]
}

// record 记下这一步已经送达。存不下来只记日志：消息已经发出去了，不能因此把这一步
// 判成失败，最坏是下一轮补投时这一段多发一次。
func (p *deliveryProgress) record(key string) {
	if p == nil || key == "" {
		return
	}
	p.mu.Lock()
	if p.done[key] {
		p.mu.Unlock()
		return
	}
	p.done[key] = true
	mark := p.mark
	p.mu.Unlock()
	if mark == nil {
		return
	}
	if err := mark(key); err != nil {
		log.Printf("diana subscription delivery step record failed: key=%s err=%v", key, err)
	}
}

// deliveryProgressStep 跑一个可跳过的发送步骤：已经送达的直接跳过，发成功的记下来。
// 不在订阅推送里（ctx 没有进度或没有分段键）时照常发送。
func deliveryProgressStep(ctx context.Context, name string, send func() (map[string]any, error)) (map[string]any, error) {
	progress, scope := deliveryProgressFromContext(ctx)
	if progress == nil || scope == "" {
		return send()
	}
	key := scope + "|" + name
	if progress.delivered(key) {
		return nil, nil
	}
	result, err := send()
	if err == nil {
		progress.record(key)
	}
	return result, err
}
