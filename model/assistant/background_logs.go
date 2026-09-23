// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"time"

	"github.com/SuInk/diana/model/applog"
)

// recordBackgroundFailure 把后台任务的失败写进运行日志。这些任务（记忆提取、语义
// 索引、人员档案写入）失败时不影响回复，以前只打到终端，界面上看就是「机器人好像
// 没记住」却查不到原因。throttleKey 相同的一分钟只记一条，数据库或上游出问题时
// 不会刷屏。
func (r *Runtime) recordBackgroundFailure(action, message, throttleKey string, err error, metadata map[string]any) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	if throttleKey == "" {
		throttleKey = action
	}
	if !r.backgroundLogThrottle.allow(throttleKey, time.Now()) {
		return
	}
	entry := applog.Entry{
		Kind:      applog.KindError,
		Level:     applog.LevelError,
		Action:    action,
		Message:   message,
		Metadata:  metadata,
		CreatedAt: time.Now(),
	}
	if err != nil {
		entry.Detail = truncateRunes(err.Error(), 500)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = writer.AppendLog(ctx, entry)
}
