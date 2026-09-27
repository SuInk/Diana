// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"log"
	"time"
)

// hotPathIndexes 是给写连接热路径补的索引。
//
// idx_inbound_events_message：入站事件按「消息 ID + 类型 + 群 + 发送者」找最新那一行，
// 发送前的并入检查（InboundEventSuperseded）、每条事件的审计回写
// （RecordInboundEventAudit）、追发合并（RecordInboundEventReplyMerge）、不知道事件
// ID 时的投递回执和连发交接都走这条。inbound_events 上原来没有 message_id 索引，
// 这几条都是整表扫描，而且全在唯一的写连接上：合成库（48 万条入站事件、2.7 GB）
// 上一次 0.2–7 秒，期间入队、领取、记忆任务全部排队——生产上 ClaimMemoryJobBatch、
// ClaimNextInboundEvent 秒级的 begin_transaction 等待和「supersession check failed:
// context deadline exceeded」都是它。
var hotPathIndexes = []struct {
	name string
	ddl  string
}{
	{"idx_inbound_events_message", `CREATE INDEX IF NOT EXISTS idx_inbound_events_message ON inbound_events(message_id, kind)`},
}

// ensureHotPathIndexes 尽力建立上面的索引。建不成只是慢，不能让启动失败。
//
// 老库升级后第一次启动要把 inbound_events 扫一遍来建索引，期间写连接被占着
// （这时候机器人还没开始收消息），合成库上冷缓存约 12 秒；之后每次启动
// IF NOT EXISTS 直接跳过。耗时超过一秒的打一行日志，免得运维以为启动卡死。
//
// 首次启动的时间要算进自更新的健康检查窗口（45 秒）里。同一次启动里补检索
// 索引不再整表扫描消息历史（省下的时间和这里新增的大致相当），控制台基线也
// 挪到了后台，所以首次启动总体不会比升级前更慢。
func (s *SQLiteStore) ensureHotPathIndexes() {
	for _, index := range hotPathIndexes {
		started := time.Now()
		if _, err := s.db.Exec(index.ddl); err != nil {
			log.Printf("storage: create index %s skipped: %v", index.name, err)
			continue
		}
		if elapsed := time.Since(started); elapsed >= time.Second {
			log.Printf("storage: created index %s in %s", index.name, elapsed.Round(time.Millisecond))
		}
	}
}
