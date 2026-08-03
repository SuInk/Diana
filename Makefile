# Diana common commands.
#
# Usage examples:
#   make dev
#   make dev BACKEND_PORT=18081 FRONTEND_PORT=5174
#   make test
#   make build

BACKEND_HOST ?= 127.0.0.1
BACKEND_PORT ?= 18080
FRONTEND_HOST ?= 127.0.0.1
FRONTEND_PORT ?= 5173
VITE_BACKEND_TARGET ?= http://$(BACKEND_HOST):$(BACKEND_PORT)

GO ?= go
NODE ?= node
NPM ?= npm
DOCKER ?= docker
DOCKER_COMPOSE ?= docker compose

ifeq ($(OS),Windows_NT)
BIN_EXT := .exe
else
BIN_EXT :=
endif

DIST_BIN := dist/diana-webui$(BIN_EXT)

export BACKEND_HOST
export BACKEND_PORT
export FRONTEND_HOST
export FRONTEND_PORT
export VITE_BACKEND_TARGET

.DEFAULT_GOAL := help

.PHONY: help dev backend frontend frontend-next frontend-legacy deps deps-next fmt test test-go test-web build build-go build-web build-web-next build-web-legacy run run-next preview preview-legacy clean docker-build docker-up docker-down

help:
	@$(NODE) -e "console.log(['Diana Makefile','', 'Usage:', '  make dev                         Start Go backend and frontend-next', '  make dev BACKEND_PORT=18081      Start with custom backend port', '  make backend                     Start Go backend only', '  make frontend                    Start frontend-next only', '  make deps                        Install Go and frontend-next dependencies', '  make fmt                         Format Go code', '  make test                        Run Go tests and frontend-next build check', '  make build                       Build frontend-next and backend binary', '  make run                         Build frontend-next, then run backend', '  make clean                       Remove build artifacts', '  make docker-build                Build Docker image', '  make docker-up                   Start Docker Compose stack', '  make docker-down                 Stop Docker Compose stack'].join('\n'))"

dev:
	$(NODE) scripts/dev.mjs

backend:
	$(GO) run ./cmd/webui

frontend:
	cd frontend-next && $(NPM) run dev -- --host $(FRONTEND_HOST) --port $(FRONTEND_PORT) --strictPort

frontend-next: frontend

# 旧版界面仅保留显式兼容入口，不参与默认开发、测试或发布。
frontend-legacy:
	cd frontend && $(NPM) run dev -- --host $(FRONTEND_HOST) --port $(FRONTEND_PORT) --strictPort

deps:
	$(GO) mod download
	cd frontend-next && $(NPM) ci

deps-next:
	cd frontend-next && $(NPM) install

fmt:
	$(GO) fmt ./...

test: test-go test-web

test-go:
	$(GO) test ./...

test-web:
	cd frontend-next && $(NPM) run build

build: build-web build-go

build-go:
	$(NODE) -e "require('fs').mkdirSync('dist', { recursive: true })"
	$(GO) build -o $(DIST_BIN) ./cmd/webui

build-web:
	cd frontend-next && $(NPM) run build

build-web-next: build-web

build-web-legacy:
	cd frontend && $(NPM) run build

run: build-web
	FRONTEND_DIST=frontend-next/dist $(GO) run ./cmd/webui

run-next: run

preview:
	cd frontend-next && $(NPM) run preview -- --host $(FRONTEND_HOST)

preview-legacy:
	cd frontend && $(NPM) run preview -- --host $(FRONTEND_HOST)

clean:
	$(NODE) -e "const fs=require('fs'); for (const p of ['dist','frontend-next/dist','frontend/dist']) fs.rmSync(p,{recursive:true,force:true})"

docker-build:
	$(DOCKER) build -t diana:latest .

docker-up:
	$(DOCKER_COMPOSE) up -d --build

docker-down:
	$(DOCKER_COMPOSE) down
