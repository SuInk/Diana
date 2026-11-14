# Offline member command sandbox image for Diana.
# Build locally (supports both linux/amd64 and linux/arm64):
#   docker build -t diana-sandbox:latest deploy/sandbox
#
# Once built, Diana automatically detects diana-sandbox:latest (or DIANA_MEMBER_SANDBOX_IMAGE)
# and executes member commands inside ephemeral, 100% network-disabled (--network none),
# read-only (--read-only), zero-capability (--cap-drop ALL, no-new-privileges) containers
# with isolated per-invocation /work and /tmp mounts.

FROM debian:trixie-slim

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates tzdata fontconfig \
      fonts-noto-cjk fonts-noto-color-emoji fonts-dejavu-core \
      python3 python-is-python3 \
      python3-numpy python3-scipy python3-sympy python3-matplotlib \
      python3-pil python3-pandas python3-qrcode python3-pypdf \
      nodejs sqlite3 jq ripgrep gawk diffutils bc dc file libdigest-sha-perl \
      ffmpeg imagemagick graphviz poppler-utils qpdf pandoc qrencode \
      tar gzip bzip2 xz-utils zip unzip xxd bsdextrautils \
      chromium \
 && printf '#!/bin/sh\nif [ "$1" = "identify" ]; then shift; exec identify "$@"; fi\nif [ "$1" = "convert" ]; then shift; fi\nexec convert "$@"\n' > /usr/local/bin/magick \
 && chmod 0755 /usr/local/bin/magick \
 && fc-cache -f \
 && rm -rf /var/lib/apt/lists/*

RUN groupadd --gid 1000 box \
 && useradd --uid 1000 --gid 1000 --no-create-home --home-dir /work --shell /usr/sbin/nologin box \
 && mkdir -p /work \
 && chown 1000:1000 /work

ENV HOME=/tmp \
    TMPDIR=/tmp \
    MPLCONFIGDIR=/tmp/mpl \
    XDG_CACHE_HOME=/tmp/cache \
    DBUS_SESSION_BUS_ADDRESS=/dev/null \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONNOUSERSITE=1 \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8

USER 1000:1000
WORKDIR /work
