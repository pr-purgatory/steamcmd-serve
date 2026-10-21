# Deploying steamcmd-serve

The controller (web UI + API on port 8080) runs as one container. It creates the
game servers as sibling containers through the container runtime's Docker-compatible
API socket, and those containers publish their own game ports on the host.

Replace `OWNER` in `ghcr.io/OWNER/steamcmd-serve:latest` with the image owner in every
file below.

## Security

**Read this first.**

- **Access to the Docker socket is root-equivalent on the host** (rootful Docker or
  rootful Podman). Anyone who can control the controller can control the host.
- Mitigations:
  - Put `tecnativa/docker-socket-proxy` in front of the socket (see
    [Socket proxy](#socket-proxy-variant)); it allows only containers, images, networks
    and volumes, with `EXEC=0`.
  - Use rootless Podman (see [Rootless Podman](#rootless-podman-quadlet)): the socket
    then only grants the rights of an unprivileged user.
- **Never expose port 8080 to the internet** without a TLS-terminating reverse proxy
  (Caddy, nginx, Traefik). The admin login and session cookie must not travel in
  plain text.
- The controller only starts containers from its own image allowlist and only
  bind-mounts paths under `HOST_DATA_ROOT`.

## Firewall and ports

Compose publishes only 8080. Each game instance publishes its own ports (for example
Enshrouded `15637/udp`), chosen in the UI. Open or forward those ports per instance in
your host firewall and router. Rootless Podman cannot bind ports below 1024; game ports
are high, so this is normally fine.

## Docker Compose

```sh
mkdir -p /srv/steamcmd-serve && sudo chown 1000:1000 /srv/steamcmd-serve   # PUID:PGID
export DATA_ROOT=/srv/steamcmd-serve
export DOCKER_GID=$(stat -c %g /var/run/docker.sock)
docker compose -f deploy/compose.yaml up -d
```

Then open `http://<host>:8080`.

Notes:

- `DATA_ROOT` is bind-mounted at `/data` and also passed as `HOST_DATA_ROOT`, so the
  path the controller hands to game containers is always the real host path.
- `DOCKER_GID` has no default and compose refuses to start without it: the controller
  needs the socket's group.
- Optional: `PUID`/`PGID` (default 1000/1000). The controller and every game container
  run as this user, which must own `DATA_ROOT`. Also `TZ`.
- **No healthcheck is defined**: the image is distroless and has no `curl` or shell.
  The controller serves `GET /healthz` (204) for external monitors.
- SELinux hosts: append `:z` to the data volume (`${DATA_ROOT}:/data:z`) in your copy
  of the file.

### Socket proxy variant

Requires Docker Compose 2.24 or newer (uses `!override` and `!reset`).

```sh
docker compose -f deploy/compose.yaml -f deploy/compose.socket-proxy.yaml up -d
```

The proxy is the only service with the socket (read-only mount) and sits on an
`internal` network with no published ports. The controller reaches it at
`tcp://socket-proxy:2375` and has no socket mount. `DOCKER_GID` must still be set (the
base file requires it) but is unused in this variant. The proxy image is pinned to
`tecnativa/docker-socket-proxy:0.3.0`.

## Rootless Podman (Quadlet)

```sh
mkdir -p ~/steamcmd-serve ~/.config/containers/systemd
cp deploy/podman/steamcmd-serve.container ~/.config/containers/systemd/
systemctl --user enable --now podman.socket
loginctl enable-linger "$USER"      # keep it running after logout / start at boot
systemctl --user daemon-reload && systemctl --user start steamcmd-serve
```

Edit `Image=` (replace `OWNER`) first. The unit uses
`UserNS=keep-id:uid=65532,gid=65532` so the container's uid 65532 is your host user,
who owns the socket and data dir. Keep `PUID=0`/`PGID=0`: in rootless Podman, root in
a game container is your host user, so game files stay owned by you; any other id
maps to a subuid the controller cannot read. Check with
`systemctl --user status steamcmd-serve`.

If SELinux blocks access to the socket, add `SecurityLabelDisable=true` to the
`[Container]` section.

## Rootful Podman

Enable the socket and point the container at it:

```sh
sudo systemctl enable --now podman.socket     # /run/podman/podman.sock
export DATA_ROOT=/srv/steamcmd-serve DOCKER_GID=$(stat -c %g /run/podman/podman.sock)
```

Use `deploy/compose.yaml` with a compose provider, changing the socket bind to
`/run/podman/podman.sock:/var/run/docker.sock`. Add `:z` on the data volume under
SELinux. This is the root-equivalent case; prefer the socket proxy or rootless.

## Unraid

See `deploy/unraid/`.
