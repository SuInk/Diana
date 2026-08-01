# frontend-next — 新版 WebUI

Diana 控制台的组件化重构版本，与 `frontend/` 并存，可随时切换。

## 相比旧版的变化

- 组件化结构：视图拆分为 `src/views/`，共享组件在 `src/components/`，不再是单个 5000 行的 App.vue。
- 新增「总览 Dashboard」：连接链路检查清单、今日/累计消息统计、24 小时消息量走势、实时事件流。
- 实时推送：通过 `GET /api/events`（SSE）接收状态、统计和事件，替代轮询。
- 「配置向导」：三步引导（LLM → NapCat → 验证），首次访问且未配置 LLM 时自动进入。
- 移动端适配：侧栏在窄屏折叠为抽屉，表单和统计卡自适应。
- 主题：浅色 / 深色 / 跟随系统 + 4 种主题色，顶栏一键循环切换。

## 开发

```sh
cd frontend-next
npm install        # 首次
npm run dev        # http://127.0.0.1:5174，/api 和 /onebot 代理到 127.0.0.1:18080
```

或在仓库根目录：

```sh
make deps-next
make backend        # 终端 1：Go 后端
make frontend-next  # 终端 2：Vite 前端
```

## 生产构建与切换

```sh
cd frontend-next && npm run build && cd ..
FRONTEND_DIST=frontend-next/dist ./dist/diana-webui
```

或：

```sh
make run-next
```

不设置 `FRONTEND_DIST` 时后端仍伺服旧版 `frontend/dist`，两版互不影响。

## 依赖的新后端接口

| 接口 | 用途 |
| --- | --- |
| `GET /api/stats` | Dashboard 统计（进程内内存计数，重启清零） |
| `GET /api/events` | SSE：`status` / `stats` / `bot_event` 三类事件 |
| `GET /api/health` | 版本与运行时长 |

旧接口全部沿用，见 `src/api.ts`。
