// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Package osc 是 OSC 1.0 的最小实现：消息与 bundle 的编解码，外加 UDP 收发。
//
// VRChat 只用到 OSC 很窄的一块（i/f/s/T/F 几种参数、单条消息），为这点功能
// 引一个第三方库不划算，自己写还能把字节布局钉进测试里。
package osc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
)

// Message 是一条 OSC 消息。Args 支持 int32、float32、string、bool、[]byte 和 nil，
// 编码时 int/int64/float64 会被收窄成 OSC 的 32 位类型。
type Message struct {
	Address string
	Args    []any
}

// Bundle 是 OSC bundle。Timetag 为 1 表示「立即执行」。
type Bundle struct {
	Timetag  uint64
	Elements []Message
}

// TimetagImmediately 是规范里约定的「立即」时间标签。
const TimetagImmediately uint64 = 1

// maxBundleDepth 限制嵌套 bundle 的深度：解码的是网络上来的包，不能被一个
// 自引用式的深层嵌套拖进递归栈溢出。
const maxBundleDepth = 8

var bundleTag = []byte("#bundle\x00")

var (
	ErrInvalidAddress = errors.New("osc: invalid address")
	ErrMalformed      = errors.New("osc: malformed packet")
)

// ValidateAddress 检查发送用的地址：必须以 / 开头，不能带空白和模式匹配字符。
// 这些字符在接收端会被当成通配符，发出去的就不是我们以为的那个地址了。
func ValidateAddress(address string) error {
	if !strings.HasPrefix(address, "/") || len(address) < 2 {
		return fmt.Errorf("%w: %q", ErrInvalidAddress, address)
	}
	for _, r := range address {
		if r <= ' ' || r == 0x7f || strings.ContainsRune("#*,?[]{}", r) {
			return fmt.Errorf("%w: %q", ErrInvalidAddress, address)
		}
	}
	return nil
}

// MarshalBinary 按 OSC 1.0 编码一条消息。
func (m Message) MarshalBinary() ([]byte, error) {
	if err := ValidateAddress(m.Address); err != nil {
		return nil, err
	}
	var tags strings.Builder
	tags.WriteByte(',')
	var payload bytes.Buffer
	for i, arg := range m.Args {
		switch value := arg.(type) {
		case int32:
			tags.WriteByte('i')
			writeInt32(&payload, value)
		case int:
			if value < math.MinInt32 || value > math.MaxInt32 {
				return nil, fmt.Errorf("osc: arg %d out of int32 range", i)
			}
			tags.WriteByte('i')
			writeInt32(&payload, int32(value))
		case int64:
			if value < math.MinInt32 || value > math.MaxInt32 {
				return nil, fmt.Errorf("osc: arg %d out of int32 range", i)
			}
			tags.WriteByte('i')
			writeInt32(&payload, int32(value))
		case float32:
			tags.WriteByte('f')
			writeUint32(&payload, math.Float32bits(value))
		case float64:
			tags.WriteByte('f')
			writeUint32(&payload, math.Float32bits(float32(value)))
		case string:
			if strings.ContainsRune(value, 0) {
				return nil, fmt.Errorf("osc: arg %d string contains NUL", i)
			}
			tags.WriteByte('s')
			writePaddedString(&payload, value)
		case bool:
			if value {
				tags.WriteByte('T')
			} else {
				tags.WriteByte('F')
			}
		case nil:
			tags.WriteByte('N')
		case []byte:
			tags.WriteByte('b')
			writeInt32(&payload, int32(len(value)))
			payload.Write(value)
			pad(&payload, len(value))
		default:
			return nil, fmt.Errorf("osc: arg %d has unsupported type %T", i, arg)
		}
	}
	var out bytes.Buffer
	writePaddedString(&out, m.Address)
	writePaddedString(&out, tags.String())
	out.Write(payload.Bytes())
	return out.Bytes(), nil
}

// MarshalBinary 编码一个只含消息的 bundle。
func (b Bundle) MarshalBinary() ([]byte, error) {
	var out bytes.Buffer
	out.Write(bundleTag)
	var tt [8]byte
	binary.BigEndian.PutUint64(tt[:], b.Timetag)
	out.Write(tt[:])
	for _, element := range b.Elements {
		data, err := element.MarshalBinary()
		if err != nil {
			return nil, err
		}
		writeInt32(&out, int32(len(data)))
		out.Write(data)
	}
	return out.Bytes(), nil
}

// ParsePacket 解码一个 UDP 包，bundle 会被展开成其中的全部消息。
func ParsePacket(data []byte) ([]Message, error) {
	return parsePacket(data, 0)
}

