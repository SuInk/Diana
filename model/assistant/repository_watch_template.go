// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "strings"

// 仓库订阅的推送格式此前是硬编码的，每次调排版都要改代码发版。这里把每类动态的
// 条目排版开放成插件设置里的模板；聚合行为（共同作者去重、截断、Star 名单）仍在
// 代码里，模板只负责一条动态长什么样。
const (
	repositoryWatchSettingTemplateHeader  = "template_header"
	repositoryWatchSettingTemplateCommit  = "template_commit"
	repositoryWatchSettingTemplatePull    = "template_pull"
	repositoryWatchSettingTemplateIssue   = "template_issue"
	repositoryWatchSettingTemplateRelease = "template_release"
)

// 默认模板与既有硬编码输出逐字一致，留空即维持现状。
const (
	repositoryWatchDefaultHeaderTemplate  = "GitHub 动态：{repository}\n{summary}\n{body}"
	repositoryWatchDefaultCommitTemplate  = "{sha} {title}\n{author} 提交于 {time}\n{url}"
	repositoryWatchDefaultPullTemplate    = "PR #{number}（{status}）\n{title}\n作者：{author}\n{branches}\n{time_label} {time}\n{url}"
	repositoryWatchDefaultIssueTemplate   = "Issue #{number}（{status}）\n{title}\n作者：{author}\n{time_label} {time}\n{url}"
	repositoryWatchDefaultReleaseTemplate = "Release {label}\n发布于 {time}\n{url}"
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
	pick := func(key, fallback string) string {
		if value := strings.TrimSpace(settings.String(key, "")); value != "" {
			return value
		}
		return fallback
	}
	return repositoryWatchTemplates{
		Header:  pick(repositoryWatchSettingTemplateHeader, repositoryWatchDefaultHeaderTemplate),
		Commit:  pick(repositoryWatchSettingTemplateCommit, repositoryWatchDefaultCommitTemplate),
		Pull:    pick(repositoryWatchSettingTemplatePull, repositoryWatchDefaultPullTemplate),
		Issue:   pick(repositoryWatchSettingTemplateIssue, repositoryWatchDefaultIssueTemplate),
		Release: pick(repositoryWatchSettingTemplateRelease, repositoryWatchDefaultReleaseTemplate),
	}
}

func defaultRepositoryWatchTemplates() repositoryWatchTemplates {
	return repositoryWatchTemplatesFromSettings(nil)
}
