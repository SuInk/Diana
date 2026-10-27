package assistant

import (
	"context"
	"strings"
	"testing"
)

func TestQQOfficialGalleryMarkdown(t *testing.T) {
	content, ok := qqOfficialGalleryMarkdown([]string{"https://cdn.example.com/a.jpg", "https://cdn.example.com/b.jpg?x=1&y=2"})
	if !ok {
		t.Fatal("两张公网图应当合成图集")
	}
	for _, want := range []string{
		"nodeType=multiPic&nodeID=dianaPics&state=start&itemsPerRow=3&itemsNum=2",
		"index=0&picUrl=https%3A%2F%2Fcdn.example.com%2Fa.jpg",
		"index=1&picUrl=https%3A%2F%2Fcdn.example.com%2Fb.jpg%3Fx%3D1%26y%3D2",
		"state=end",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("图集缺少 %q：%s", want, content)
		}
	}
	for _, images := range [][]string{
		{"https://cdn.example.com/a.jpg"},
		{"https://cdn.example.com/a.jpg", "http://127.0.0.1:18080/media/b.jpg"},
		{"https://cdn.example.com/a.jpg", "http://192.168.1.5/b.jpg"},
		{"https://cdn.example.com/a.jpg", "/tmp/b.jpg"},
		{"https://cdn.example.com/a.jpg", "data:image/png;base64,AAAA"},
		{"https://cdn.example.com/a.jpg", "http://localhost/b.jpg"},
	} {
		if _, ok := qqOfficialGalleryMarkdown(images); ok {
			t.Fatalf("%v 有 QQ 拉不到的图，不该合成图集", images)
		}
	}
}

// 多张公网图合成一条图集消息，不再逐张上传。
func TestQQOfficialSendsPublicImagesAsGallery(t *testing.T) {
	api := newQQFakeAPI(t)
	if err := api.channel().Send(context.Background(), OutgoingMessage{
		GroupID: "G", ImageURLs: []string{"https://cdn.example.com/a.jpg", "https://cdn.example.com/b.jpg"}, ReplyMessageID: "in1",
	}); err != nil {
		t.Fatal(err)
	}
	if len(api.messages) != 1 || len(api.prepares) != 0 {
		t.Fatalf("messages = %#v prepares = %#v", api.messages, api.prepares)
	}
	markdown, _ := api.messages[0]["markdown"].(map[string]any)
	if api.messages[0]["msg_type"] != float64(2) || !strings.Contains(markdown["content"].(string), "multiPic") {
		t.Fatalf("gallery message = %#v", api.messages[0])
	}
}
