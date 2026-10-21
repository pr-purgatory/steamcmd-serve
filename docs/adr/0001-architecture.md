# ADR 0001: Architecture

- Status: accepted
- Date: 2026-10-21

## Context

steamcmd-serve runs many SteamCMD-installed dedicated servers (different games, or several instances of one game) on one host. It must self-host on Docker, Podman and Unraid, be managed from a web UI, support mods from Thunderstore and NexusMods, and ship Enshrouded first. Enshrouded's server is Windows-only, so some games need Wine.

## Decisions

### 1. One controller, one container per game instance

A single `controller` container holds the web UI, REST API and state. Each game instance runs in its **own sibling container**, created by the controller through the Docker Engine API.

Why not one big container that supervises every game process:

- Per-instance resource limits (Enshrouded wants ~6 GB RAM each) and crash isolation come free from the container runtime.
- Windows and Linux servers need different base images (Wine vs. plain), which a single image would have to merge.
- Instances keep running across controller restarts and upgrades.

Cost: the controller needs access to the container runtime's API socket (see §6).

### 2. Runtime: Docker Engine API over a socket

The controller speaks only the Docker Engine API (`github.com/docker/docker/client`) at `DOCKER_HOST` (default `unix:///var/run/docker.sock`). This one client covers:

| Host | Socket |
|---|---|
| Docker / Unraid | `/var/run/docker.sock` |
| Podman rootful | `/run/podman/podman.sock` |
| Podman rootless | `$XDG_RUNTIME_DIR/podman/podman.sock` |
| Socket proxy | `tcp://docker-socket-proxy:2375` |

There is no runtime abstraction interface: one implementation, one API. Podman compatibility is a CI test target, not a second code path.

Every container the controller creates carries labels `io.steamcmd-serve.managed=true`, `io.steamcmd-serve.instance=<id>`, `io.steamcmd-serve.role=server|job`. On startup the controller **reconciles** DB state against containers found by label; the containers, not the DB, are the truth for "is it running".

### 3. Images

| Image | Base | Use |
|---|---|---|
| `controller` | distroless, multi-arch | UI + API |
| `steamcmd-base` | Debian slim + steamcmd + tini, amd64 | Linux game servers, all install/update jobs |
| `steamcmd-wine` | `steamcmd-base` + Wine64 + xvfb, amd64 | Windows game servers (Enshrouded) |

Runtime images are amd64-only: steamcmd has no arm64 build. The controller and all game containers run as the same `PUID:PGID` (1000:1000 default, 99:100 on Unraid), which owns the data root, so neither side ever has to chown the other's files. Rootless Podman is the exception: there game containers use `PUID=0`/`PGID=0` (root in a rootless container is the host user) and the controller maps to the host user with `keep-id`.

Install and update run as short-lived **job containers** from `steamcmd-base` that write into the instance's `server/` directory, so a running server is never touched by steamcmd. Windows servers download with `+@sSteamCmdForcePlatformType windows`.

### 4. Games are data

Each supported game is a directory `games/<id>/` with:

- `manifest.yaml` — validated by `games/manifest.schema.json`: Steam app ids, platform, runtime image, ports, launch command, settings schema, optional rendered config file, persisted paths, readiness regex, resource needs, mod support.
- `settings.schema.json` — JSON Schema for the instance's user-editable settings. The UI form is generated from it (with `x-ui` / `x-unit` hints); the API validates against it.

Adding a game means adding a directory; no Go code. Manifests ship in the controller image and are loaded from `GAMES_DIR`, so users can drop in their own.

Launch arguments and rendered config files use Go `text/template` with a fixed context: `.Settings`, `.Ports` (name → host/container port), `.Paths`. No scripts in manifests.

### 5. Controller owns game config

Enshrouded creates `enshrouded_server.json` if it is missing and may rewrite or mis-migrate an existing one. Invalid enum text stops it from booting. So:

- Instance settings live in the controller DB, validated against the game's settings schema.
- The config file is **re-rendered before every start**. A file found at first adoption is imported once.
- After stop, the controller diffs the file against what it rendered and surfaces drift instead of silently losing it.

### 6. Security

- **Socket access is root-equivalent on the host.** Anyone who controls the controller controls the host. Docs and the Unraid template must say so. Supported mitigations: rootless Podman, or `tecnativa/docker-socket-proxy` allowing only `CONTAINERS`, `IMAGES`, `NETWORKS`, `VOLUMES`, `POST`, `EXEC=0`.
- The controller only creates containers from images on an allowlist (the runtime images it ships, by repository) and only bind-mounts paths under `HOST_DATA_ROOT`. User input never becomes an arbitrary image or host path.
- Single local admin (argon2id), session cookie, CSRF on mutations. Setup endpoint closes after the first admin exists.
- Secrets (game passwords such as Enshrouded `userGroups[].password`, the Nexus API key) are encrypted at rest and **write-only** in the API: reads return `hasValue: true`, never the value. Schema fields carry `x-secret: true`.
- Mod archives are extracted with path validation (no `..`, no absolute paths, no symlinks out of the target).

### 7. Data layout and host paths

The controller creates sibling containers, so bind-mount sources must be **host** paths, not the controller's view. The controller gets both:

- `DATA_DIR` (inside controller, default `/data`)
- `HOST_DATA_ROOT` (same directory as the host sees it, e.g. `/mnt/user/appdata/steamcmd-serve`)

```
<data root>/
  controller.db
  steamcmd/                 shared steamcmd home (self-update cache)
  instances/<id>/
    server/                 game install (steamcmd force_install_dir)
    data/                   saves, logs, persisted paths
      wineprefix/           Windows games only (WINEPREFIX=/data/wineprefix)
    backups/
```

### 8. Ports

Ports come from the manifest (name, default, protocol). On create, the controller suggests the default, or the next free port when that one is taken by another instance. A port allocator rejects clashes with other instances and with ports bound on the host. Containers use bridge networking with explicit `/udp` or `/tcp` mappings; container and host port are kept equal, because Steam server-browser queries advertise the port the server thinks it uses.

### 9. Mods

`internal/mods.Provider` is implemented by Thunderstore and NexusMods. Two real implementations justify the interface. Mods are installed per instance, only while it is stopped, through a loader declared in the manifest (`bepinex` first). Mods are reapplied after every game update, because `app_update … validate` can remove loader files. Nexus direct downloads need a Premium key; free users upload the file by hand.

Enshrouded declares `mods.loader: none` until a server-side mod scene exists; Valheim is the reference game for the mod pipeline.

### 10. Stack

- Go (single static binary), SQLite via `modernc.org/sqlite` (no cgo, cross-compiles).
- Svelte + Vite UI, built to `web/dist`, embedded with `go:embed`.
- REST + SSE (logs, job progress), described in `api/openapi.yaml`.
- Optional stretch client: Wails wrapping the same web UI.

## Consequences

- One codepath for every runtime; Podman parity is proven by CI, not by abstraction.
- Game support scales by data; quirks a manifest cannot express become explicit manifest fields, reviewed as schema changes.
- The socket requirement is the main security trade-off, documented up front, with proxy and rootless options.
