# SnowLuma 加上 QQ 语音通话需要的东西：PulseAudio 虚拟声卡、点窗口的 xdotool、
# 认来电昵称的 tesseract，以及 qqcall-agent。在仓库根目录构建：
#
#   docker build -f packaging/snowluma-voice/Dockerfile -t snowluma-voice .
#
# 说明见 docs/qq-voice-call.md。

# 用在下面的 FROM 里，必须声明在第一个 FROM 之前。
ARG SNOWLUMA_IMAGE=motricseven7/snowluma:v1.14.10

FROM --platform=$BUILDPLATFORM golang:1.26.6-bookworm AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY model/qqcall model/qqcall
COPY cmd/qqcall-agent cmd/qqcall-agent
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /qqcall-agent ./cmd/qqcall-agent

FROM ${SNOWLUMA_IMAGE}
# libpulse-mainloop-glib0 是 QQ 语音插件 libAVSDKPlugin.so 的依赖，原镜像里没有：
# 缺了它点「语音通话」不报错，过一会儿显示「已取消」，对方根本没响铃。
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      pulseaudio pulseaudio-utils libpulse-mainloop-glib0 \
      xdotool imagemagick tesseract-ocr tesseract-ocr-chi-sim \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /qqcall-agent /usr/local/bin/qqcall-agent
COPY packaging/snowluma-voice/qqcall-agent.conf /etc/supervisor/conf.d/qqcall-agent.conf
EXPOSE 6190
