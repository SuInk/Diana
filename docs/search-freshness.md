# 最新版本核验与搜索时效

`web_search.search` 每次都会调用配置的搜索提供商，Diana 没有缓存搜索结果。但搜索提供商的索引、正文快照和网页本身更新的时间可能不同；精确 `site:` URL 没有结果，也可能只是该 URL 尚未收录或查询约束过强，不能证明页面或版本不存在。

## 处理方式

- 精确 URL / `site:host/path` 搜索失败时，在原有查询与提供商预算内增加路径关键词候选，保留项目名及版本号，允许找到其他官方公告。
- 搜索输出提供 `retrieved_at`（本次查询完成时间）和 `freshness_verified: false`。查询时间不是索引时间，也不代表来源已被实时核验。成功返回链接只标记 `candidate_sources_found`，不标记“证据充分”。
- `browser_render` 读取 `https://github.com/<owner>/<repo>/releases` 或 `/releases/latest` 时，优先请求公开 GitHub `/repos/<owner>/<repo>/releases/latest` API；读取 `/releases/tag/<tag>` 时请求对应标签 API。带分页/筛选查询参数或其他站点仍走浏览器。
- API 结果标明来源、版本、`published_at` 和查询时间。latest 摘要仅覆盖 GitHub 指定的最新正式发布，不声称包含全部历史或预发布版本；指定标签的存在也不能证明它是最新。
- API 访问不使用任何仓库凭据，遵守公网地址及重定向检查，有独立超时、响应大小和总调用预算。失败时回退 Chromium；403、404、429、网络超时均不转成“版本不存在”。两条路径都失败时保留真实错误。
- 原有证据账本接受本次实际读取的官方 API 地址及对应 Release 页面作为来源；结果仍属于外部不可信内容，不能作为操作指令。

## 2026-09-18 实测

使用 Diana 默认 Exa MCP 搜索入口和项目内工具实现，未使用搜索提供商密钥：

| 操作 | 结果 |
| --- | --- |
| 修复前精确搜索 `site:github.com/go-gitea/gitea/releases/tag/v1.27.3`，单次候选 | 约 3.0 秒，`no_results` |
| 修复前较宽查询 `Gitea latest release 1.27.3` | 约 4.6 秒，找到官方 1.27.3 公告 |
| 修复后相同精确搜索 | 约 5.5 秒，自动放宽为 `go-gitea gitea releases tag v1.27.3`，找到官方公告及标签页面 |
| 修复后读取 Gitea Release 首页 | 约 2.4 秒，官方 API 返回 `v1.27.3`，发布时间 `2026-08-29T17:42:17Z` |
| 修复后读取指定 `v1.27.3` 标签 | 约 1.3 秒，核验成功；测试刻意指定不存在的浏览器路径，证明不依赖浏览器启动 |
| Docker 内修复后同一精确查询 | 约 8.7 秒，自动放宽并找到官方公告及标签页面 |
| Docker 内官方 API 核验 | 首页约 1.8 秒、指定标签约 0.7 秒，均核验到 1.27.3 与正确发布日期 |
| 修复前当前 Chromium 读取同一 Release 首页 | 约 15 秒，成功，正文包含 1.27.3 |

这些是单次运行结果，不是耗时保证。此次没有复现 Chromium 被 GitHub 拦截，不能仅凭群聊中的失败转述确定原来的失败原因。已复现的确定问题是精确搜索空结果与较宽查询结果不一致，以及旧流程容易把候选来源误当作最新事实。

官方核验地址：

- [Gitea 最新正式发布 API](https://api.github.com/repos/go-gitea/gitea/releases/latest)
- [Gitea v1.27.3 发布记录](https://github.com/go-gitea/gitea/releases/tag/v1.27.3)
- [Gitea 1.27.3 官方公告](https://blog.gitea.com/release-of-1.27.3/)
