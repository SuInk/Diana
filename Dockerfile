# syntax=docker/dockerfile:1

# Copyright (c) 2025-now SuInk.
# Licensed under the Limited Redistribution License in the repository root.

FROM --platform=$BUILDPLATFORM node:22-alpine AS frontend-next
WORKDIR /src/frontend-next
COPY frontend-next/package*.json ./
RUN npm ci
COPY frontend-next/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.26.6-alpine AS backend
ARG BUILD_VERSION=dev
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w -X main.buildVersion=${BUILD_VERSION}" -o /out/diana-webui ./cmd/webui

# 随包发布的 gitea-mcp：只收 Gitea 官方的发布产物，版本和 SHA-256 钉在脚本里，
# 对不上就在这里构建失败，不会把一个没核对过的二进制发出去。拉取的是目标平台的
# 产物，构建机自己跑不跑得动它无所谓，所以停在 BUILDPLATFORM 上。
FROM --platform=$BUILDPLATFORM alpine:3.22 AS gitea-mcp
ARG TARGETOS
ARG TARGETARCH
RUN apk add --no-cache bash curl tar
COPY scripts/fetch-gitea-mcp.sh /usr/local/bin/fetch-gitea-mcp.sh
RUN /usr/local/bin/fetch-gitea-mcp.sh "${TARGETOS}" "${TARGETARCH}" /out

# 随镜像发布的 yt-dlp：同样只收官方产物并校验 SHA-256。不用发行版的包是因为
# 它必须新——Debian stable 把它冻在发布那天的版本，视频站解析会成片失效。
FROM --platform=$BUILDPLATFORM debian:bookworm-slim AS yt-dlp
ARG TARGETOS
ARG TARGETARCH
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
COPY scripts/fetch-yt-dlp.sh /usr/local/bin/fetch-yt-dlp.sh
RUN /usr/local/bin/fetch-yt-dlp.sh "${TARGETOS}" "${TARGETARCH}" /out

