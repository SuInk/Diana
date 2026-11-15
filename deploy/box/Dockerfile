# Diana 工作区容器（work box）镜像，照雨夹雪 deploy/box/Dockerfile。
# 根文件系统只读，运行期工具都装在这里；uv 环境、npm/pip 用户安装落在 /work
# （HOME=/work/.home），随工作区重置一起清空。Diana 首次使用时用这份文件在本地
# docker build，标签带文件内容的哈希，改了这份文件就会重建镜像和容器。
FROM node:24-trixie-slim@sha256:54f84b4cf8432b21aaffd5d3ce8d41e12cea0d10d33c09d2f3c9879d49d6b446

# uv 0.13.6, pinned to the official multi-platform image digest.
COPY --from=ghcr.io/astral-sh/uv:0.13.6@sha256:af22a52f5eafef3d78e551599d405c02df61919d18028f0cea8eda93bc9986d0 /uv /uvx /usr/local/bin/

# iptables 只给 Diana 起的一次性防火墙容器用（它带 NET_ADMIN 进 box 的网络命名空间
# 写出站规则）；box 本身 cap-drop ALL，跑不了它。
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      bash ca-certificates curl wget git jq file procps less nano tree unzip zip xz-utils \
      ripgrep fd-find sqlite3 bc \
      python3 python3-pip python3-venv python3-pil python3-numpy python3-requests \
      ffmpeg imagemagick poppler-utils qrencode \
      fontconfig fonts-noto-cjk fonts-noto-color-emoji \
      chromium \
      iptables \
 && rm -rf /var/lib/apt/lists/*

RUN userdel --remove node \
 && useradd --uid 1000 --no-create-home --home-dir /work/.home --shell /bin/bash box \
 && mkdir -p /work \
 && chown 1000:1000 /work

ENV PIP_BREAK_SYSTEM_PACKAGES=1 \
    UV_CACHE_DIR=/work/.home/.cache/uv \
    UV_LINK_MODE=copy \
    NPM_CONFIG_PREFIX=/work/.home/.npm-global \
    PATH=/work/.home/.local/bin:/work/.home/.npm-global/bin:/usr/local/bin:/usr/bin:/bin \
    LANG=C.UTF-8 \
    TZ=Asia/Shanghai

USER box
WORKDIR /work
