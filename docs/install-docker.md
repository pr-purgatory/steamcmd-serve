# Install with Docker Compose

This guide runs the controller with Docker Compose on a Linux host with
Docker Engine. The controller then creates each game server as its own
container.

Read the [security notes](../README.md#security) first. The controller needs
the Docker socket, and access to that socket is root-equivalent on the host.

## Requirements

- Docker Engine and Docker Compose. The socket proxy variant needs Docker
  Compose 2.24 or newer.
- An x86_64 host for game servers. Apple Silicon and other arm64 hosts can run
  the controller but not game servers.
- Disk space for game installs. An Enshrouded install is about 7.6 GB per
  instance. See [the Enshrouded guide](games/enshrouded.md).

## 1. Get the files

```sh
git clone https://github.com/OWNER/steamcmd-serve.git
cd steamcmd-serve
```

Replace `OWNER` with the GitHub owner of the image. In `deploy/compose.yaml`,
change this line:

```yaml
image: ghcr.io/OWNER/steamcmd-serve:latest
```

Replace `OWNER` with the owner name.

## 2. Create the data directory

The directory must exist and be owned by `PUID:PGID`. The defaults are
`1000:1000`.

```sh
mkdir -p /srv/steamcmd-serve && sudo chown 1000:1000 /srv/steamcmd-serve
```

## 3. Start the controller

```sh
export DATA_ROOT=/srv/steamcmd-serve
export DOCKER_GID=$(stat -c %g /var/run/docker.sock)
docker compose -f deploy/compose.yaml up -d
```

- `DATA_ROOT` is the host directory for all state. It is mounted at `/data`
  in the controller, and passed to the controller as `HOST_DATA_ROOT`. The
  host path must be the same path the controller sees, so game containers
  get the right bind mounts.
- `DOCKER_GID` is the group ID of the Docker socket. Compose refuses to start
  without it.
- `PUID`, `PGID` and `TZ` are optional. `PUID` and `PGID` default to `1000`.

Game installs go under `DATA_ROOT/games` unless you set `GAMES_DIR` and
`HOST_GAMES_ROOT`. To move them, uncomment the matching lines in
`deploy/compose.yaml` and set `GAMES_ROOT` to the host path.

### SELinux

On SELinux hosts, add `:z` to the data volume in your copy of the file:

```yaml
- ${DATA_ROOT:?set DATA_ROOT}:/data:z
```

## 4. Open the web UI and create the admin account

Open `http://<host>:8080`. On the first visit, create the admin account. The
password must be at least 12 characters. There is only one admin account.

Do not forward port 8080 to the internet. Put it behind a TLS reverse proxy
and set `TRUSTED_PROXIES` to the proxy address. See the
[configuration table](../README.md#configuration).

## Optional: socket proxy

The socket proxy puts a filter between the controller and the Docker socket.
It allows only containers, images, networks, volumes and POST requests. Exec is
disabled. This limits what a compromised controller can do, but it does not
remove the root-equivalent access.

```sh
docker compose -f deploy/compose.yaml -f deploy/compose.socket-proxy.yaml up -d
```

In this variant, the proxy is the only service with the socket. The controller
reaches it at `tcp://socket-proxy:2375`. `DOCKER_GID` must still be set, but it
is not used. The proxy image is `tecnativa/docker-socket-proxy:0.3.0`.

## Upgrade

Pull the new image and restart the controller. Your data stays in `DATA_ROOT`.

```sh
docker compose -f deploy/compose.yaml pull
docker compose -f deploy/compose.yaml up -d
```

If you use the socket proxy, add the same `-f deploy/compose.socket-proxy.yaml`
option to both commands.

Game servers keep running across controller restarts and upgrades.

## Uninstall

1. Delete the game instances in the UI. Choose to delete their data if you no
   longer need the saves and backups. Or remove the managed containers from the
   command line:

   ```sh
   docker ps -aq --filter label=io.steamcmd-serve.managed=true \
     | xargs -r docker rm -f
   ```

2. Stop and remove the controller:

   ```sh
   docker compose -f deploy/compose.yaml down
   ```

3. Delete the directories you no longer need:
   - `DATA_ROOT`: the database, saves, backups, logs and `secret.key`. Without
     `secret.key`, the stored game passwords and NexusMods API key cannot be
     decrypted.
   - The games directory (`DATA_ROOT/games` by default): the game installs.

Deleting these directories is permanent. Keep a copy of `DATA_ROOT` if you want
to keep your saves.

## Troubleshooting

### The controller cannot reach Docker

The log says `container engine not reachable (DOCKER_HOST)`. Check these:

- The socket group. Run `stat -c %g /var/run/docker.sock` and compare the
  result with the `DOCKER_GID` you exported. Export the value again and run
  `docker compose ... up -d` again.
- The socket path. The controller reads `/var/run/docker.sock` by default.
- The socket proxy. If you use it, `DOCKER_HOST` must be
  `tcp://socket-proxy:2375`, and the proxy must be running.

Show the log with:

```sh
docker compose -f deploy/compose.yaml logs controller
```

### Bind mount errors when a game starts

The host path of the data does not match `HOST_DATA_ROOT`. Compose sets
`HOST_DATA_ROOT` from `DATA_ROOT`, so check that `DATA_ROOT` is the real host
path and that the directory exists. If you moved the data, update `DATA_ROOT`
and start the controller again.

### A port is already in use

The controller refuses a port used by another instance or by a running
container. Pick another port in the instance settings. The controller suggests
the next free port when the default is taken. Check what uses a port with
`ss -ulpn` (UDP) or `ss -tlpn` (TCP).

### Permission denied on the data directory

The directory must be owned by `PUID:PGID`. Run
`sudo chown -R 1000:1000 /srv/steamcmd-serve` with your values, or set `PUID`
and `PGID` to match the owner.

### SELinux blocks the data or socket

Add `:z` to the data volume, as shown in step 3.