# 两个运行时变体共用的基础。
#
# 底座是 Debian 12（bookworm）而不是 Alpine，换过来是为了 glibc：WebUI 会在容器里
# 装编码 CLI 和 npm 包，musl 下预编译产物和原生模块经常要自己编、甚至跑不起来；
# glibc 还让「无包管理器时改用 Chrome for Testing 下载」那条路在容器里也成立。
#
# 具体用 node 官方镜像而不是 debian:bookworm-slim：Node 由官方镜像钉版本（Debian
# stable 自带的是已经 EOL 的 18），npm 也一并带好，省掉自己加第三方源。剩下那些
# 跟着上游节奏走的工具同样不用发行版的包：yt-dlp 在上面的构建阶段按版本+SHA-256 取
# 官方产物。Chromium 则用 Debian 的——安全团队一直在回填新版本（bookworm 上就是 153）。
#
# ca-certificates 供 Go 进程访问 HTTPS API；fontconfig 是渲染出图的基础；
# bubblewrap 是 Agent 执行本地命令时的沙盒——装了它不代表一定能用：容器默认的
# seccomp 或 AppArmor 策略常常禁掉非特权用户命名空间，运行时会实际试跑一次再决定
# 用不用；但不装则连试的机会都没有，命令只能以主进程权限裸跑。
# tini 做 PID 1：Chromium 的子进程退出后会过继给 PID 1，diana-webui 不负责回收它们，
# 没有 init 时每次网页渲染都留下一串僵尸进程。-s 让它在外面又套了一层 init
# （docker run --init）时也以 subreaper 身份照常回收。
FROM node:24-bookworm-slim AS runtime-base
WORKDIR /app
# data/logs 预建并交给运行用户，容器不挂卷也能直接跑（SQLite 与日志有处可写）。
# git 供 WebUI 安装的编码 CLI（Codex 等）运行；运行用户的 home 放在数据目录下，
# 挂卷后 CLI 的设备登录态能跨容器重建保留。
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates fontconfig git bubblewrap tini \
    && rm -rf /var/lib/apt/lists/* \
    && useradd -M -d /app/data/home -u 10001 diana \
    && mkdir -p /app/data/home /app/logs \
    && chown -R diana:diana /app/data /app/logs
# gitea-mcp 放在主程序旁边，MCP 预设按这个位置拉起它；许可证随二进制一起带上。
COPY --from=gitea-mcp /out/gitea-mcp /app/gitea-mcp
COPY --from=gitea-mcp /out/gitea-mcp.LICENSE /app/gitea-mcp.LICENSE
# 入口以 root 起步，把挂进来的 data/logs 交给 diana 后再降权启动主程序。
# 不能直接 USER diana：Linux 上 bind mount 自动建出来的宿主机目录归 root，
# diana 写不进去，SQLite 和日志都起不来。
COPY --chmod=0755 scripts/docker/entrypoint.sh /usr/local/bin/diana-entrypoint
# DIANA_DEPLOYMENT 让控制台按 Docker 部署处理更新：只提示新版本，不在容器里下载和
# 替换程序（/app 只读，重建容器也会丢），升级靠拉新镜像。DIANA_LOG_PATH 在
# config.yaml 没写 storage.log_path 时把日志写进挂出来的 /app/logs。
ENV DIANA_DEPLOYMENT=docker \
    DIANA_LOG_PATH=/app/logs/diana.log

# 完整版运行时：预装 Chromium（网页读取/截图）、Noto CJK 字体、ffmpeg、
# yt-dlp 与 tesseract 及中英语言包（图片文字识别插件的本地离线后端）。
#
# xvfb 是内置浏览器「有头」那一档的底座：容器里没有显示器，有头的 Chromium 要一块屏
# 才开得起窗口，Diana 打开有头时自己拉一块（Selenium Grid、Playwright 官方镜像和
# Steel 的容器都是这个形状）。看画面仍然走 CDP screencast，所以不需要 VNC/noVNC。
FROM runtime-base AS runtime-full
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        chromium fonts-noto-cjk ffmpeg xvfb \
        tesseract-ocr tesseract-ocr-chi-sim tesseract-ocr-eng \
    && rm -rf /var/lib/apt/lists/*
COPY --from=yt-dlp /out/yt-dlp /usr/local/bin/yt-dlp

# slim 版运行时：不预装浏览器/字体/媒体/OCR 栈，体积约为完整版的四分之一。
# 后续想用网页渲染，在宿主机以 root 执行：
#   docker exec -u root <容器名> sh -c 'apt-get update && apt-get install -y chromium fonts-noto-cjk'
# WebUI 里的一键安装会因进程非 root 失败，报错会附上同样的命令；无包管理器的
# glibc Linux 裸机部署则会自动改用 Chrome for Testing 无 root 下载。
FROM runtime-base AS runtime-slim
COPY --from=backend /out/diana-webui /app/diana-webui
COPY --from=frontend-next /src/frontend-next/dist /app/frontend-next/dist
# 浏览器控制扩展的源码。容器用户没有仓库检出，WebUI 的「下载扩展」就是从这里打包。
COPY packaging/browser-control-extension /app/browser-control-extension
ENV DIANA_CONFIG=/app/config.yaml
EXPOSE 18080
ENTRYPOINT ["/usr/bin/tini", "-s", "--", "/usr/local/bin/diana-entrypoint", "/app/diana-webui"]

FROM runtime-full AS runtime
COPY --from=backend /out/diana-webui /app/diana-webui
COPY --from=frontend-next /src/frontend-next/dist /app/frontend-next/dist
COPY packaging/browser-control-extension /app/browser-control-extension
# 应用配置走 config.yaml；镜像内只放一份内置默认配置，挂载同名文件即可覆盖。
ENV DIANA_CONFIG=/app/config.yaml
EXPOSE 18080
ENTRYPOINT ["/usr/bin/tini", "-s", "--", "/usr/local/bin/diana-entrypoint", "/app/diana-webui"]
