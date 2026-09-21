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

# 两个运行时变体共用的轻量基础。
# ca-certificates 供 Go 进程访问 HTTPS API；fontconfig 是渲染出图的基础；
# nodejs/npm 供插件/工具链运行；bubblewrap 是 Agent 执行本地命令时的沙盒——
# 装了它不代表一定能用：容器默认的 seccomp 或 AppArmor 策略常常禁掉非特权
# 用户命名空间，运行时会实际试跑一次再决定用不用；但不装则连试的机会都没有，
# 命令只能以主进程权限裸跑。
FROM alpine:3.22 AS runtime-base
WORKDIR /app
# data/logs 预建并交给运行用户，容器不挂卷也能直接跑（SQLite 与日志有处可写）。
# git/libgcc/libstdc++ 供 WebUI 安装的编码 CLI（Codex 等）运行；运行用户的 home
# 放在数据目录下，挂卷后 CLI 的设备登录态能跨容器重建保留。
RUN apk add --no-cache ca-certificates fontconfig nodejs npm git libgcc libstdc++ bubblewrap \
    && adduser -D -H -h /app/data/home -u 10001 diana \
    && mkdir -p /app/data/home /app/logs \
    && chown -R diana:diana /app/data /app/logs
# gitea-mcp 放在主程序旁边，MCP 预设按这个位置拉起它；许可证随二进制一起带上。
COPY --from=gitea-mcp /out/gitea-mcp /app/gitea-mcp
COPY --from=gitea-mcp /out/gitea-mcp.LICENSE /app/gitea-mcp.LICENSE

# 完整版运行时：预装 Chromium（网页读取/截图）、Noto CJK 字体、ffmpeg、
# yt-dlp 与 tesseract 及中英语言包（图片文字识别插件的本地离线后端）。
FROM runtime-base AS runtime-full
RUN apk add --no-cache chromium font-noto-cjk ffmpeg yt-dlp tesseract-ocr tesseract-ocr-data-chi_sim tesseract-ocr-data-eng \
    # 容器里只用 --headless=new 做无头截图/读网页，软件渲染由 chromium 自带实现，
    # 用不到 mesa 桌面 GPU 栈。apk 依赖会把它整包拉进来（libLLVM 153M + gallium 92M），
    # 这里直接删掉其中最大的文件；chromium 主程序不链接它们，已实测截图/PDF 正常。
    # 注意不能删 libpipewire——chromium 主程序直接链接它。
    && rm -f /usr/lib/libLLVM.so.* /usr/lib/libgallium-*.so \
    && rm -rf /usr/lib/gallium-pipe

# slim 版运行时：不预装浏览器/字体/媒体/OCR 栈，体积约为完整版的五分之一。
# 后续想用网页渲染，在宿主机以 root 执行：
#   docker exec -u root <容器名> apk add --no-cache chromium font-noto-cjk
# WebUI 里的一键安装会因进程非 root 失败，报错会附上同样的命令；无包管理器的
# glibc Linux 裸机部署则会自动改用 Chrome for Testing 无 root 下载。
FROM runtime-base AS runtime-slim
COPY --from=backend /out/diana-webui /app/diana-webui
COPY --from=frontend-next /src/frontend-next/dist /app/frontend-next/dist
ENV DIANA_CONFIG=/app/config.yaml
EXPOSE 18080
USER diana
ENTRYPOINT ["/app/diana-webui"]

FROM runtime-full AS runtime
COPY --from=backend /out/diana-webui /app/diana-webui
COPY --from=frontend-next /src/frontend-next/dist /app/frontend-next/dist
# 应用配置走 config.yaml；镜像内只放一份内置默认配置，挂载同名文件即可覆盖。
ENV DIANA_CONFIG=/app/config.yaml
EXPOSE 18080
USER diana
ENTRYPOINT ["/app/diana-webui"]
