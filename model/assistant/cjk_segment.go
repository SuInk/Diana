// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"sync"
	"sync/atomic"
	"unicode"

	"github.com/go-ego/gse"
)

// 选词原先只有 n-gram：中文连续段切一二三字组,「虎皮凤爪很好吃」会切出
// 「爪很」「皮凤」这种跨词碎片,它们和真实词一起进检索词表,既占掉词数
// 上限又在打分里贡献噪声权重。
//
// 引入 gse（纯 Go 的 jieba 移植,词典内嵌）做词典分词,只加在选词层:
// 词典词以更高权重并入词表,排序后先被选走;n-gram 仍然保留兜底,词典
// 没收录的新词、人名靠二字组保住召回,所以召回下限不变,变的是排序和
// 精度。存储层 FTS 索引仍按二元组切,查询短语也按二元组拆,词典只决定
// 「搜什么」,不碰「怎么存」,不存在索引和查询切法不一致的问题,也不用
// 重建索引。
//
// 词典要常驻约 130MB 内存,默认不开,由配置 dict_segment_enabled 控制。
// 开启是即时的:后台加载词典,加载完成前 cjkSegmentWords 返回 nil,调用方
// 自然退回纯 n-gram。关闭要重启进程才生效——已经加载的词典内存只有重启
// 才能归还,与其做一个"关了开关但内存还占着"的假关闭,不如把语义说清楚。

var (
	cjkSegmenter       gse.Segmenter
	cjkSegmentErr      error
	cjkSegmentWarmOnce sync.Once
	cjkSegmentWarmed   = make(chan struct{})
	cjkSegmentEnabled  atomic.Bool
)

// applyCJKSegmentConfig 按配置决定是否启用词典分词。启用是只进不退的闩:
// 配置里关掉不会在运行中把它扳回去,重启进程后才回到关闭状态,
// 语义与词典内存的生命周期一致。
func applyCJKSegmentConfig(cfg BotConfig) {
	if boolValue(cfg.DictSegmentEnabled, false) {
		cjkSegmentEnabled.Store(true)
		startCJKSegmenterWarmup()
	}
}

// startCJKSegmenterWarmup 异步加载词典,重复调用无害。
func startCJKSegmenterWarmup() {
	cjkSegmentWarmOnce.Do(func() {
		go func() {
			cjkSegmenter.SkipLog = true
			// 只装简体词典:全量词典（含繁体）常驻内存约 197MB,简体版约
			// 129MB、加载也快一半。QQ 场景几乎全是简体,繁体和词典没收录的
			// 词一样走 n-gram 兜底。
			cjkSegmenter, cjkSegmentErr = gse.NewEmbed("zh_s")
			close(cjkSegmentWarmed)
		}()
	})
}

// awaitCJKSegmenter 阻塞到词典就绪,给测试和需要确定性的调用方用。
func awaitCJKSegmenter() bool {
	startCJKSegmenterWarmup()
	<-cjkSegmentWarmed
	return cjkSegmentErr == nil
}

// cjkSegmentReady 报告词典当前是否可用;从不主动等待,
// 消息处理路径不会被首次加载卡住。
func cjkSegmentReady() bool {
	select {
	case <-cjkSegmentWarmed:
		return cjkSegmentErr == nil
	default:
		return false
	}
}

// cjkSegmentWords 把一段 CJK 连续文本切成 2 字以上的词典词。
// 词典未就绪或没切出多字词时返回 nil。
func cjkSegmentWords(run string) []string {
	if !cjkSegmentEnabled.Load() || strings.TrimSpace(run) == "" || !cjkSegmentReady() {
		return nil
	}
	var words []string
	for _, word := range cjkSegmenter.CutSearch(run, true) {
		word = strings.TrimSpace(word)
		runes := []rune(word)
		if len(runes) < 2 || !unicode.IsLetter(runes[0]) {
			continue
		}
		words = append(words, word)
	}
	return words
}
