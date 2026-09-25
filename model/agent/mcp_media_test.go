package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func testMCPMediaCollector(t *testing.T) (*mcpMediaCollector, string) {
	t.Helper()
	dir := t.TempDir()
	return &mcpMediaCollector{server: "demo", localDir: dir, callStart: time.Now()}, dir
}

func mediaIDsOf(t *testing.T, output string) []string {
	t.Helper()
	return mcpMediaIDPattern.FindAllString(output, -1)
}

// 以前 MCP 返回的图片只剩一句「返回了图片」，数据被扔掉，模型看不到也发不出去。
func TestMCPImageContentIsKeptForViewingAndSending(t *testing.T) {
	collector, _ := testMCPMediaCollector(t)
	data := []byte("\x89PNG\r\n\x1a\nfake-image")
	output, err := formatSDKMCPToolResult(&mcpsdk.CallToolResult{Content: []mcpsdk.Content{
		&mcpsdk.TextContent{Text: "画好了"},
		&mcpsdk.ImageContent{Data: data, MIMEType: "image/png"},
	}}, collector)
	if err != nil {
		t.Fatal(err)
	}
	ids := mediaIDsOf(t, output)
	if len(ids) != 1 {
		t.Fatalf("结果里应有一个 media_id：%s", output)
	}
	media, ok := LookupMCPMedia(ids[0])
	if !ok || media.Kind != MCPMediaImage || !bytes.Equal(media.Data, data) || media.Server != "demo" {
		t.Fatalf("暂存的图片不对：%+v ok=%v", media, ok)
	}
	parts := (&MCPTool{}).ToolResultParts(output)
	if len(parts) != 1 || parts[0].Type != llm.ContentPartImageURL || !strings.HasPrefix(parts[0].ImageURL, "data:image/png;base64,") {
		t.Fatalf("图片没有附给模型：%+v", parts)
	}
}

func TestMCPAudioAndBlobResourceAreKept(t *testing.T) {
	collector, _ := testMCPMediaCollector(t)
	output, err := formatSDKMCPToolResult(&mcpsdk.CallToolResult{Content: []mcpsdk.Content{
		&mcpsdk.AudioContent{Data: []byte("RIFFfake"), MIMEType: "audio/wav"},
		&mcpsdk.EmbeddedResource{Resource: &mcpsdk.ResourceContents{URI: "mem://out/report.pdf", MIMEType: "application/pdf", Blob: []byte("%PDF-1.4")}},
	}}, collector)
	if err != nil {
		t.Fatal(err)
	}
	ids := mediaIDsOf(t, output)
	if len(ids) != 2 {
		t.Fatalf("音频和资源都应暂存：%s", output)
	}
	audio, _ := LookupMCPMedia(ids[0])
	file, _ := LookupMCPMedia(ids[1])
	if audio.Kind != MCPMediaAudio || file.Kind != MCPMediaFile || file.Name != "report.pdf" {
		t.Fatalf("类型或文件名不对：audio=%+v file=%+v", audio, file)
	}
	// 音频不是图，不该附给模型。
	if parts := (&MCPTool{}).ToolResultParts(output); len(parts) != 0 {
		t.Fatalf("非图片被附给了模型：%+v", parts)
	}
}

// 很多服务把图存到磁盘、只回一句路径。这次调用新生成的媒体文件要取回来；早就在
// 磁盘上的不行，否则群成员让会回显输入的 MCP 念出路径，就能把旧文件要走。
func TestMCPTextPathOnlyPicksUpFreshMediaFiles(t *testing.T) {
	collector, dir := testMCPMediaCollector(t)
	fresh := filepath.Join(dir, "out", "chart.png")
	if err := os.MkdirAll(filepath.Dir(fresh), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fresh, []byte("\x89PNGfresh"), 0o600); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, "old.jpg")
	if err := os.WriteFile(stale, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "settings.yaml")
	if err := os.WriteFile(config, []byte("token: x"), 0o600); err != nil {
		t.Fatal(err)
	}
	text := "已保存到 " + fresh + "。相对路径 out/chart.png，旧图 " + stale + "，配置 " + config
	output, err := formatSDKMCPToolResult(&mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: text}}}, collector)
	if err != nil {
		t.Fatal(err)
	}
	ids := mediaIDsOf(t, output)
	if len(ids) != 1 {
		t.Fatalf("应只取回新生成的那张图（绝对和相对路径指向同一个文件）：%s", output)
	}
	media, _ := LookupMCPMedia(ids[0])
	if media.Name != "chart.png" || media.Kind != MCPMediaImage {
		t.Fatalf("取回的不对：%+v", media)
	}
}

