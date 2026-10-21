FROM --platform=linux/amd64 debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=en_US.UTF-8 \
    LANGUAGE=en_US:en \
    LC_ALL=en_US.UTF-8

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates curl lib32gcc-s1 tini locales \
 && sed -i 's/^# *\(en_US.UTF-8 UTF-8\)/\1/' /etc/locale.gen \
 && locale-gen \
 && rm -rf /var/lib/apt/lists/*

# steamcmd from Valve's tarball; first run self-updates it into the image.
# Non-zero exit on the first run is expected and tolerated, but the update itself must
# have happened, or every container would start with a bootstrapper that has to download.
# /opt/steamcmd is the read-only seed; entrypoint.sh copies it into the /steamcmd volume.
RUN mkdir -p /opt/steamcmd /server /data /steamcmd \
 && curl -fsSL https://steamcdn-a.akamaihd.net/client/installer/steamcmd_linux.tar.gz \
      | tar -xz -C /opt/steamcmd \
 && (HOME=/root /opt/steamcmd/steamcmd.sh +quit || true) \
 && test -n "$(ls -A /opt/steamcmd/package)" \
 && rm -rf /root/Steam /root/.steam \
 && chmod -R a+rX /opt/steamcmd \
 && chmod 0777 /server /data /steamcmd

COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod 0755 /usr/local/bin/entrypoint.sh \
 && mkdir -p /usr/local/lib/steamcmd-serve/run.d

ENV PUID=1000 \
    PGID=1000 \
    HOME=/steamcmd

WORKDIR /server
VOLUME ["/steamcmd"]

ENTRYPOINT ["/usr/bin/tini", "-g", "--", "/usr/local/bin/entrypoint.sh"]
