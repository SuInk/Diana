// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Sec-MS-GEC v2 的算法来自 Edge 浏览器朗读接口。v2 token 是
// "<16 位 CS64>-<64 位 SHA-256>"，SHA-256 的输入带一段固定前缀；
// CS64 是一组 32 位乘法和 rol16 混淆。算法实现参考 MIT 许可的
// node-edge-tts（src/drm.ts），没有引入其运行时依赖。
const (
	edgeSecMSGECPrefix = "Copyright (C) Microsoft Corporation. All rights reserved."
	// windowsEpochOffset 是 1601-01-01 到 1970-01-01 的秒数。
	windowsEpochOffset = 11644473600
)

// edgeCS64Coefficients 是 CS64 的 14 组系数和偏移量。
var edgeCS64Coefficients = [...][2]uint32{
	{0xE82E91B1, 0xB32700D0},
	{0x44DE24C6, 0x88DFF51D},
	{0x3BEB65BC, 0x29514038},
	{0xC89C5933, 0xB328CBDF},
	{0x8D1EC211, 0xD405B4D5},
	{0x314EF5D5, 0x26E55F3C},
	{0xE92491B1, 0x607801C6},
	{0xF0A5B9D5, 0x82B2C1FB},
	{0x06064CF3, 0xFA51A3DA},
	{0xDA79FA60, 0x207CC464},
	{0xBED20560, 0x3C3C74F3},
	{0x89F9C9F8, 0xD2D14B61},
	{0x35FA38C6, 0xBB04A2C1},
	{0xCB49E3C6, 0xC5626167},
}

func edgeRol16(value uint32) uint32 {
	return value<<16 | value>>16
}

// edgeCS64 计算 token 的 16 位 CS64 部分。
func edgeCS64(data, digest []byte) [8]byte {
	keyA := binary.LittleEndian.Uint32(digest[0:4]) | 1
	keyB := binary.LittleEndian.Uint32(digest[4:8]) | 1
	factors := [2]uint32{keyA * 0x82893933, keyB * 0x82893933}

	terms := make([]uint32, 0, len(edgeCS64Coefficients)+len(data)/2+2)
	offsets := make([]uint32, 0, cap(terms))
	for index, coefficient := range edgeCS64Coefficients {
		terms = append(terms, coefficient[0]*factors[index%2])
		offsets = append(offsets, coefficient[1])
	}

	paddedLength := ((len(data) + 1 + 7) / 8) * 8
	padded := make([]byte, paddedLength)
	copy(padded[1:], data)
	for index := 0; index < paddedLength/4; index++ {
		word := binary.LittleEndian.Uint32(padded[index*4:])
		term := word * keyA
		offset := edgeRol16(word) * 0xEF0569FB
		if index%2 == 1 {
			term = word * keyB
			offset = edgeRol16(word) * 0xC31713DB
		}
		if index == 0 {
			term += 0xF2F90B1A * factors[0]
			offset += 0x0B1A0000
		}
		terms = append(terms, term)
		offsets = append(offsets, offset)
	}

	var swapAcc, swapLast, state, acc uint32
	for index := 0; index < len(terms); index += 2 {
		x, y := terms[index], terms[index+1]

		t := edgeRol16(offsets[index] + x)
		q := t * 0x76F50F34
		swapAcc += edgeRol16(q)*0xC3EFEA97 + q

		t = edgeRol16(offsets[index+1] + y)
		q = t * 0x1881CE3C
		swapLast = edgeRol16(q)*0x35BD1EC9 + q
		swapAcc += swapLast

		state = state*keyA + x
		for _, constant := range [...]uint32{0xCF98B111, 0x87085B9F, 0x12CEB96D, 0x257E1D83} {
			state = edgeRol16(state) * constant
		}
		acc += state

		t = state*keyB + y
		t = edgeRol16(t) * 0xA27416F5
		t = edgeRol16(t) * 0xD38396FF
		state = edgeRol16(edgeRol16(t)*0x7C932B89) * 0xBFA49F69
		acc += state
	}

	var out [8]byte
	binary.LittleEndian.PutUint32(out[0:4], swapLast^state)
	binary.LittleEndian.PutUint32(out[4:8], swapAcc^acc)
	return out
}

// edgeSecMSGEC 生成 Sec-MS-GEC v2 token：Windows 时间按五分钟取整，
// 拼上客户端令牌做 SHA-256，再用 CS64 混淆出前 16 位。
func edgeSecMSGEC(now time.Time) string {
	seconds := now.Unix() + windowsEpochOffset
	seconds -= seconds % 300
	ticks := uint64(seconds) * 10_000_000
	data := fmt.Sprintf("%d%s", ticks, edgeTrustedClientToken)
	sum := sha256.Sum256([]byte(edgeSecMSGECPrefix + data))
	cs64 := edgeCS64([]byte(data), sum[:])
	return strings.ToUpper(hex.EncodeToString(cs64[:])) + "-" + strings.ToUpper(hex.EncodeToString(sum[:]))
}
