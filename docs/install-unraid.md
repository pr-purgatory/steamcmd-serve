# Install on Unraid

This guide installs the controller on Unraid with the template in
`deploy/unraid/steamcmd-serve.xml`. The controller then creates each game
server as its own container.

Read the [security notes](../README.md#security) first. The template maps the
Docker socket into the controller. Access to that socket is root-equivalent on
the Unraid host.

The template is not in Community Apps yet. You install it by hand, as
described below.

## Requirements

- Unraid 7 with the Docker service enabled.
- An x86_64 host for game servers.
- Disk space for game installs. An Enshrouded install is about 7.6 GB per
  instance. See [the Enshrouded guide](games/enshrouded.md).

## 1. Create the share folders

The defaults in the template are:

- `/mnt/user/appdata/steamcmd-serve` for app data: the database, saves,
  backups and `secret.key`.
- `/mnt/user/games/steamcmd-serve` for game installs, one folder per game.

Create them and give them to `nobody:users` (`99:100`), the default `PUID` and
`PGID` in the template. In the Unraid terminal:

```sh
mkdir -p /mnt/user/appdata/steamcmd-serve /mnt/user/games/steamcmd-serve
chown -R 99:100 /mnt/user/appdata/steamcmd-serve /mnt/user/games/steamcmd-serve
```

## 2. Add the template

Use one of these methods.

**Copy the XML file.** Copy `deploy/unraid/steamcmd-serve.xml` to the user
templates folder:

```sh
cp deploy/unraid/steamcmd-serve.xml /boot/config/plugins/dockerMan/templates-user/
```

Before you copy it, replace `OWNER` in the `Repository` line with the GitHub
owner of the image. The file has `OWNER` in several places. The `Repository`
line is the one that matters for pulling the image:

```xml
<Repository>ghcr.io/OWNER/steamcmd-serve:latest</Repository>
```

**Template repository.** If you host this repository on GitHub, you can add it
under the Docker settings page as a template repository. Then choose the
template from the list when you add a container.

Then go to the **Docker** tab, choose **Add Container**, and select
`steamcmd-serve` from the template list.

## 3. Check the template fields

Most fields have good defaults. Check these before you apply.

| Field | Value | Notes |
|---|---|---|
| WebUI Port | `8080` | Port of the web UI on the host. |
| Data Path | `/mnt/user/appdata/steamcmd-serve` | Mapped to `/data`. |
| Games Path | `/mnt/user/games/steamcmd-serve` | Mapped to `/games`. |
| Docker Socket | `/var/run/docker.sock` | Same path in the container. |
| `DATA_DIR` | `/data` | Keep as is. |
| `HOST_DATA_ROOT` | `/mnt/user/appdata/steamcmd-serve` | Equals Data Path. |
| `GAMES_DIR` | `/games` | Keep as is. Advanced view only. |
| `HOST_GAMES_ROOT` | `/mnt/user/games/steamcmd-serve` | Equals Games Path. |
| `PUID` | `99` | Unraid `nobody`. |
| `PGID` | `100` | Unraid `users`. |
| `TZ` | `Etc/UTC` | For example `America/New_York`. |
| `DOCKER_HOST` | `unix:///var/run/docker.sock` | Keep the default. |
| Extra Parameters | `--user=99:100 --group-add=281` | See the next section. |

**Paths must match.** `HOST_DATA_ROOT` and `HOST_GAMES_ROOT` are the host paths
that game containers bind-mount. They must be identical to the Data Path and
Games Path. If you change one, change the other. If they differ, game servers
start with bind mounts that point at the wrong place.

## 4. Check the Docker group

The controller runs as `nobody:users` (`--user=99:100`). It also needs the
group of the Docker socket, which is set with `--group-add` in Extra
Parameters. On stock Unraid 7 that group is `281`.

Check your value in the Unraid terminal:

```sh
stat -c %g /var/run/docker.sock
```

If the output is not `281`, change `--group-add=281` in Extra Parameters to
your value. Keep `--user=99:100`.

## 5. Start and open the web UI

Click **Apply**. When the container is running, click the WebUI link or open
`http://<unraid-ip>:8080`. On the first visit, create the admin account. The
password must be at least 12 characters. There is only one admin account.

Do not forward port 8080 to the internet. Put it behind a TLS reverse proxy.
Set `TRUSTED_PROXIES` to the proxy address. The template does not set it by
default, so add it as a variable in the container settings.

Game ports are per instance. Open them in your router or firewall, as described
in the platform guide for your game.

## Where files go

Under the Data Path, `/mnt/user/appdata/steamcmd-serve/`:

- `controller.db`: the database with instances, settings and users.
- `secret.key`: the key that encrypts stored passwords and the NexusMods key.
- `instances/<id>/data/`: saves, logs and copies of the last rendered config.
- `instances/<id>/backups/`: backups.

Under the Games Path, `/mnt/user/games/steamcmd-serve/`:

- `<game>/<id>/`: the game install.

## Upgrade

In the **Docker** tab, update the `steamcmd-serve` container to the latest
image, as you do for other containers. Your data stays in the Data Path and the
Games Path. Game servers keep running across controller restarts and upgrades.

## Uninstall

1. Delete the game instances in the web UI. Choose to delete their data if you
   no longer need the saves and backups.
2. Remove the `steamcmd-serve` container from the **Docker** tab.
3. Delete the folders you no longer need:
   - `/mnt/user/appdata/steamcmd-serve`: the database, saves, backups and
     `secret.key`. Without `secret.key`, the stored game passwords and NexusMods
     API key cannot be decrypted.
   - `/mnt/user/games/steamcmd-serve`: the game installs.
4. If you copied the template XML, delete
   `/boot/config/plugins/dockerMan/templates-user/steamcmd-serve.xml`.

Deleting these folders is permanent.

## Troubleshooting

### The controller cannot reach Docker

The log says `container engine not reachable (DOCKER_HOST)`. The Docker group
is wrong. Run `stat -c %g /var/run/docker.sock` and set `--group-add` in Extra
Parameters to that value. Then restart the container.

### Game servers fail to start with bind mount errors

`HOST_DATA_ROOT` or `HOST_GAMES_ROOT` does not match the Data Path or the Games
Path. Make them identical, then restart the container.

### Permission denied on the data or games folder

The folders must be owned by `99:100`. Run the `chown` command from step 1 again.

### A port is already in use

The controller refuses a port used by another instance or by a running
container. Pick another port in the instance settings. The controller suggests
the next free port when the default is taken.

### The template does not appear

Check that the XML file is in
`/boot/config/plugins/dockerMan/templates-user/`, and that it has a `.xml`
extension. Reload the Docker tab.
