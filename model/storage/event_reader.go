package storage

import (
	"context"
	"database/sql"
	"log"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

const eventReaderConns = 4

// historySessionsIndex 覆盖 ListHistorySessions 用到的全部列，表达式必须和查询里
// 逐字一致，查询规划器才认得出来。
//
// 那条查询按平台、机器人、会话分组取最新时间，平台只存在 payload 里。没有这个
// 索引时每一行都要回表读整段 payload 再解析 JSON，30 万条的合成库上约 0.75 秒；
// 有了它只扫索引，约 0.27 秒。代价是每次写消息多维护一个索引项。
const historySessionsIndex = `CREATE INDEX IF NOT EXISTS idx_message_events_history_sessions ON message_events(
  kind, group_id, user_id,
  COALESCE(json_extract(payload, '$.platform'), ''),
  COALESCE(profile_id, json_extract(payload, '$.profile_id'), ''),
  event_time
)`

// ensureHistorySessionsIndex 尽力建立回补会话索引。建不成只影响速度：历史里若有
// 解析不了的 payload，建表达式索引会报错，这时不能让启动失败。
func (s *SQLiteStore) ensureHistorySessionsIndex() {
	if _, err := s.db.Exec(historySessionsIndex); err != nil {
		log.Printf("storage: create history sessions index skipped: %v", err)
	}
}

// Record browsing must not occupy the connection used for durable ingest.
// Each reader connection receives its own read-only pragmas via the driver DSN.
func (s *SQLiteStore) openEventReader() error {
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_app_logs_action_target_time ON app_logs(action, target, created_at);
CREATE INDEX IF NOT EXISTS idx_inbound_events_profile_order ON inbound_events(profile_id, event_time DESC, created_at DESC, id DESC);`); err != nil {
		return err
	}
	s.ensureHistorySessionsIndex()
	if s.path == "" {
		return nil
	} // In-memory and custom DSNs retain their existing semantics.
	uriPath := filepath.ToSlash(s.path)
	if filepath.VolumeName(s.path) != "" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath}
	q := u.Query()
	q.Set("mode", "ro")
	q.Add("_pragma", "query_only(1)")
	q.Add("_pragma", "busy_timeout(1000)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	// WAL 下读连接之间互不阻塞，多开几条只多占几份页缓存。原来只有 2 条时，
	// 历史检索、记录列表、提示词组装里的只读查询挤在一起排队；重连回补、状态
	// 计数、缓存命中这些读也从写连接挪了过来。
	db.SetMaxOpenConns(eventReaderConns)
	db.SetMaxIdleConns(eventReaderConns)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return err
	}
	s.readDB = db
	return nil
}

func (s *SQLiteStore) eventReader() *sql.DB {
	if s.readDB != nil {
		return s.readDB
	}
	return s.db
}
