package webui

import (
	"reflect"
	"testing"
)

func TestMaskLLMHeadersKeepsNamesDropsValues(t *testing.T) {
	masked := maskLLMHeaders(map[string]string{"X-Session-Affinity": "opaque", "X-Api-Key": "secret"})
	if want := map[string]string{"X-Session-Affinity": "", "X-Api-Key": ""}; !reflect.DeepEqual(masked, want) {
		t.Fatalf("masked = %#v, want %#v", masked, want)
	}
	if maskLLMHeaders(nil) != nil {
		t.Fatal("没有请求头时应当回 nil，而不是空对象")
	}
}

func TestMergeLLMHeaders(t *testing.T) {
	existing := map[string]string{"X-Keep": "old-value", "X-Drop": "gone", "X-Replace": "before"}
	for _, item := range []struct {
		name      string
		submitted map[string]string
		want      map[string]string
	}{
		{
			// 界面把脱敏读到的配置原样写回来：所有值都是空串，一个都不能丢。
			name:      "原样回写不洗掉任何头",
			submitted: map[string]string{"X-Keep": "", "X-Drop": "", "X-Replace": ""},
			want:      existing,
		},
		{
			name:      "没提交这个字段就整体保留",
			submitted: nil,
			want:      existing,
		},
		{
			name:      "删掉一行就是删掉这个头",
			submitted: map[string]string{"X-Keep": "", "X-Replace": ""},
			want:      map[string]string{"X-Keep": "old-value", "X-Replace": "before"},
		},
		{
			name:      "填了新值就覆盖",
			submitted: map[string]string{"X-Keep": "", "X-Replace": "after"},
			want:      map[string]string{"X-Keep": "old-value", "X-Replace": "after"},
		},
		{
			name:      "新增的头照常写入",
			submitted: map[string]string{"X-New": "fresh"},
			want:      map[string]string{"X-New": "fresh"},
		},
		{
			name:      "提交空对象表示清空全部",
			submitted: map[string]string{},
			want:      nil,
		},
		{
			name:      "从来没存过的头提交空值直接丢弃",
			submitted: map[string]string{"X-Never": ""},
			want:      nil,
		},
		{
			name:      "键名两侧空白被规整",
			submitted: map[string]string{"  X-New  ": "fresh"},
			want:      map[string]string{"X-New": "fresh"},
		},
	} {
		t.Run(item.name, func(t *testing.T) {
			got := mergeLLMHeaders(existing, item.submitted)
			if !reflect.DeepEqual(got, item.want) {
				t.Fatalf("mergeLLMHeaders = %#v, want %#v", got, item.want)
			}
		})
	}
}

// TestMergeLLMHeadersRoundTripIsIdempotent 盯住脱敏方案最容易坏的地方：读一次、
// 原样写回、再读一次，配置必须完全不变。值被清空回显，如果合并把空值当成「改成
// 空」，这个循环会把用户配的头全部洗掉，而且界面上看不出来。
func TestMergeLLMHeadersRoundTripIsIdempotent(t *testing.T) {
	stored := map[string]string{"X-Session-Affinity": "opaque", "X-Api-Key": "secret"}
	for round := 0; round < 3; round++ {
		stored = mergeLLMHeaders(stored, maskLLMHeaders(stored))
	}
	want := map[string]string{"X-Session-Affinity": "opaque", "X-Api-Key": "secret"}
	if !reflect.DeepEqual(stored, want) {
		t.Fatalf("三轮读写之后 = %#v, want %#v", stored, want)
	}
}
