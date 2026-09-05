# syntax=docker/dockerfile:1

# Copyright (c) 2025-now SuInk.
# Licensed under the Limited Redistribution License in the repository root.

FROM node:22-alpine AS frontend-next
WORKDIR /src/frontend-next
COPY frontend-next/package*.json ./
RUN npm ci
COPY frontend-next/ ./
RUN npm run build

FROM golang:1.26.6-alpine AS backend
ARG BUILD_VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.buildVersion=${BUILD_VERSION}" -o /out/diana-webui ./cmd/webui

FROM alpine:3.22
WORKDIR /app
# data/logs 预建并交给运行用户，容器不挂卷也能直接跑（SQLite 与日志有处可写）。
# tesseract 及中英语言包供图片文字识别插件的本地离线后端使用。
# bubblewrap 是 Agent 执行本地命令时的沙盒。装了它不代表一定能用——容器默认的
# seccomp 或 AppArmor 策略常常禁掉非特权用户命名空间，运行时会实际试跑一次再决定
# 用不用；但不装则连试的机会都没有，命令只能以主进程权限裸跑。
RUN apk add --no-cache ffmpeg nodejs yt-dlp bubblewrap tesseract-ocr tesseract-ocr-data-chi_sim tesseract-ocr-data-eng \
    && adduser -D -H -u 10001 diana \
    && mkdir -p /app/data /app/logs \
    && chown -R diana:diana /app/data /app/logs
COPY --from=backend /out/diana-webui /app/diana-webui
COPY --from=frontend-next /src/frontend-next/dist /app/frontend-next/dist
# 应用配置走 config.yaml；镜像内只放一份内置默认配置，挂载同名文件即可覆盖。
ENV DIANA_CONFIG=/app/config.yaml
EXPOSE 18080
USER diana
ENTRYPOINT ["/app/diana-webui"]
