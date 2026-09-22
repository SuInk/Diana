// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"testing"
	"time"
)

func liveSeqEvent(seq string) MessageEvent {
	return MessageEvent{
		Kind: EventKindGroup, Platform: PlatformOneBotV11,
		ProfileID: "qq", GroupID: "20003", UserID: "917568554",
		MessageID: "m" + seq, MessageSeq: seq, Time: time.Now().Unix(),
	}
}

func drainSeqProbes(r *Runtime) []groupSeqProbe {
	var out []groupSeqProbe
	for {
		select {
		case probe := <-r.inboundSeqProbe:
			out = append(out, probe)
		default:
			return out
		}
	}
}

// 桥接漏推一条事件不会断线，重连探测那一档完全看不到——2026-09-22 线上就是
// 这样丢了一条。连接一直好着时也要比 seq。
func TestLiveSeqGapTriggersProbeWithoutReconnect(t *testing.T) {
	r := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	// 不 arm：模拟连接一直正常、没有重连过。
	r.observeLiveGroupSeq(liveSeqEvent("7800"))
	if probes := drainSeqProbes(r); len(probes) != 0 {
		t.Fatalf("第一条只记录基线，不该探测：%#v", probes)
	}
	r.observeLiveGroupSeq(liveSeqEvent("7802"))
	probes := drainSeqProbes(r)
	if len(probes) != 1 || probes[0].seq != 7802 {
		t.Fatalf("跳号了该探测一次：%#v", probes)
	}
}

// 连号不探测：绝大多数消息都是连号的，每条都查会把历史接口刷爆。
func TestLiveSeqContinuousNoProbe(t *testing.T) {
	r := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	for _, seq := range []string{"100", "101", "102", "103"} {
		r.observeLiveGroupSeq(liveSeqEvent(seq))
	}
	if probes := drainSeqProbes(r); len(probes) != 0 {
		t.Fatalf("连号不该探测：%#v", probes)
	}
}

// 撤回和系统提示也占 seq，跳号未必是丢消息。同一个群短时间内只探一次。
func TestLiveSeqProbeIsRateLimited(t *testing.T) {
	r := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.observeLiveGroupSeq(liveSeqEvent("200"))
	r.observeLiveGroupSeq(liveSeqEvent("205"))
	r.observeLiveGroupSeq(liveSeqEvent("210"))
	if probes := drainSeqProbes(r); len(probes) != 1 {
		t.Fatalf("冷却期内只该探一次：%#v", probes)
	}
	// 冷却过去之后可以再探。
	r.seqProbeMu.Lock()
	r.liveSeqProbedAt[groupSeqProbeKey(liveSeqEvent("0"))] = time.Now().Add(-liveSeqProbeCooldown - time.Second)
	r.seqProbeMu.Unlock()
	r.observeLiveGroupSeq(liveSeqEvent("220"))
	if probes := drainSeqProbes(r); len(probes) != 1 {
		t.Fatalf("冷却过后该能再探一次：%#v", probes)
	}
}

// 重连那一档的行为不变：每个群连上后的第一条实时消息照样探测，哪怕没有跳号。
func TestReconnectProbeStillFiresOnFirstMessage(t *testing.T) {
	r := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.armGroupSeqProbes()
	r.observeLiveGroupSeq(liveSeqEvent("500"))
	if probes := drainSeqProbes(r); len(probes) != 1 {
		t.Fatalf("重连后第一条该探测：%#v", probes)
	}
	r.observeLiveGroupSeq(liveSeqEvent("501"))
	if probes := drainSeqProbes(r); len(probes) != 0 {
		t.Fatalf("同一次连接里连号不该再探：%#v", probes)
	}
}
