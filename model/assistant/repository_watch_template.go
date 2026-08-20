// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "strings"

// 仓库订阅只开放一个模板：整条通知怎么组装。五类动态的条目排版已经统一成同一个
// 形状，再给每类各开一个模板，只会让设置页排出五个大文本框、还容易被改回互不一致
// 的样子——那正是这次要修掉的问题。条目排版和聚合行为（共同作者去重、截断、Star
// 名单）都留在代码里。
const repositoryWatchSettingTemplateHeader = "template_header"

// 默认模板：五类动态统一压成两行——第一行「类型 + 标识 + 标题」，第二行「谁于何时
// 做了什么 · 链接」。每条都以类型词开头，扫一眼就知道这条是提交还是 PR。此前 Commit 是紧凑两行，PR/Issue/Release 还留着四到六行的老排版，
// 同一条通知里时间格式、作者写法和链接位置各不相同，读起来像是三个人拼的。留空
// 即使用这里的默认值。
const (
	// 概括排在事实清单后面，并用 <botbr> 单独发一条：黏在最后一行链接后面既难读，
	// 也分不清哪些是确定的事实、哪句是模型写的。
	repositoryWatchDefaultHeaderTemplate  = "GitHub 动态：{repository}\n{body}\n<botbr>\n{summary}"
	repositoryWatchDefaultCommitTemplate  = "Commit {sha} {title}\n{byline} · {short_url}"
	repositoryWatchDefaultPullTemplate    = "PR #{number}（{status}）{title}\n{byline} · {branches} · {url}"
	repositoryWatchDefaultIssueTemplate   = "Issue #{number}（{status}）{title}\n{byline} · {url}"
	repositoryWatchDefaultReleaseTemplate = "Release {label}\n{byline} · {url}"
)

// renderRepositoryWatchTemplate 逐行替换占位符。某一行里出现过占位符、而全部占位
// 符都替换成了空串时，这一行连同它的静态前缀一起删除——作者缺失时不能留下一行
// 光秃秃的「作者：」。没有占位符的静态行原样保留。
func renderRepositoryWatchTemplate(template string, values map[string]string) string {
	lines := strings.Split(template, "\n")
	rendered := make([]string, 0, len(lines))
	for _, line := range lines {
		replaced := line
		sawPlaceholder := false
		sawValue := false
		for key, value := range values {
			placeholder := "{" + key + "}"
			if !strings.Contains(replaced, placeholder) {
				continue
			}
			sawPlaceholder = true
			if strings.TrimSpace(value) != "" {
				sawValue = true
			}
			replaced = strings.ReplaceAll(replaced, placeholder, value)
		}
		if sawPlaceholder && !sawValue {
			continue
		}
		// 一行里只有部分占位符为空时，静态分隔符会裸露在行首行尾——「{byline} ·
		// {short_url}」缺链接就会留下一个孤零零的「·」。把两端的分隔符连同空白一起
		// 剪掉。
		if sawPlaceholder {
			// 中间的占位符为空时会留下两个挨着的分隔符（「A ·  · B」），并回一个。
			for strings.Contains(replaced, " ·  · ") {
				replaced = strings.ReplaceAll(replaced, " ·  · ", " · ")
			}
			replaced = strings.Trim(replaced, " \t·|-—、,，")
		}
		if replaced = strings.TrimSpace(replaced); replaced == "" && sawPlaceholder {
			continue
		}
		rendered = append(rendered, replaced)
	}
	return strings.Join(rendered, "\n")
}

// repositoryWatchTemplates 收集生效的模板集合；设置缺失或留空时逐项回落默认值。
type repositoryWatchTemplates struct {
	Header  string
	Commit  string
	Pull    string
	Issue   string
	Release string
}

func repositoryWatchTemplatesFromSettings(settings SettingValues) repositoryWatchTemplates {
	header := strings.TrimSpace(settings.String(repositoryWatchSettingTemplateHeader, ""))
	if header == "" {
		header = repositoryWatchDefaultHeaderTemplate
	}
	return repositoryWatchTemplates{
		Header:  header,
		Commit:  repositoryWatchDefaultCommitTemplate,
		Pull:    repositoryWatchDefaultPullTemplate,
		Issue:   repositoryWatchDefaultIssueTemplate,
		Release: repositoryWatchDefaultReleaseTemplate,
	}
}

func defaultRepositoryWatchTemplates() repositoryWatchTemplates {
	return repositoryWatchTemplatesFromSettings(nil)
}
