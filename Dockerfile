# syntax=docker/dockerfile:1

FROM node:22-alpine AS frontend
WORKDIR /src/frontend
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

FROM node:22-alpine AS frontend-next
WORKDIR /src/frontend-next
COPY frontend-next/package*.json ./
RUN npm ci
COPY frontend-next/ ./
RUN npm run build

FROM golang:1.25-alpine AS backend
ARG GIT_SHA=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.buildVersion=${GIT_SHA}" -o /out/diana-webui ./cmd/webui

FROM alpine:3.22
WORKDIR /app
# data/logs 预建并交给运行用户，容器不挂卷也能直接跑（SQLite 与日志有处可写）。
RUN adduser -D -H -u 10001 diana \
    && mkdir -p /app/data /app/logs \
    && chown -R diana:diana /app/data /app/logs
COPY --from=backend /out/diana-webui /app/diana-webui
# 新旧前端同时打进镜像：默认服务 frontend-next，需要回退旧版时
# 把 FRONTEND_DIST 改回 /app/frontend/dist 即可。
COPY --from=frontend /src/frontend/dist /app/frontend/dist
COPY --from=frontend-next /src/frontend-next/dist /app/frontend-next/dist
ENV PORT=18080
ENV FRONTEND_DIST=/app/frontend-next/dist
ENV LOG_PATH=/app/logs/diana.log
EXPOSE 18080
USER diana
ENTRYPOINT ["/app/diana-webui"]
