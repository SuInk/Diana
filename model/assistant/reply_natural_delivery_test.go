package assistant

import (
	"reflect"
	"testing"
)

func TestNaturalDeliveryUsesOnlyModelBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       []string
	}{
		{"raw lines become punctuation", "恭喜你拿到 offer！\n\n搬去陌生城市会慌很正常\n你最担心哪一部分？", []string{"恭喜你拿到 offer！搬去陌生城市会慌很正常，你最担心哪一部分？"}},
		{"no inferred sentences", "端口被占了。先看看是谁占着。别急着杀进程", []string{"端口被占了。先看看是谁占着。别急着杀进程"}},
		{"list", "1. 检查配置" + notificationLineMarker + "2. 重启服务" + notificationLineMarker + "3. 查看日志", []string{"1. 检查配置\n2. 重启服务\n3. 查看日志"}},
		{"quotes", "原文如下" + notificationSplitMarker + "> 第一行" + notificationLineMarker + "> 第二行" + notificationSplitMarker + "我赞同这一点", []string{"原文如下", "> 第一行\n> 第二行", "我赞同这一点"}},
		{"code", "```python" + notificationLineMarker + "print(1)" + notificationLineMarker + "print(2)" + notificationLineMarker + "```", []string{"```python\nprint(1)\nprint(2)\n```"}},
		{"markers", "在的" + notificationSplitMarker + "怎么啦" + notificationSplitMarker + "说说看", []string{"在的", "怎么啦", "说说看"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := splitChatReply(tc.text, chatSplitLimits{}); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}
