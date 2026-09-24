// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	_ = os.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	// Agent 的工作目录跟着数据库位置走。测试里把它指到临时目录，免得用例往
	// 开发机的真实缓存目录写文件。
	workspaceRoot, err := os.MkdirTemp("", "diana-assistant-test-*")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("APP_DB_PATH", filepath.Join(workspaceRoot, "app.db"))
	// 分词词典是后台异步加载的,不等它就绪的话,选词结果会随测试时序漂移:
	// 同一条用例可能这次用上词典词、下次只有 n-gram。统一开启并等到就绪,
	// 测试面对的就是开着词典分词的线上稳态。
	applyCJKSegmentConfig(BotConfig{DictSegmentEnabled: boolPointer(true)})
	if !awaitCJKSegmenter() {
		// 加载失败时选词会静默退回 n-gram,同一段中文被切成不同的词,依赖检索
		// 打分的用例就会莫名其妙地翻脸(例如摘要排到情景后面)。以前这里忽略返回值,
		// 于是「词典没装上」表现成一条排序断言失败,查起来完全不着边。
		fmt.Fprintln(os.Stderr, "分词词典加载失败,检索相关用例的结果不可信:", cjkSegmentErr)
		_ = os.RemoveAll(workspaceRoot)
		os.Exit(1)
	}
	code := m.Run()
	if code == 0 {
		code = checkPackageLeftovers()
	}
	_ = os.RemoveAll(workspaceRoot)
	os.Exit(code)
}

// checkPackageLeftovers 在整包跑完后检查有没有用例把东西留在了共享状态里。
//
// #709、#711 都是同一类问题：某个用例派出去的后台协程活得比用例长，把编码任务
// 记录写进整包共享的工作区根目录，下一个启动 Runtime 的用例把它当遗留任务捡走，
// 用自己的 channel 汇报出去，表现成一条与编码毫无关系的用例「多发了一条」。这种
// 失败只在特定的执行顺序下出现，追查代价很高；在这里把「留下了东西」本身变成
// 确定的失败，并指出留下的是什么。
func checkPackageLeftovers() int {
	code := 0
	if jobs := listCodingJobs(); len(jobs) > 0 {
		fmt.Fprintln(os.Stderr, "测试结束后共享工作区里还留着编码任务记录；用到编码任务的用例要先调 useTempCodingWorkspace，并在结束前收干净看护协程（见 codingTestRuntime）：")
		for _, job := range jobs {
			fmt.Fprintf(os.Stderr, "  id=%s status=%s workspace=%s instruction=%q\n", job.ID, job.Status, job.Workspace, job.Instruction)
		}
		code = 1
	}
	if stacks := lingeringPackageGoroutines(10 * time.Second); len(stacks) > 0 {
		fmt.Fprintf(os.Stderr, "测试结束 10 秒后仍有 %d 个本包的协程没有退出。启动 Runtime、服务器或后台循环的用例要在 t.Cleanup 里停掉并等它退出：\n\n%s\n", len(stacks), strings.Join(stacks, "\n\n"))
		code = 1
	}
	return code
}

// lingeringPackageGoroutineAllowlist 是允许活过整包的协程：它们按设计就是延时
// 执行、到点自己退出，不读写共享状态。
var lingeringPackageGoroutineAllowlist = []string{
	// 解析出的本地媒体发出去之后延时删除，延时按分钟计；只删自己登记的绝对路径。
	"model/assistant.cleanupLocalMediaFilesLater",
}

// lingeringPackageGoroutines 等到 grace 为止，返回仍在运行、栈里带本包代码的协程。
// 不引入 goleak：只看本包的栈帧就够判断「用例漏了东西」，标准库和第三方包自己的
// 常驻协程（HTTP 连接池、数据库驱动）不在考察范围里。
func lingeringPackageGoroutines(grace time.Duration) []string {
	deadline := time.Now().Add(grace)
	for {
		stacks := packageGoroutineStacks()
		if len(stacks) == 0 || time.Now().After(deadline) {
			return stacks
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func packageGoroutineStacks() []string {
	buf := make([]byte, 1<<20)
	for {
		n := goruntime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	var lingering []string
	for _, stack := range strings.Split(string(buf), "\n\n") {
		if !strings.Contains(stack, "diana/model/assistant.") {
			continue
		}
		// 当前协程就是 TestMain 自己。
		if strings.Contains(stack, "model/assistant.TestMain") {
			continue
		}
		allowed := false
		for _, marker := range lingeringPackageGoroutineAllowlist {
			if strings.Contains(stack, marker) {
				allowed = true
				break
			}
		}
		if !allowed {
			lingering = append(lingering, stack)
		}
	}
	return lingering
}
