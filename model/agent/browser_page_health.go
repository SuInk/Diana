// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// 页面卡死和渲染进程崩溃的收尾。
//
// 两种情况下 Runtime.evaluate 都永远不回：页面主线程被脚本占住（死循环、极重的
// 同步计算），或者渲染进程崩了（容器里 /dev/shm 只有 64MB 时，重页面很容易崩）。
// 以前工具就一直等到 Runner 的 60 秒上限，模型拿到的是「工具执行超时」，标签页也
// 留在后台卡着，下一次调用接着卡。现在每次调用最多等一个浏览器超时，撞上了就先把
// 标签页救回来，再把原因和处理结果交给模型。

const (
	// browserRecoverTimeout 是救一个标签页总共花的时间上限，独立于调用方已经用完的 ctx。
	browserRecoverTimeout = 8 * time.Second
	// browserProbeWait 是恢复时每一步（停止加载、换空白页、确认页面能回话）等多久。
	// 这些命令正常情况下几毫秒就回。
	browserProbeWait = 2 * time.Second
	// terminateWait 是等 Runtime.terminateExecution 回话的时间。
	terminateWait = time.Second
)

// browserPageError 是页面卡死或崩溃。给模型看的是原因和已经做了什么；卡死的情况
// errors.Is 仍认得出 context.DeadlineExceeded，和其他浏览器超时一个口径。
type browserPageError struct {
	crashed bool
	timeout time.Duration
	// subject 是出事时在做什么，例如「打开 https://a.example/ 后」。
	subject string
	// outcome 是恢复之后标签页的状态。
	outcome string
}

func (e *browserPageError) Error() string {
	reason := fmt.Sprintf("页面 %s 内没有响应（脚本卡住或页面崩溃）", e.timeout)
	if e.crashed {
		reason = "页面崩溃了（渲染进程退出，常见于页面太重、内存或 /dev/shm 不够）"
	}
	message := e.subject + reason
	if e.outcome != "" {
		message += "，" + e.outcome
	}
	return message
}

func (e *browserPageError) Unwrap() error {
	if e.crashed {
		return nil
	}
	return context.DeadlineExceeded
}

// describe 给同一个故障配上情境和处理结果，不改会话里记着的那一份。
func (e *browserPageError) describe(subject, outcome string) *browserPageError {
	copied := *e
	copied.subject = subject
	copied.outcome = outcome
	return &copied
}

// pageRecovery 是撞上卡死或崩溃之后怎么处置这个标签页。
type pageRecovery int

const (
	// recoverKeep：先打断卡住的脚本，页面能回话就留着（主人的页面、填了一半的表单
	// 不该因为一次卡顿就没了）；救不回来再换回空白页。
	recoverKeep pageRecovery = iota
	// recoverBlank：打开失败的那一页不留，停止加载后换回空白页。
	recoverBlank
	// recoverDiscard：关掉。给刚为这次调用新开的标签页用。
	recoverDiscard
)

// release 在工具返回前收尾：这次调用撞上了卡死或崩溃的页面，就先把标签页救回来，
// 再把原因和处理结果写进错误；其他情况只关连接。
func (b browserToolBase) release(client *cdpClient, errp *error) {
	b.releaseWith(client, errp, recoverKeep, "")
}

func (b browserToolBase) releaseWith(client *cdpClient, errp *error, mode pageRecovery, subject string) {
	if client == nil {
		return
	}
	defer client.Close()
	failure := client.pageFailure()
	if failure == nil {
		return
	}
	outcome := b.recoverPage(client, mode)
	// 就算工具自己吞掉了中途的错误（比如 pageState 读不到地址），页面已经出过事，
	// 也要让模型知道：它拿到的结果可能不完整，标签页也可能已经换回空白页。
	if errp != nil {
		*errp = failure.describe(subject, outcome)
	}
}

