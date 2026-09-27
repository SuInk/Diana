// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package osc

import (
	"bytes"
	"encoding/hex"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"
)

func mustHex(t *testing.T, text string) []byte {
	t.Helper()
	data, err := hex.DecodeString(strings.ReplaceAll(text, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// 两个用例取自 OSC 1.0 规范正文的示例包，字节逐一对照。
func TestMessageMatchesSpecExamples(t *testing.T) {
	cases := []struct {
		name    string
		message Message
		want    string
	}{
		{
			name:    "oscillator frequency",
			message: Message{Address: "/oscillator/4/frequency", Args: []any{float32(440.0)}},
			want: "2f6f7363 696c6c61 746f722f 342f6672 65717565 6e637900" +
				"2c660000 43dc0000",
		},
		{
			name:    "foo iisff",
			message: Message{Address: "/foo", Args: []any{int32(1000), int32(-1), "hello", float32(1.234), float32(5.678)}},
			want: "2f666f6f 00000000 2c696973 66660000" +
				"000003e8 ffffffff 68656c6c 6f000000 3f9df3b6 40b5b22d",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.message.MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
			want := mustHex(t, tc.want)
			if !bytes.Equal(got, want) {
				t.Fatalf("encoded\n got %x\nwant %x", got, want)
			}
			decoded, err := ParseMessage(want)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(decoded, tc.message) {
				t.Fatalf("decoded %#v, want %#v", decoded, tc.message)
			}
		})
	}
}

func TestMessageBoolAndIntNarrowing(t *testing.T) {
	message := Message{Address: "/chatbox/input", Args: []any{"hi", true, false}}
	got, err := message.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	// T/F 只占类型标签，不占参数字节。
	want := mustHex(t, "2f636861 74626f78 2f696e70 75740000 2c735446 00000000 68690000")
	if !bytes.Equal(got, want) {
		t.Fatalf("got %x\nwant %x", got, want)
	}

	narrowed, err := Message{Address: "/a", Args: []any{7, 0.5}}.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := ParseMessage(narrowed)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded.Args, []any{int32(7), float32(0.5)}) {
		t.Fatalf("args = %#v", decoded.Args)
	}
	if _, err := (Message{Address: "/a", Args: []any{int64(1 << 40)}}).MarshalBinary(); err == nil {
		t.Fatal("out-of-range int64 must be rejected")
	}
}

func TestStringPaddingOnFourByteBoundary(t *testing.T) {
	// "/abc" 正好 4 字节，结尾仍要补满一组 NUL。
	got, err := Message{Address: "/abc", Args: []any{"wxyz"}}.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	want := mustHex(t, "2f616263 00000000 2c730000 7778797a 00000000")
	if !bytes.Equal(got, want) {
		t.Fatalf("got %x\nwant %x", got, want)
	}
}

func TestBlobAndNilRoundTrip(t *testing.T) {
	message := Message{Address: "/blob", Args: []any{[]byte{1, 2, 3, 4, 5}, nil, int32(9)}}
	data, err := message.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if len(data)%4 != 0 {
		t.Fatalf("packet length %d not aligned", len(data))
	}
	decoded, err := ParseMessage(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, message) {
		t.Fatalf("decoded %#v", decoded)
	}
}

func TestValidateAddress(t *testing.T) {
	for _, address := range []string{"/avatar/parameters/Happy", "/input/Jump"} {
		if err := ValidateAddress(address); err != nil {
			t.Fatalf("%q: %v", address, err)
		}
	}
	for _, address := range []string{"", "/", "avatar", "/a b", "/a*", "/a#", "/a{b}"} {
		if err := ValidateAddress(address); !errors.Is(err, ErrInvalidAddress) {
			t.Fatalf("%q should be invalid, got %v", address, err)
		}
	}
}

func TestBundleRoundTripAndLayout(t *testing.T) {
	bundle := Bundle{Timetag: TimetagImmediately, Elements: []Message{
		{Address: "/a", Args: []any{int32(1)}},
		{Address: "/b", Args: []any{true}},
	}}
	data, err := bundle.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	want := mustHex(t, "2362756e 646c6500 00000000 00000001"+
		"0000000c 2f610000 2c690000 00000001"+
		"00000008 2f620000 2c540000")
	if !bytes.Equal(data, want) {
		t.Fatalf("got %x\nwant %x", data, want)
	}
	messages, err := ParsePacket(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(messages, bundle.Elements) {
		t.Fatalf("messages = %#v", messages)
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	bad := [][]byte{
		nil,
		[]byte("/abc"),                       // 没有 NUL
		mustHex(t, "2f610000 2c690000 0000"), // int 被截断
		mustHex(t, "2f610000 2c780000"),      // 未知类型标签
		mustHex(t, "2f610000 69000000"),      // 类型标签缺逗号
		append([]byte("#bundle\x00"), make([]byte, 8)...),
	}
	bad[5] = append(bad[5], mustHex(t, "000000ff")...) // 元素长度越界
	for i, data := range bad {
		if _, err := ParsePacket(data); err == nil {
			t.Fatalf("case %d: expected error", i)
		}
	}
	// 自嵌套过深的 bundle 要被拒，而不是一路递归下去。
	nested := []byte{}
	inner, _ := Message{Address: "/x"}.MarshalBinary()
	nested = inner
	for range maxBundleDepth + 1 {
		wrapped := append([]byte("#bundle\x00"), make([]byte, 8)...)
		size := []byte{0, 0, 0, byte(len(nested))}
		if len(nested) > 255 {
			t.Fatal("test bundle grew too large")
		}
		wrapped = append(wrapped, size...)
		nested = append(wrapped, nested...)
	}
	if _, err := ParsePacket(nested); err == nil {
		t.Fatal("deeply nested bundle should be rejected")
	}
}

func TestUDPLoopback(t *testing.T) {
	received := make(chan Message, 4)
	server, err := Listen("127.0.0.1:0", func(message Message, _ *net.UDPAddr) {
		received <- message
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	client, err := Dial(server.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	sent := Message{Address: "/avatar/parameters/Happy", Args: []any{true}}
	if err := client.Send(sent); err != nil {
		t.Fatal(err)
	}
	if err := client.SendBundle(Bundle{Timetag: TimetagImmediately, Elements: []Message{
		{Address: "/avatar/change", Args: []any{"avtr_x"}},
		{Address: "/input/Vertical", Args: []any{float32(0.5)}},
	}}); err != nil {
		t.Fatal(err)
	}
	want := []Message{sent, {Address: "/avatar/change", Args: []any{"avtr_x"}}, {Address: "/input/Vertical", Args: []any{float32(0.5)}}}
	for i, expected := range want {
		select {
		case got := <-received:
			if !reflect.DeepEqual(got, expected) {
				t.Fatalf("message %d = %#v, want %#v", i, got, expected)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for message %d", i)
		}
	}
}

func TestServerSurvivesHandlerPanicAndGarbage(t *testing.T) {
	received := make(chan Message, 2)
	calls := 0
	server, err := Listen("127.0.0.1:0", func(message Message, _ *net.UDPAddr) {
		calls++
		if calls == 1 {
			panic("boom")
		}
		received <- message
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	conn, err := net.DialUDP("udp", nil, server.LocalAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("garbage"))
	first, _ := Message{Address: "/first"}.MarshalBinary()
	second, _ := Message{Address: "/second"}.MarshalBinary()
	_, _ = conn.Write(first)
	_, _ = conn.Write(second)
	select {
	case got := <-received:
		if got.Address != "/second" {
			t.Fatalf("got %q", got.Address)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server stopped after handler panic")
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if err := server.Close(); err != nil {
		t.Fatal("second close should be a no-op")
	}
}
