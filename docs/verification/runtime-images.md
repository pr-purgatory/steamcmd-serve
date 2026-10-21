# Runtime image verification (T4V)

Host: Unraid 7.3.2, x86_64, Docker 29.5.3, 20 threads, 31 GB RAM. Date: 2026-10-21.
Images built on the host from `images/` at commit 1de4374, run as `PUID=99 PGID=100`.

| Check | Result |
|---|---|
| `steamcmd-base` build, including the "steamcmd self-updated" guard | Pass. `/opt/steamcmd/package` populated (steamcmd reached Valve's CDN even though `client-download.steampowered.com` was NXDOMAIN that day). |
| `app_info_print 2278520` | Pass: `"name" "Enshrouded Dedicated Server"`, `"type" "Tool"`, `"oslist" "windows"`. Anonymous login works. |
| `install 896660 --validate` (Valheim, linux) | Pass: `Success! App '896660' fully installed.`, exit 0, `valheim_server.x86_64` present, 0 files not owned by uid 99. |
| `install 2278520 --platform windows --validate` (Enshrouded) | Pass: exit 0, `enshrouded_server.exe` present. Install size **7.6 GB** (the official FAQ's ~13 GB is outdated). |
| `steamcmd-wine` build on that base | Pass. `wine --version` → `wine-11.0`; prefix initialized on first start in `/data/wineprefix`. |
| Enshrouded under Wine reaches `[Session] 'HostOnline' (up)!` | Pass, **50 s** after container start. 1.3 GiB RAM, ~37 % CPU shortly after start. |
| Server writes `enshrouded_server.json` next to the exe, uid 99 | Pass. |
| World save in the persisted `savegame` dir | Pass: `world0.ksd` + `backup/` written by the game (first save ~5 min after start). |
| `docker stop` with **SIGINT** (manifest `stopSignal`) | Pass: game logs `Switching state from Run to Shutdown`, `Clear Chunk Persistent Data`, `Shutdown to Destroy`; stop took 1 s; `world0.ksd` rewritten at shutdown. |
| Save files owned by uid 99 after stop | Pass (0 others). |

## Notes

- Container exit code after a clean SIGINT shutdown is 1, preceded by `X connection to :99 broken`. `tini -g` delivers the signal to Xvfb as well, and Wine reports the lost display while exiting. The game had already finished its shutdown and save, so the result is harmless. The controller does not treat the stop exit code as an error.
- The game tries to post analytics to `enshrouded-analytics.keengames.com`; on this network the request failed (`connection refused`) without affecting the server.
- steamcmd and Wine cannot run under qemu on Apple Silicon (16 KB pages): steamcmd segfaults and Wine asserts in `anon_mmap_fixed`. Runtime checks need a native amd64 host.

## Script

The script that produced these results exercised the images directly with `docker run`, mirroring what the controller does (same binds, env, stop signal). Test data and test images were deleted afterwards.
