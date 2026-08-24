// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"database/sql"
	"strings"
	"unicode"
)

// 历史检索原先是 LIKE '%词%' 的 OR 拼接：前置通配符用不上任何索引，跨会话检索
// 只能全表扫；排序是「第一个词权重 8、其余各 1」的相加，既没有 IDF 也没有文档
// 长度归一化，「怎么样」这类高频词一命中就排前面。
//
// 改用 FTS5 倒排索引 + BM25。
//
// 分词方式是自己切二元组，不用 FTS5 自带的 trigram：trigram 按三字符切，查不了
// 2 字词，而中文里「凤爪」「好吃」「转发」这类太常见，丢掉召回会明显变差。实测
// 若把短词用 LIKE 补进同一条 SQL，全表扫又回来了，比原来还慢一个数量级。
//
// 二元组方案把「虎皮凤爪很好吃」存成 token 序列「虎皮 皮凤 凤爪 爪很 很好 好吃」，
// 查询时把词同样切成二元组做短语匹配：「虎皮凤爪」→ 短语「虎皮 皮凤 凤爪」，
// 「凤爪」→ 单 token「凤爪」。两种长度都能走索引，不必再退回扫描。
//
// 索引内容只取发言者、账号和正文。payload 是整条事件的 JSON，进索引既让库体积
// 翻几倍，又会把元数据当正文搜出来。
//
// FTS5 在个别构建里可能不可用，建表失败时保留原来的 LIKE 路径，检索降级但不中断。

const messageHistoryFTSTable = "message_events_fts"

// messageHistoryIndexTokens 把文本切成用空格分隔的检索 token。
// CJK 连续段切二元组，ASCII 连续段整体作为一个词。
func messageHistoryIndexTokens(parts ...string) string {
	tokens := make([]string, 0, 32)
	appendRun := func(runs []rune, cjk bool) {
		if len(runs) == 0 {
			return
		}
		if !cjk {
			tokens = append(tokens, strings.ToLower(string(runs)))
			return
		}
		if len(runs) == 1 {
			tokens = append(tokens, string(runs))
			return
		}
		for index := 0; index+2 <= len(runs); index++ {
			tokens = append(tokens, string(runs[index:index+2]))
		}
	}
	for _, part := range parts {
		var ascii, cjk []rune
		for _, value := range part {
			switch {
			case value <= unicode.MaxASCII && (unicode.IsLetter(value) || unicode.IsDigit(value)):
				appendRun(cjk, true)
				cjk = cjk[:0]
				ascii = append(ascii, value)
			case unicode.In(value, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul):
				appendRun(ascii, false)
				ascii = ascii[:0]
				cjk = append(cjk, value)
			default:
				appendRun(ascii, false)
				appendRun(cjk, true)
				ascii, cjk = ascii[:0], cjk[:0]
			}
		}
		appendRun(ascii, false)
		appendRun(cjk, true)
	}
	return strings.Join(tokens, " ")
}

// messageHistoryFTSQuery 把检索词拼成 FTS5 查询串。
// 每个词按同一套二元组切分后作为短语匹配，短语内 token 必须连续出现，
// 因此「虎皮凤爪」不会被「凤爪虎皮」这种乱序命中。
func messageHistoryFTSQuery(terms []string) string {
	phrases := make([]string, 0, len(terms))
	seen := make(map[string]struct{}, len(terms))
	for _, term := range terms {
		tokens := messageHistoryIndexTokens(strings.TrimSpace(term))
		if tokens == "" {
			continue
		}
		if _, ok := seen[tokens]; ok {
			continue
		}
		seen[tokens] = struct{}{}
		// token 里不会有双引号（切分时非字母数字一律作为分隔符丢弃），
		// 这里仍然转义一次，避免将来改分词规则时留下注入面。
		//
		// 末尾加 * 做前缀匹配：ASCII 连续段整体存成一个 token，不加前缀的话搜
		// 「diana」命中不了「dianabot」、搜「deploy」命中不了「deployment」，
		// 而原来的 LIKE 是能命中的。CJK 侧 token 长度固定为二元组，前缀匹配
		// 退化成精确匹配，不会放宽召回。
		phrases = append(phrases, `"`+strings.ReplaceAll(tokens, `"`, `""`)+`"*`)
	}
	if len(phrases) == 0 {
		return ""
	}
	return strings.Join(phrases, " OR ")
}

// ensureMessageHistoryFTS 建立索引表并回填存量数据。
// 返回索引是否可用；不可用时调用方回退到 LIKE 检索。
func ensureMessageHistoryFTS(db *sql.DB) bool {
	if db == nil {
		return false
	}
	if _, err := db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS ` + messageHistoryFTSTable +
		` USING fts5(search_text)`); err != nil {
		return false
	}
	// token 要在 Go 侧切，触发器做不到，因此写入同步放在 AppendMessageEvent 里。
	// 这里只补存量：升级到本版本时库里已有的历史都要进索引。
	rows, err := db.Query(`SELECT e.rowid, COALESCE(e.sender_name, ''), COALESCE(e.user_id, ''), COALESCE(e.text, ''), COALESCE(e.search_extra, '')
FROM message_events AS e
WHERE e.rowid NOT IN (SELECT rowid FROM ` + messageHistoryFTSTable + `)`)
	if err != nil {
		return false
	}
	type pending struct {
		rowID  int64
		tokens string
	}
	batch := make([]pending, 0, 256)
	for rows.Next() {
		var rowID int64
		var sender, userID, text, extra string
		if err := rows.Scan(&rowID, &sender, &userID, &text, &extra); err != nil {
			_ = rows.Close()
			return false
		}
		batch = append(batch, pending{rowID: rowID, tokens: messageHistoryIndexTokens(sender, userID, text, extra)})
	}
	closeErr := rows.Close()
	if err := rows.Err(); err != nil || closeErr != nil {
		return false
	}
	for _, item := range batch {
		if _, err := db.Exec(`INSERT INTO `+messageHistoryFTSTable+`(rowid, search_text) VALUES (?, ?)`,
			item.rowID, item.tokens); err != nil {
			return false
		}
	}
	return true
}

// indexMessageHistoryRow 让某条消息的索引与正表保持一致。
// AppendMessageEvent 走 upsert，同一条消息会被重复写入，所以先删后插。
func (s *SQLiteStore) indexMessageHistoryRow(id, sender, userID, text, extra string) {
	if !s.historyFTS {
		return
	}
	var rowID int64
	if err := s.db.QueryRow(`SELECT rowid FROM message_events WHERE id = ?`, id).Scan(&rowID); err != nil {
		return
	}
	if _, err := s.db.Exec(`DELETE FROM `+messageHistoryFTSTable+` WHERE rowid = ?`, rowID); err != nil {
		return
	}
	_, _ = s.db.Exec(`INSERT INTO `+messageHistoryFTSTable+`(rowid, search_text) VALUES (?, ?)`,
		rowID, messageHistoryIndexTokens(sender, userID, text, extra))
}
