# Install with Podman

This guide runs the controller with rootless Podman, managed by systemd as a
Quadlet unit. Rootless Podman is the recommended option for Podman. Rootful
Podman is covered at the end.

Read the [security notes](../README.md#security) first. The controller uses
the Podman API socket. With rootless Podman, that socket grants only the
rights of your user account. With rootful Podman, it grants root on the host.

## Requirements

- Podman with the systemd user session, and a user account that can run
  `systemctl --user`.
- An x86_64 host for game servers. Apple Silicon and other arm64 hosts can run
  the controller but not game servers.
- Disk space for game installs. An Enshrouded install is about 7.6 GB per
  instance. See [the Enshrouded guide](games/enshrouded.md).

## 1. Get the files

```sh
git clone https://github.com/OWNER/steamcmd-serve.git
cd steamcmd-serve
```

## 2. Install the Quadlet unit

Copy the unit file and edit `Image=`. Replace `OWNER` with the GitHub owner
of the image:

```sh
mkdir -p ~/steamcmd-serve ~/.config/containers/systemd
cp deploy/podman/steamcmd-serve.container ~/.config/containers/systemd/
```

```ini
Image=ghcr.io/OWNER/steamcmd-serve:latest
```

Then enable the socket, start the unit and check it:

```sh
systemctl --user enable --now podman.socket
loginctl enable-linger "$USER"      # keep it running after logout / start at boot
systemctl --user daemon-reload && systemctl --user start steamcmd-serve
systemctl --user status steamcmd-serve
```

`loginctl enable-linger` keeps your user services running after you log out
and starts them at boot.

## 3. Open the web UI and create the admin account

Open `http://<host>:8080`. On the first visit, create the admin account. The
password must be at least 12 characters. There is only one admin account.

Do not forward port 8080 to the internet. Put it behind a TLS reverse proxy
and set `TRUSTED_PROXIES` to the proxy address. See the
[configuration table](../README.md#configuration).

## How the unit is set up

These settings are in `steamcmd-serve.container`. Change them only if you know
why.

- **Data directory.** `Volume=%h/steamcmd-serve:/data:Z` maps `~/steamcmd-serve`
  to `/data`. `HOST_DATA_ROOT` is the same host path, so game containers get
  the right bind mounts. The `:Z` label is for SELinux.
- **User namespace.** `UserNS=keep-id:uid=65532,gid=65532` maps the controller's
  user (65532) to your host user. Your user owns the socket and the data
  directory.
- **PUID and PGID.** `PUID=0` and `PGID=0`. In rootless Podman, root in a
  game container is your host user, so game files stay owned by you. Any other
  ID maps to a subuid that the controller cannot read.
- **Socket.** The unit mounts `%t/podman/podman.sock` (your runtime directory)
  as `/var/run/docker.sock`, and sets `DOCKER_HOST` to match.

If SELinux blocks access to the socket, add this line to the `[Container]`
section, then run `systemctl --user daemon-reload` and restart the unit:

```ini
SecurityLabelDisable=true
```

## Rootless limits

Rootless Podman cannot bind ports below 1024. The game ports are high, so this
is normally fine.

## Upgrade

Pull the new image and restart the unit. Your data stays in `~/steamcmd-serve`.

```sh
podman pull ghcr.io/OWNER/steamcmd-serve:latest
systemctl --user restart steamcmd-serve
```

Game servers keep running across controller restarts and upgrades.

## Uninstall

1. Delete the game instances in the UI. Choose to delete their data if you no
   longer need the saves and backups. Or remove the managed containers from the
   command line:

   ```sh
   podman ps -aq --filter label=io.steamcmd-serve.managed=true \
     | xargs -r podman rm -f
   ```

2. Stop the unit, remove the Quadlet file and reload systemd:

   ```sh
   systemctl --user disable --now steamcmd-serve
   rm ~/.config/containers/systemd/steamcmd-serve.container
   systemctl --user daemon-reload
   ```

3. Delete the directories you no longer need:
   - `~/steamcmd-serve`: the database, saves, backups, logs and `secret.key`.
     Without `secret.key`, the stored game passwords and NexusMods API key
     cannot be decrypted.
   - The games directory: the game installs. It is `~/steamcmd-serve/games`
     unless you set `GAMES_DIR` and `HOST_GAMES_ROOT` in the unit.

Deleting these directories is permanent.

## Troubleshooting

### The controller cannot reach Podman

The log says `container engine not reachable (DOCKER_HOST)`. Check these:

- The socket service. Run `systemctl --user status podman.socket`. Start it
  with `systemctl --user enable --now podman.socket` if it is not running.
- SELinux. If the socket is blocked, add `SecurityLabelDisable=true` as shown
  above.

Show the controller log with:

```sh
journalctl --user -u steamcmd-serve
```

### The unit stops after you log out

Run `loginctl enable-linger "$USER"`. Check the setting with
`loginctl show-user "$USER" | grep Linger`.

### Bind mount errors when a game starts

The host path of the data does not match `HOST_DATA_ROOT`. In the unit, both
`Volume=` and `HOST_DATA_ROOT` must point to the same host directory. The
default is `%h/steamcmd-serve` for both.

### Permission denied on the data directory

Your user must own `~/steamcmd-serve`. Check with `ls -ld ~/steamcmd-serve`.

### A port is already in use

The controller refuses a port used by another instance or by a running
container. Pick another port in the instance settings. The controller suggests
the next free port when the default is taken.

## Rootful Podman

Rootful Podman gives the controller root-equivalent access to the host. Prefer
rootless Podman, or the socket proxy in [install-docker.md](install-docker.md),
where you can.

Enable the rootful socket and set the data path:

```sh
sudo systemctl enable --now podman.socket     # /run/podman/podman.sock
export DATA_ROOT=/srv/steamcmd-serve DOCKER_GID=$(stat -c %g /run/podman/podman.sock)
```

Then use `deploy/compose.yaml` with a compose provider. Change the socket bind
to `/run/podman/podman.sock:/var/run/docker.sock`. Add `:z` to the data volume
on SELinux hosts. Follow the steps in [install-docker.md](install-docker.md)
for the data directory and the first login.