func parsePacket(data []byte, depth int) ([]Message, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: empty packet", ErrMalformed)
	}
	if !bytes.HasPrefix(data, bundleTag) {
		message, err := ParseMessage(data)
		if err != nil {
			return nil, err
		}
		return []Message{message}, nil
	}
	if depth >= maxBundleDepth {
		return nil, fmt.Errorf("%w: bundle nested too deep", ErrMalformed)
	}
	if len(data) < 16 {
		return nil, fmt.Errorf("%w: short bundle", ErrMalformed)
	}
	var out []Message
	rest := data[16:]
	for len(rest) > 0 {
		if len(rest) < 4 {
			return nil, fmt.Errorf("%w: truncated bundle element size", ErrMalformed)
		}
		size := int(int32(binary.BigEndian.Uint32(rest)))
		rest = rest[4:]
		if size <= 0 || size%4 != 0 || size > len(rest) {
			return nil, fmt.Errorf("%w: bad bundle element size %d", ErrMalformed, size)
		}
		messages, err := parsePacket(rest[:size], depth+1)
		if err != nil {
			return nil, err
		}
		out = append(out, messages...)
		rest = rest[size:]
	}
	return out, nil
}

// ParseMessage 解码单条 OSC 消息。
func ParseMessage(data []byte) (Message, error) {
	address, rest, err := readPaddedString(data)
	if err != nil {
		return Message{}, err
	}
	if !strings.HasPrefix(address, "/") {
		return Message{}, fmt.Errorf("%w: %q", ErrInvalidAddress, address)
	}
	message := Message{Address: address}
	if len(rest) == 0 {
		// OSC 1.0 之前的实现可能省略类型标签，按无参数处理。
		return message, nil
	}
	tags, rest, err := readPaddedString(rest)
	if err != nil {
		return Message{}, err
	}
	if !strings.HasPrefix(tags, ",") {
		return Message{}, fmt.Errorf("%w: type tag string must start with ','", ErrMalformed)
	}
	for _, tag := range tags[1:] {
		switch tag {
		case 'i':
			if len(rest) < 4 {
				return Message{}, fmt.Errorf("%w: truncated int32", ErrMalformed)
			}
			message.Args = append(message.Args, int32(binary.BigEndian.Uint32(rest)))
			rest = rest[4:]
		case 'f':
			if len(rest) < 4 {
				return Message{}, fmt.Errorf("%w: truncated float32", ErrMalformed)
			}
			message.Args = append(message.Args, math.Float32frombits(binary.BigEndian.Uint32(rest)))
			rest = rest[4:]
		case 's':
			var text string
			text, rest, err = readPaddedString(rest)
			if err != nil {
				return Message{}, err
			}
			message.Args = append(message.Args, text)
		case 'b':
			if len(rest) < 4 {
				return Message{}, fmt.Errorf("%w: truncated blob size", ErrMalformed)
			}
			size := int(int32(binary.BigEndian.Uint32(rest)))
			rest = rest[4:]
			padded := size + (4-size%4)%4
			if size < 0 || padded > len(rest) {
				return Message{}, fmt.Errorf("%w: truncated blob", ErrMalformed)
			}
			message.Args = append(message.Args, bytes.Clone(rest[:size]))
			rest = rest[padded:]
		case 'T':
			message.Args = append(message.Args, true)
		case 'F':
			message.Args = append(message.Args, false)
		case 'N':
			message.Args = append(message.Args, nil)
		default:
			return Message{}, fmt.Errorf("%w: unsupported type tag %q", ErrMalformed, tag)
		}
	}
	return message, nil
}

func writeInt32(buf *bytes.Buffer, value int32) { writeUint32(buf, uint32(value)) }

func writeUint32(buf *bytes.Buffer, value uint32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], value)
	buf.Write(b[:])
}

// writePaddedString 写一个以 NUL 结尾、补齐到 4 字节的 OSC 字符串。长度恰好是 4
// 的倍数时也要补满一组 NUL，否则接收端找不到结尾。
func writePaddedString(buf *bytes.Buffer, text string) {
	buf.WriteString(text)
	buf.WriteByte(0)
	pad(buf, len(text)+1)
}

func pad(buf *bytes.Buffer, length int) {
	for i := length % 4; i != 0 && i < 4; i++ {
		buf.WriteByte(0)
	}
}

func readPaddedString(data []byte) (string, []byte, error) {
	end := bytes.IndexByte(data, 0)
	if end < 0 {
		return "", nil, fmt.Errorf("%w: unterminated string", ErrMalformed)
	}
	padded := (end + 4) &^ 3
	if padded > len(data) {
		return "", nil, fmt.Errorf("%w: string padding truncated", ErrMalformed)
	}
	return string(data[:end]), data[padded:], nil
}
