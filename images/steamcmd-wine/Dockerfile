# Runtime for Windows-only dedicated servers (e.g. Enshrouded) under Wine.
# Install/update jobs still use steamcmd-base; this image only runs servers.
ARG BASE_IMAGE=localhost/steamcmd-base:latest
FROM --platform=linux/amd64 ${BASE_IMAGE}

ARG WINE_BRANCH=stable

RUN set -eux; \
    dpkg --add-architecture i386; \
    apt-get update; \
    apt-get install -y --no-install-recommends gnupg; \
    mkdir -pm755 /etc/apt/keyrings; \
    curl -fsSL https://dl.winehq.org/wine-builds/winehq.key | gpg --dearmor -o /etc/apt/keyrings/winehq-archive.key; \
    curl -fsSL -o /etc/apt/sources.list.d/winehq-bookworm.sources https://dl.winehq.org/wine-builds/debian/dists/bookworm/winehq-bookworm.sources; \
    apt-get update; \
    apt-get install -y --no-install-recommends "winehq-${WINE_BRANCH}" xvfb xauth; \
    apt-get purge -y --auto-remove gnupg; \
    rm -rf /var/lib/apt/lists/*; \
    wine --version

# Prefix lives in the instance data dir, so it persists and gets PUID ownership. Skip Mono/Gecko prompts.
ENV WINEPREFIX=/data/wineprefix \
    WINEARCH=win64 \
    WINEDEBUG=-all \
    WINEDLLOVERRIDES="mscoree,mshtml=" \
    DISPLAY=:99

RUN mkdir -p /usr/local/lib/steamcmd-serve/run.d \
    && install -d -m 1777 /tmp/.X11-unix
COPY run.d/10-wine.sh /usr/local/lib/steamcmd-serve/run.d/10-wine.sh
