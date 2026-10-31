// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"image"
	"math"
	"strings"
	"sync"
	"time"

	golangdraw "golang.org/x/image/draw"
)

// relationAvatarTimeout 是整张图取头像的总时限。头像拿不到就画纯色圆点，不能为
// 几张头像把出图拖住。
const relationAvatarTimeout = 10 * time.Second

// loadRelationAvatars 并发取图上每个人的头像，取不到的人不在结果里。
func (t *dianaGroupRelationsTool) loadRelationAvatars(ctx context.Context, ids []string) map[string]image.Image {
	ctx, cancel := context.WithTimeout(ctx, relationAvatarTimeout)
	defer cancel()
	avatars := make(map[string]image.Image, len(ids))
	var mu sync.Mutex
	var wg sync.WaitGroup
	gate := make(chan struct{}, 8)
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer recoverGoroutinePanic("relation_graph.avatar")
			defer wg.Done()
			select {
			case gate <- struct{}{}:
				defer func() { <-gate }()
			case <-ctx.Done():
				return
			}
			avatar := t.loadRelationAvatar(ctx, id)
			if avatar == nil {
				return
			}
			mu.Lock()
			avatars[id] = avatar
			mu.Unlock()
		}(id)
	}
	wg.Wait()
	return avatars
}

func (t *dianaGroupRelationsTool) loadRelationAvatar(ctx context.Context, userID string) image.Image {
	source := t.runtime.avatarSourceURL(ctx, t.event, userID, false)
	if source == "" {
		return nil
	}
	if normalizedHTTPURL(source) != "" {
		// 头像地址固定、内容会换，按天换缓存键，和头像匹配那边一致。
		separator := "?"
		if strings.Contains(source, "?") {
			separator = "&"
		}
		source += separator + "diana_avatar_day=" + time.Now().Format("20060102")
	}
	body, _, err := readHistoryImageSource(ctx, source, 0)
	if err != nil {
		return nil
	}
	avatar, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return nil
	}
	return avatar
}

// circleAvatar 把头像居中裁成正方形、缩到 size 像素，再挖成圆，圆边带抗锯齿。
func circleAvatar(source image.Image, size int) image.Image {
	if size <= 0 {
		return nil
	}
	bounds := source.Bounds()
	side := min(bounds.Dx(), bounds.Dy())
	crop := image.Rect(0, 0, side, side).Add(bounds.Min).Add(image.Pt((bounds.Dx()-side)/2, (bounds.Dy()-side)/2))
	out := image.NewNRGBA(image.Rect(0, 0, size, size))
	golangdraw.CatmullRom.Scale(out, out.Bounds(), source, crop, golangdraw.Src, nil)
	radius := float64(size) / 2
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			distance := math.Hypot(float64(x)+0.5-radius, float64(y)+0.5-radius)
			coverage := math.Max(0, math.Min(1, radius-distance+0.5))
			offset := out.PixOffset(x, y)
			out.Pix[offset+3] = uint8(math.Round(float64(out.Pix[offset+3]) * coverage))
		}
	}
	return out
}
