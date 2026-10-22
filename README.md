# steamcmd-serve

steamcmd-serve is a self-hosted web UI and API for installing, configuring
and running SteamCMD dedicated game servers. You manage each server from the
browser: install it, change its settings, start and stop it, read its logs,
back up its saves and, for games that support it, install mods. Each game
server runs in its own container. The controller creates those containers
through the Docker Engine API, so the same setup works on Docker, Podman and
Unraid.

Built-in games are Enshrouded (Windows server under Wine) and Valheim (Linux,
with mods through BepInEx).

## Features

- Web UI and REST API on port 8080, with one local admin account.
- One container per game server instance. Several instances of one game can
  run side by side on different ports.
- Install, start, stop, restart and update, with live progress and logs.
- Settings forms generated from each game's JSON Schema. Changes apply on the
  next start.
- Manual and scheduled backups of saves, with restore.
- Valheim mods from Thunderstore and NexusMods, installed per instance.
- Game passwords and the NexusMods API key are encrypted at rest.

## Quickstart

Each platform has a detailed guide in `docs/`. Replace `OWNER` in the image
name (`ghcr.io/OWNER/steamcmd-serve`) with the GitHub owner of the image
before you start.

Docker Compose ([full guide](docs/install-docker.md)):

```sh
mkdir -p /srv/steamcmd-serve && sudo chown 1000:1000 /srv/steamcmd-serve
export DATA_ROOT=/srv/steamcmd-serve
export DOCKER_GID=$(stat -c %g /var/run/docker.sock)
docker compose -f deploy/compose.yaml up -d
```

Rootless Podman, Quadlet ([full guide](docs/install-podman.md)):

```sh
mkdir -p ~/steamcmd-serve ~/.config/containers/systemd
cp deploy/podman/steamcmd-serve.container ~/.config/containers/systemd/
systemctl --user enable --now podman.socket
loginctl enable-linger "$USER"
systemctl --user daemon-reload && systemctl --user start steamcmd-serve
```

Unraid ([full guide](docs/install-unraid.md)): add the template from
`deploy/unraid/steamcmd-serve.xml`, set the Data Path and Games Path, then
start the container and open the WebUI port.

Then open `http://<host>:8080` and create the admin account.

## Security

**Read this before you install.**

- **Access to the Docker socket is root-equivalent on the host.** Anyone who
  can control the controller can control the host.
- Reduce that risk with the Docker socket proxy in
  [docs/install-docker.md](docs/install-docker.md), or with rootless Podman
  in [docs/install-podman.md](docs/install-podman.md).
- **Do not expose port 8080 to the internet.** Put it behind a reverse proxy
  that terminates TLS, and set `TRUSTED_PROXIES` to that proxy.
- Game passwords and the NexusMods API key are encrypted at rest with
  `DATA_DIR/secret.key`. The API never returns them.
- Rendered game config files contain game passwords in plain text, because
  the game needs them. Protect the data directory.
- Each game instance publishes its own game ports. Open them per instance in
  your firewall and router.

## Configuration

Set these on the controller container.

| Variable | Default | Purpose |
|---|---|---|
| `DATA_DIR` | `/data` | Data directory inside the controller container. |
| `HOST_DATA_ROOT` | none (required) | The same directory as the host sees it. |
| `GAMES_DIR` | `DATA_DIR/games` | Game installs in the controller. |
| `HOST_GAMES_ROOT` | `HOST_DATA_ROOT/games` | Host path for games. |
| `PUID` | `1000` | User ID the controller and game containers run as. |
| `PGID` | `1000` | Group ID the controller and game containers run as. |
| `TZ` | unset (compose, Unraid: `UTC`) | Time zone, e.g. `Europe/Berlin`. |
| `DOCKER_HOST` | `unix:///var/run/docker.sock` | Container engine endpoint. |
| `TRUSTED_PROXIES` | unset | Comma-separated CIDRs or IPs of reverse proxies. |
| `BACKUP_INTERVAL` | `6h` | Automatic backup interval. `0` disables it. |
| `LISTEN_ADDR` | `:8080` | Address and port the web UI and API listen on. |
| `IMAGE_REPO` | set by the image | Registry path of the runtime images. |
| `IMAGE_TAG` | set by the image | Tag of the runtime images. |

`PUID` and `PGID` default to `99` and `100` on Unraid, and to `0` and `0` for
rootless Podman. See the platform guides.

## Supported games

| Game | Platform | Ports | Mods |
|---|---|---|---|
| Enshrouded | Windows server under Wine | `15637/udp` | None |
| Valheim | Linux | `2456/udp`, `2457/udp` | BepInEx |

Valheim mods come from Thunderstore and NexusMods. Game guides:
[Enshrouded](docs/games/enshrouded.md).

## Development

```sh
make build      # builds bin/controller
make test       # go test ./...
make lint-api   # lints api/openapi.yaml with Redocly
make it         # integration tests; needs DOCKER_HOST set to a container engine
```

The web UI is in `web/`:

```sh
cd web
npm ci
npm run dev       # UI with the backend
npm run dev:mock  # UI without a backend
```

End-to-end test on a Docker host: `test/e2e/enshrouded.sh`.

Adding a game means adding a directory under `games/<id>/` with a
`manifest.yaml`. See [CONTRIBUTING.md](CONTRIBUTING.md) and
[docs/adr/0001-architecture.md](docs/adr/0001-architecture.md).

Every commit needs a sign-off line. Use `git commit -s`. See
[CONTRIBUTING.md](CONTRIBUTING.md).

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