func TestMCPTextPathIgnoredForRemoteServers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chart.png")
	if err := os.WriteFile(path, []byte("\x89PNG"), 0o600); err != nil {
		t.Fatal(err)
	}
	// HTTP 服务说的路径在远端机器上，本机同名文件不是它的输出。
	collector := &mcpMediaCollector{server: "remote", callStart: time.Now()}
	output, _ := formatSDKMCPToolResult(&mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "saved " + path}}}, collector)
	if ids := mediaIDsOf(t, output); len(ids) != 0 {
		t.Fatalf("远程服务的路径不该读本机文件：%s", output)
	}
}

func TestMCPResourceLinkFileIsKeptButNotExecutablesOrSecrets(t *testing.T) {
	collector, dir := testMCPMediaCollector(t)
	doc := filepath.Join(dir, "notes.txt")
	script := filepath.Join(dir, "run.sh")
	secret := filepath.Join(dir, "secret.json")
	for _, path := range []string{doc, script, secret} {
		if err := os.WriteFile(path, []byte("content"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(doc, old, old)
	collector.protected = protectedFiles{files: map[string]bool{secret: true}}
	output, err := formatSDKMCPToolResult(&mcpsdk.CallToolResult{Content: []mcpsdk.Content{
		// 服务明确交出来的文件不要求是新生成的。
		&mcpsdk.ResourceLink{URI: "file://" + doc, Name: "notes.txt"},
		&mcpsdk.ResourceLink{URI: "file://" + script, Name: "run.sh"},
		&mcpsdk.ResourceLink{URI: "file://" + secret, Name: "secret.json"},
		&mcpsdk.ResourceLink{URI: "https://example.com/cat.png", Name: "cat", MIMEType: "image/png"},
	}}, collector)
	if err != nil {
		t.Fatal(err)
	}
	ids := mediaIDsOf(t, output)
	if len(ids) != 1 {
		t.Fatalf("只有 notes.txt 应被取回：%s", output)
	}
	if media, _ := LookupMCPMedia(ids[0]); media.Name != "notes.txt" {
		t.Fatalf("取回的不对：%+v", media)
	}
	if !strings.Contains(output, "remote_image") {
		t.Fatalf("网上的图片应提示走 remote_image：%s", output)
	}
}

func TestMCPMediaExpires(t *testing.T) {
	now := time.Now()
	mcpMediaNow = func() time.Time { return now }
	defer func() { mcpMediaNow = time.Now }()
	id, err := StoreMCPMedia(MCPMedia{Kind: MCPMediaFile, Data: []byte("x")})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := LookupMCPMedia(id); !ok {
		t.Fatal("刚存的取不到")
	}
	now = now.Add(mcpMediaTTL + time.Second)
	if _, ok := LookupMCPMedia(id); ok {
		t.Fatal("过期了还能取到")
	}
}

// 走一遍真实的 tools/call，确认 CallTool 把结果交给了暂存，而不只是格式化函数单测里对。
func TestMCPClientCallToolKeepsImageContent(t *testing.T) {
	data := []byte("\x89PNG\r\n\x1a\nfrom-server")
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "painter", Version: "1"}, nil)
	server.AddTool(&mcpsdk.Tool{Name: "paint", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.ImageContent{Data: data, MIMEType: "image/png"}}}, nil
		})
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	ctx := context.Background()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	clientSession, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "diana", Version: "1"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &MCPClient{
		name:     "painter",
		redactor: newMCPRedactor(mcpServerConfig{}),
		current:  &mcpSession{session: clientSession, stderr: &lockedBuffer{}, done: make(chan struct{})},
	}
	defer client.Close()
	tool := &MCPTool{client: client, serverName: "painter", rawName: "paint", modelName: "mcp__painter__paint"}
	output, err := tool.Run(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	ids := mediaIDsOf(t, output)
	if len(ids) != 1 {
		t.Fatalf("结果里没有 media_id：%s", output)
	}
	if media, ok := LookupMCPMedia(ids[0]); !ok || !bytes.Equal(media.Data, data) || media.Server != "painter" {
		t.Fatalf("暂存的不是服务返回的图：%+v", media)
	}
	if parts := tool.ToolResultParts(output); len(parts) != 1 {
		t.Fatalf("图没有附给模型：%+v", parts)
	}
}