// recoverPage 把卡死或崩溃的标签页救回可用状态，返回给模型看的处理结果。
func (b browserToolBase) recoverPage(client *cdpClient, mode pageRecovery) string {
	failure := client.pageFailure()
	ctx, cancel := context.WithTimeout(context.Background(), browserRecoverTimeout)
	defer cancel()
	// 要留住页面时先试着打断卡住的脚本。Runtime.terminateExecution 走 V8 的中断，
	// 死循环里也打得断，正常几毫秒就回；但只有页面卡住之前就挂上的会话发得进去，
	// 新连上的会话挂不到卡住的渲染进程上，所以只等一小会儿。关页、换空白页都由
	// 浏览器进程处理，卡住的渲染进程拦不住，用不着先打断。
	if mode == recoverKeep && failure != nil && !failure.crashed {
		probe, probeCancel := context.WithTimeout(ctx, terminateWait)
		_, err := client.roundTrip(probe, "Runtime.terminateExecution", nil)
		probeCancel()
		if err == nil && pageResponds(ctx, client.wsURL) {
			return "已打断页面上卡住的脚本，这个标签页还能继续用"
		}
	}
	if mode == recoverDiscard {
		b.discardTab(client.baseURL, client.targetID)
		waitTabGone(ctx, client.baseURL, client.targetID)
		return "已关闭这个新开的标签页"
	}
	// 导航回空白页由浏览器进程处理，卡死或崩掉的渲染进程拦不住它；崩溃的标签页也靠
	// 这一步换一个新的渲染进程。
	if resetTabToBlank(ctx, client.wsURL) {
		return "已停止加载并把这个标签页换回空白页"
	}
	if b.tabRegistry().isOpened(client.targetID) {
		b.discardTab(client.baseURL, client.targetID)
		waitTabGone(ctx, client.baseURL, client.targetID)
		return "这个标签页救不回来，已关闭"
	}
	// 不是机器人开的页不替主人关，只是不再把它当成这个对话的当前页。
	if b.session.active() == client.targetID {
		b.session.setActive("")
	}
	return "这个标签页救不回来，用 browser_tabs 的 new 另开一页"
}

// pageResponds 另开一条会话确认页面能回话。卡住的渲染进程连新会话都挂不上，
// 这里就会等到超时。
func pageResponds(ctx context.Context, wsURL string) bool {
	probe, cancel := context.WithTimeout(ctx, browserProbeWait)
	defer cancel()
	client, err := newCDPClient(probe, wsURL, browserProbeWait)
	if err != nil {
		return false
	}
	defer client.Close()
	raw, err := client.roundTrip(probe, "Runtime.evaluate", map[string]any{"expression": "1", "returnByValue": true})
	if err != nil {
		return false
	}
	var out struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}
	return json.Unmarshal(raw, &out) == nil && string(out.Result.Value) == "1"
}

// resetTabToBlank 停止加载并把标签页导航回空白页，成功后确认新页面能回话。
func resetTabToBlank(ctx context.Context, wsURL string) bool {
	if wsURL == "" {
		return false
	}
	client, err := newCDPClient(ctx, wsURL, browserProbeWait)
	if err != nil {
		return false
	}
	step := func(method string, params map[string]any) error {
		stepCtx, cancel := context.WithTimeout(ctx, browserProbeWait)
		defer cancel()
		_, err := client.roundTrip(stepCtx, method, params)
		return err
	}
	_ = step("Page.stopLoading", nil)
	err = step("Page.navigate", map[string]any{"url": "about:blank"})
	client.Close()
	return err == nil && pageResponds(ctx, wsURL)
}

// waitTabGone 等关掉的标签页从列表里消失。/json/close 只是发起关闭，卡过的页面要
// 过一小会儿才真的没了；不等的话紧接着的一次新开会把它算进机器人的标签页名额。
func waitTabGone(ctx context.Context, baseURL, targetID string) {
	for {
		targets, err := listBrowserTargets(ctx, baseURL)
		if err != nil {
			return
		}
		gone := true
		for _, target := range targets {
			if target.ID == targetID {
				gone = false
				break
			}
		}
		if gone {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}
