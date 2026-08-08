package assistant

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ResolverDependency 描述解析器依赖的一个外部命令。
type ResolverDependency struct {
	Name string `json:"name"`
	// Purpose 说明缺了它会失去什么能力，便于用户判断要不要装。
	Purpose   string `json:"purpose"`
	Available bool   `json:"available"`
	Path      string `json:"path,omitempty"`
	Version   string `json:"version,omitempty"`
}

// resolverDependencySpecs 是需要探测的外部命令。这些在 Docker 镜像里由
// apk 安装，裸二进制部署则要用户自己装，缺了会让对应平台整体不可用。
var resolverDependencySpecs = []struct {
	name        string
	purpose     string
	versionArgs []string
}{
	{"yt-dlp", "YouTube / X 等平台的视频下载", []string{"--version"}},
	{"ffmpeg", "B 站音视频分离流的合并", []string{"-version"}},
	{"node", "抖音接口签名（a-bogus）", []string{"--version"}},
}

var (
	resolverDepsOnce  sync.Once
	resolverDepsCache []ResolverDependency
)

// ResolverDependencies 返回外部依赖的探测结果。
//
// 原先这些依赖是「用到才 LookPath」，缺失只在用户发了链接之后以聊天报错的
// 形式暴露；Docker 部署里依赖是否齐全直接决定插件能不能用，应该在控制台
// 一眼可见，而不是让用户发个链接去试。
//
// 结果缓存到进程结束：容器运行期间不会有人偷偷装上 ffmpeg，重启即重新探测。
func ResolverDependencies() []ResolverDependency {
	resolverDepsOnce.Do(func() {
		resolverDepsCache = probeResolverDependencies()
	})
	out := make([]ResolverDependency, len(resolverDepsCache))
	copy(out, resolverDepsCache)
	return out
}

func probeResolverDependencies() []ResolverDependency {
	out := make([]ResolverDependency, 0, len(resolverDependencySpecs))
	for _, spec := range resolverDependencySpecs {
		dep := ResolverDependency{Name: spec.name, Purpose: spec.purpose}
		path, err := exec.LookPath(spec.name)
		if err == nil {
			dep.Available = true
			dep.Path = path
			dep.Version = probeCommandVersion(spec.name, spec.versionArgs)
		}
		out = append(out, dep)
	}
	return out
}

// probeCommandVersion 取版本号首行，拿不到就留空——版本只是给人看的补充信息，
// 探测失败不该影响可用性判断。
func probeCommandVersion(name string, args []string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(string(output)), "\n", 2)[0])
	if len(line) > 120 {
		line = line[:120]
	}
	return line
}
