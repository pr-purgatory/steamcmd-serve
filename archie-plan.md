# Plan: steamcmd-serve — containerized multi-game SteamCMD manager
Goal: Apache-2.0, self-hosted (Docker / Podman / Unraid) manager that installs, configures and runs many SteamCMD game-server instances from a web UI, Enshrouded first, with Thunderstore/NexusMods mod support.
Tiers: light=claude-haiku-5-5 standard=claude-sonnet-5-5 strong=claude-opus-5-5 max=claude-opus-5-5 (max reasoning effort)
Audit: pass-with-fixes (8) — self-audit by main agent on strong-tier model
Progress: W1 done (T1) · W2 done (T2 T3 T4 T5; runtime checks moved to T4V) · W3 done (T4V verified on Unraid) · W4 done · W5 done · W6 done · W7 done · W8 done (T19)
Scale: 1-2 jr · 2.5-3 eng · 3.5-4 sr · 4.5-5 ar

## Questions for Prompter
| # | Question | Default | Answer |
|---|----------|---------|--------|
| Q1 | Controller language | Go | assumed |
| Q2 | Frontend | Svelte + Vite, embedded via `go:embed` | assumed |
| Q3 | Instance model | One sibling container per instance via Docker API socket (Podman-compatible) | assumed |
| Q4 | Second game to prove mod pipeline (Enshrouded has no real server-side mod scene) | Valheim (AppID 896660, native Linux, Thunderstore canonical) | assumed |
| Q5 | Auth | Single local admin; OIDC later | assumed |
| Q6 | Git | `git init`, one commit per wave, no push | assumed |
| Q7 | Max concurrent subagents | 4 | assumed |
| Q8 | Enshrouded config source | — | **Official PDFs in `Enshrouded-Server-Docs/`** (facts captured below) |
| Q9 | Image registry / GitHub owner | `ghcr.io/<owner>` placeholder | **pr-purgatory** (git@github.com:pr-purgatory/steamcmd-serve.git) |
| Q10 | x86 test host for E2E | — | **Unraid** |
| Q11 | Unraid access for E2E: SSH target + appdata path | `ssh root@tower`, `/mnt/user/appdata/steamcmd-serve` | **root@<unraid-host>; appdata default; games at /mnt/user/games/steamcmd-serve/<game>/** |
| Q12 | (verified 2026-10-21 on Unraid: Enshrouded Dedicated Server, windows) SteamCMD AppID for Enshrouded server. 1203620 is the *game*; the dedicated-server tool is believed to be **2278520** (anonymous login) | `app_update 2278520`; keep 1203620 as the Steam game id for display/query | assumed — verified in T5 |

## Enshrouded reference (from official PDFs, Oct 2026)
- Windows-only server binary (`enshrouded_server.exe`); "Linux support planned". → Needs Wine on Linux.
- Config `enshrouded_server.json` lives next to the exe; crash dumps too. Server **creates it if missing** and may **rewrite/incorrectly migrate** an existing one.
- Top level: `name`, `saveDirectory` (`./savegame`), `logDirectory` (`./logs`, old logs → `./logs/backup`), `ip` (`0.0.0.0`), `queryPort` (15637), `slotCount` (1–16), `tags` [], `voiceChatMode` (Proximity|Global), `enableVoiceChat`, `enableTextChat`, `gameSettingsPreset` (Default|Relaxed|Hard|Survival|Custom), `gameSettings` {…}, `userGroups` […], `bans` [].
- `gameSettings` only take effect when preset = `Custom`. Numeric out-of-range → server clamps. **Invalid enum text → server refuses to boot** (`Internal Error: Failed to parse Config`).
- Legacy top-level `password` is deprecated; if present the server creates a `default` user group from it. Do not emit it.
- `userGroups[]`: `name`, `password`, `canKickBan`, `canAccessInventories`, `canEditWorld`, `canEditBase`, `canExtendBase`, `reservedSlots`. Fresh file seeds Admin/Friend/Guest/Visitor with random passwords.
- gameSettings (default · range/options): playerHealthFactor 1·0.25–4; playerManaFactor 1·0.25–4; playerStaminaFactor 1·0.25–4; playerBodyHeatFactor 1·0.5–2; playerDivingTimeFactor 1·0.5–2; enableDurability true; enableStarvingDebuff false; foodBuffDurationFactor 1·0.5–2; fromHungerToStarving 600e9 ns·300e9–1200e9; shroudTimeFactor 1·0.5–2; tombstoneMode AddBackpackMaterials·{AddBackpackMaterials,Everything,NoTombstone}; enableGliderTurbulences true; weatherFrequency Normal·{Disabled,Rare,Normal,Often}; fishingDifficulty Normal·{VeryEasy,Easy,Normal,Hard,VeryHard}; miningDamageFactor 1·0.5–2; plantGrowthSpeedFactor 1·0.25–2; resourceDropStackAmountFactor 1·0.25–2; factoryProductionSpeedFactor 1·0.25–2; perkUpgradeRecyclingFactor 0.5·0–1 (doc prose says 0.1, default JSON says 0.5 — use 0.5); perkCostFactor 1·0.25–2; experienceCombatFactor 1·0.25–2; experienceMiningFactor 1·0–2; experienceExplorationQuestsFactor 1·0.25–2; randomSpawnerAmount Normal·{Few,Normal,Many,Extreme}; aggroPoolAmount Normal·{Few,Normal,Many,Extreme}; enemyDamageFactor 1·0.25–5; enemyHealthFactor 1·0.25–4; enemyStaminaFactor 1·0.5–2; enemyPerceptionRangeFactor 1·0.5–2; bossDamageFactor 1·0.2–5; bossHealthFactor 1·0.2–5; threatBonus 1·0.25–4; pacifyAllEnemies false; tamingStartleRepercussion LoseSomeProgress·{KeepProgress,LoseSomeProgress,LoseAllProgress}; dayTimeDuration 1800e9 ns·120e9–3600e9; nightTimeDuration 720e9 ns·120e9–3600e9; curseModifier Normal·{Easy,Normal,Hard}.
- Network: IPv4 only; `queryPort` 15637 (UDP) + Steam client ports.
- Ready marker in log: `[Session] 'HostOnline' (up)!`.
- Resources: ~4.4 GB RAM idle, +100 MB/player, ≤6 GB; install ~13 GB, keep 30 GB free; saves 1–100 MB per world, written every 5 min. 16 players: 8C/16T, 16 GB host.

## Architecture (decided in T1, summarized)
- `controller`: one Go binary, embedded Svelte UI, SQLite state, talks to Docker Engine API over a socket (Docker, rootful/rootless Podman, Unraid).
- Each instance = one container from a runtime image (`steamcmd-base` for Linux servers, `steamcmd-wine` for Windows servers), with bind mounts `server/` (install), `data/` (saves/config), `backups/`.
- Games = data: `games/<id>/manifest.yaml` (appids, platform, image, ports, config file + JSON Schema, launch cmd, ready regex, mod loader). New game = new manifest, no Go code.
- Controller is source of truth for game config; renders the config file before every start.

## Tasks

### T1 — Architecture ADR, repo skeleton, core schemas
- Score: 4.5 (ar) · Tier: max · Executor: main
- Status: done
- Depends: none
- Wave: 1 · Parallel: no
- Files: `docs/adr/0001-architecture.md` (w), `go.mod` (w), `cmd/controller/main.go` (w), `internal/` layout stubs (w), `games/manifest.schema.json` (w), `api/openapi.yaml` (w), `internal/mods/provider.go` (w), `Makefile` (w)
- Pass: ADR records instance model, runtime abstraction, config ownership, socket-security stance, data layout; `go build ./...` exits 0; `games/manifest.schema.json` validates a stub Enshrouded and Valheim manifest; `openapi.yaml` passes `npx @redocly/cli lint`; mod `Provider` interface has ≥2 planned impls (Thunderstore, Nexus).
- Fail: build fails; manifest schema lacks ports/platform/config-schema/ready-regex fields; API spec lint errors; ADR missing socket-risk section.
- Gotchas: docker.sock = root on host — ADR must document a socket-proxy option (e.g. tecnativa/docker-socket-proxy) and rootless Podman; container paths must map to *host* paths when spawning sibling containers (controller needs a `HOST_DATA_ROOT` env, not its own mount path); keep manifest a declarative format, no embedded scripts.

### T2 — License and repo hygiene
- Score: 1.5 (jr) · Tier: light · Executor: subagent
- Status: done
- Depends: T1
- Wave: 2 · Parallel: yes
- Files: `.gitignore` (w, extend existing), `LICENSE` (w), `NOTICE` (w), `CONTRIBUTING.md` (w), `.editorconfig` (w), `.github/workflows/ci.yml` (w)
- Pass: `LICENSE` is verbatim Apache-2.0 text; CI workflow runs `go vet ./...`, `go test ./...`, UI `npm ci && npm run build`; `actionlint` exits 0.
- Fail: modified license text; CI references nonexistent paths; `.gitignore` misses `.venv/`, `node_modules/`, `data/`.
- Gotchas: do not invent a copyright holder — use `<owner>` placeholder (Q9).

### T3 — steamcmd-base runtime image
- Score: 3 (eng) · Tier: standard · Executor: subagent
- Status: done (build, shellcheck, PUID, signals verified locally; real install → T4V)
- Depends: T1
- Wave: 2 · Parallel: yes
- Files: `images/steamcmd-base/Dockerfile` (w), `images/steamcmd-base/entrypoint.sh` (w)
- Pass: `docker build` on amd64 succeeds; container runs as `PUID:PGID` (default 1000:1000) not root; `entrypoint.sh install <appid> [platform]` runs `+@sSteamCmdForcePlatformType <platform> +force_install_dir /server +login anonymous +app_update <appid> validate +quit` and exits 0 for Valheim (896660); `entrypoint.sh run -- <cmd>` execs cmd as PID 1 child with signal forwarding (`tini`); `shellcheck` clean.
- Fail: files in `/server` owned by root; SIGTERM not forwarded (container takes 10 s to stop); steamcmd self-update loop not handled.
- Gotchas: steamcmd exit code 8 / "Missing configuration" on first run → retry once; image is amd64-only (steamcmd has no arm64); keep steamcmd home in a volume so self-update persists.

### T4 — steamcmd-wine runtime image (Windows servers)
- Score: 4 (sr) · Tier: strong · Executor: main
- Status: done (builds; runtime → T4V — Wine aborts under qemu on 16K-page host)
- Depends: T1
- Wave: 2 · Parallel: no
- Files: `images/steamcmd-wine/Dockerfile` (w), `images/steamcmd-wine/entrypoint.sh` (w)
- Pass: builds FROM steamcmd-base pattern with Wine (64-bit) + `xvfb-run`; `WINEPREFIX` persisted in volume; `entrypoint.sh run -- wine /server/enshrouded_server.exe` starts and on SIGTERM process exits within 30 s; on Unraid host the Enshrouded server reaches log line `[Session] 'HostOnline' (up)!`.
- Fail: wine prompts for Mono/Gecko install and hangs; prefix re-created each start (slow); zombie wineserver keeps container alive after stop.
- Gotchas: server opens a console ("DOS window") — headless needs `xvfb-run` or `WINEDEBUG=-all` + no display check; set `WINEDLLOVERRIDES="mscoree,mshtml="` to skip Mono/Gecko; graceful shutdown of a Windows exe under Wine is not a SIGTERM — verify save survives (saves every 5 min; worst-case loss documented); first `wineboot --init` is slow, do it at image build or first start with a lock.

### T5 — Enshrouded manifest + config schema
- Score: 3 (eng) · Tier: standard · Executor: subagent
- Status: done (schema + template + tests; AppID check → T4V)
- Depends: T1
- Wave: 2 · Parallel: yes
- Files: `games/enshrouded/manifest.yaml` (w), `games/enshrouded/settings.schema.json` (w), `games/enshrouded/enshrouded_server.json.tmpl` (w), `internal/catalog/catalog_test.go` (w), `games/manifest.schema.json` (r), `Enshrouded-Server-Docs/*.pdf` (r)
- Pass: settings schema covers every user-editable key in the "Enshrouded reference" section with exact enum spellings, min/max and `default`s (controller-owned keys `saveDirectory`, `logDirectory`, `ip`, `queryPort` stay in the template only); a test renders the template with schema defaults and the output parses as JSON and matches the official default file's keys; `go test ./internal/catalog/` exits 0; manifest declares platform=windows, image=steamcmd-wine, ports `[{name: query, default: 15637, proto: udp}]`, ready regex `\[Session\] 'HostOnline' \(up\)!`, config path `/server/enshrouded_server.json`, saveDirectory/logDirectory mapped into `data/`; `steamcmd +login anonymous +app_info_print 2278520 +quit` output confirms server app (record result in manifest comment, resolves Q12).
- Fail: enum typo (server won't boot); legacy `password` key emitted; gameSettings rendered with preset≠Custom without UI warning; ns durations stored as float.
- Gotchas: doc PDF text has ligature damage (`enableStarvingDebuf `, `foodBuf DurationFactor`, `fishingDif iculty`) — correct spellings are `enableStarvingDebuff`, `foodBuffDurationFactor`, `fishingDifficulty`; ns values up to 3.6e12 fit in int64/JSON safely but must be integers; perkUpgradeRecyclingFactor default 0.5; userGroup passwords are secrets.

### T4V — Native amd64 runtime verification (Unraid)
- Score: 2.5 (eng) · Tier: standard · Executor: subagent
- Status: done (all checks pass on Unraid 7.3.2; see docs/verification/runtime-images.md)
- Depends: T3, T4, T5
- Wave: 3 · Parallel: no
- Files: `docs/verification/runtime-images.md` (w), `images/**` (r)
- Pass: on Unraid, base image build passes its `/opt/steamcmd/package` guard; `install 896660 --validate` exits 0 and files are PUID-owned; `+app_info_print 2278520` shows the Enshrouded Dedicated Server with windows oslist (update manifest comment); `install 2278520 --platform windows` exits 0; wine image `run -- wine enshrouded_server.exe` reaches `[Session] 'HostOnline' (up)!`; `podman/docker stop` with SIGINT exits within 90 s and the world save exists afterwards. Results recorded in the doc.
- Fail: any step needs root-owned files; stop hits the timeout (then try SIGTERM, record which works and update the manifest stopSignal); save missing after stop.
- Gotchas: steamcmd and Wine cannot run under qemu on Apple Silicon (16K pages) — do not retry locally; ~13 GB download; run via SSH, no secrets in args.

### T6 — Controller core: instances, ports, paths, lifecycle
- Score: 4 (sr) · Tier: strong · Executor: main
- Status: done (store, ports, settings completion, runtime, instance service; Podman integration test passes)
- Depends: T1, T3
- Wave: 3 · Parallel: no
- Files: `internal/store/**` (w), `internal/instance/**` (w), `internal/ports/**` (w), `internal/runtime/**` (w), `internal/games/**` (w)
- Pass: `go test ./internal/...` exits 0 with tests for: manifest loading/validation; port allocator rejects clash with another instance and with a host-bound port; create → container exists with correct labels, mounts (host paths), port maps, mem limit; start/stop/remove idempotent; DB migration from empty; works against Podman socket in CI (`podman system service`).
- Fail: two instances get same port; controller restart loses track of running containers (must reconcile by label); paths computed from container view not host view.
- Gotchas: reconcile on boot via container labels, not DB alone; Enshrouded needs ~6 GB RAM/instance + 30 GB disk — preflight check and warn; UDP port mapping must specify `/udp`; instance names → container names must be sanitized.

### T10a — UI scaffold, dashboard, instance list
- Score: 3 (eng) · Tier: standard · Executor: subagent
- Status: done (SvelteKit 3 / Svelte 5 SPA, mock mode, 24 tests)
- Depends: T1
- Wave: 3 · Parallel: yes
- Files: `web/**` (w), `api/openapi.yaml` (r)
- Pass: `npm run build` emits into `web/dist`; typed API client generated from `openapi.yaml`; dashboard lists instances with state badge, game, ports; create-instance wizard (game → name → ports → paths) posts to API; `npm run check` 0 errors; keyboard-navigable, labels on inputs.
- Fail: hard-coded API types diverge from spec; wizard allows submit with empty/duplicate port.
- Gotchas: API not live yet — use a mock from the OpenAPI examples; keep deps minimal (no UI kit unless needed).

### T14 — docker-compose + Podman deployment
- Score: 2.5 (eng) · Tier: standard · Executor: subagent
- Status: done (compose + socket-proxy override + rootless quadlet; controller runs as PUID:PGID)
- Depends: T1
- Wave: 3 · Parallel: yes
- Files: `deploy/compose.yaml` (w), `deploy/podman/steamcmd-serve.container` (w), `deploy/README.md` (w)
- Pass: `docker compose -f deploy/compose.yaml config` exits 0; `podman compose ... config` exits 0; quadlet passes `systemd-analyze verify` via `/usr/lib/systemd/system-generators/podman-system-generator --dryrun`; `HOST_DATA_ROOT` equals bind-mount host path in every example.
- Fail: socket mounted without the security note; rootless Podman socket path wrong (`$XDG_RUNTIME_DIR/podman/podman.sock`).
- Gotchas: SELinux hosts need `:z` on bind mounts; rootless Podman can't bind ports <1024 (game ports are high, UI port default 8080 fine).

### T15 — Unraid Community Apps template
- Score: 2 (jr) · Tier: light · Executor: subagent
- Status: done (CA template, --user=99:100)
- Depends: T1
- Wave: 3 · Parallel: yes
- Files: `deploy/unraid/steamcmd-serve.xml` (w), `deploy/unraid/icon.png` (w)
- Pass: XML parses (`xmllint --noout`); maps `/var/run/docker.sock`, appdata `/mnt/user/appdata/steamcmd-serve` → `/data`, sets `HOST_DATA_ROOT=/mnt/user/appdata/steamcmd-serve`, WebUI `http://[IP]:[PORT:8080]`, PUID 99 / PGID 100.
- Fail: HOST_DATA_ROOT mismatches the appdata mapping; missing socket-risk text in Overview.
- Gotchas: Unraid defaults are `nobody:users` = 99:100, not 1000:1000.

### T7 — Install/update orchestration, logs, readiness, backups
- Score: 3.5 (sr) · Tier: strong · Executor: main
- Status: done (job containers, readiness, config render + drift, logs, backups/restore, crash reconcile; Podman integration test with fake runtime)
- Depends: T6, T4, T5
- Wave: 4 · Parallel: no
- Files: `internal/jobs/**` (w), `internal/instance/**` (w; extend Service: install/update jobs, `starting` state + readiness, config render in Start), `internal/backup/**` (w), `internal/logs/**` (w)
- Pass: install/update run as one-shot job containers with progress parsed from steamcmd output; config file rendered from DB + schema before every start (existing file imported once on first adopt); state machine `installing → stopped → starting → running(ready regex hit) → stopping`; log stream via SSE; backup = tar.zst of `saveDirectory` on schedule and before update, retention N; tests cover render (enum validation blocks start), state transitions, backup rotation.
- Fail: start allowed while install job runs; server's rewritten config silently overrides UI settings; backup taken mid-write without stopping or snapshotting.
- Gotchas: Enshrouded rewrites its config on start — controller must re-render each start and diff on stop to surface drift; Windows save files may be locked while running → backup from a copy after the 5-min save or after stop; never log userGroup passwords; settings schema does not require top-level keys but the Enshrouded template uses missingkey=error, — already handled: `catalog.Game.CompleteSettings` fills defaults and `x-generate` at create/SetSettings (T6). Start currently goes straight to `running`; T7 adds `starting` until the readiness regex matches.

### T8 — REST handlers per OpenAPI
- Score: 3 (eng) · Tier: standard · Executor: subagent
- Status: done (all routes, kin-openapi response validation, SSE)
- Depends: T6
- Wave: 4 · Parallel: yes
- Files: `internal/api/**` (w), `api/openapi.yaml` (r)
- Pass: every OpenAPI operation has a handler; `go test ./internal/api/...` with httptest covers 2xx + 4xx per route; response bodies validate against spec (kin-openapi validator in tests).
- Fail: handler leaks secrets (userGroup passwords, Nexus key) in GET responses; 500 on bad input instead of 400.
- Gotchas: secrets write-only — return `"***"`/`hasPassword: true`; auth middleware hook point left for T9.

### T9 — Auth + secret handling
- Score: 3.5 (sr) · Tier: strong · Executor: main
- Status: done (argon2id admin, sessions, CSRF, login rate limit, trusted proxies; x-secret masking by x-key + AES-GCM at rest)
- Depends: T6
- Wave: 4 · Parallel: no
- Files: `internal/auth/**` (w), `internal/secrets/**` (w), `internal/api/middleware.go` (w)
- Pass: first-run setup creates admin (argon2id); session cookie `HttpOnly; Secure (when TLS); SameSite=Lax`; CSRF token on mutating routes; login rate-limited; secrets (game passwords, Nexus API key) encrypted at rest with key from env/file; tests for each.
- Fail: any route reachable unauthenticated besides `/login`, `/setup`, `/healthz`; secret present in plaintext in SQLite file (`strings data.db | grep` finds it).
- Gotchas: setup endpoint must lock after first admin exists; behind reverse proxy honour `X-Forwarded-Proto` only when `TRUSTED_PROXIES` set.

### T10b — Schema-driven config editor
- Score: 3 (eng) · Tier: standard · Executor: subagent
- Status: done (schema-driven form, Ajv validation, 39 web tests)
- Depends: T5, T10a
- Wave: 4 · Parallel: yes
- Files: `web/src/lib/config-form/**` (w), `games/enshrouded/settings.schema.json` (r)
- Pass: renders any game's JSON Schema into form (number+range slider, enum select, bool toggle, array of objects for userGroups); ns-duration fields shown/edited in minutes; gameSettings section disabled with note unless preset=Custom; client validation matches schema; component tests pass.
- Fail: free-text input for enum fields; minutes→ns conversion produces float.
- Gotchas: use `x-unit: ns` / `x-ui` schema hints added in T5 rather than Enshrouded-specific code.

### T10c — Live log / console view
- Score: 2.5 (eng) · Tier: standard · Executor: subagent
- Status: done (logs SSE view + backups page; 56 web tests)
- Depends: T8, T10a
- Wave: 5 · Parallel: yes
- Files: `web/src/routes/instances/[id]/logs/**` (w)
- Pass: SSE stream renders with auto-scroll toggle, pause, search; reconnects after drop; caps buffer (e.g. 5k lines).
- Fail: unbounded DOM growth; duplicate lines after reconnect.
- Gotchas: SSE uses cookie auth — same-origin only.

### T11 — Mod framework
- Score: 4 (sr) · Tier: strong · Executor: main
- Status: done (staging + BepInEx layout + apply/undo record; deps tracked via requiredBy; reapply after update; mod env; API routes; Podman integration test with fake provider)
- Depends: T6, T7
- Wave: 5 · Parallel: no
- Files: `internal/mods/**` (w), `games/valheim/manifest.yaml` (w)
- Pass: per-instance mod set stored in DB; install pipeline download → verify hash → stage → apply to instance `server/` per manifest `modLoader` (e.g. BepInEx) only while stopped; uninstall restores clean state; Valheim manifest + BepInEx loader install tested with a fake provider.
- Fail: mod files written while server running; uninstall leaves files; zip-slip possible (`../` entries).
- Gotchas: validate archive paths; Steam `validate` on update may wipe loader files → reapply mods after update; Enshrouded manifest declares `mods: none` until a server-side scene exists.

### T16 — Image build + release CI
- Score: 3 (eng) · Tier: standard · Executor: subagent
- Status: done (controller image 19.8 MB distroless nonroot; release workflow; CI integration job)
- Depends: T3, T4, T6
- Wave: 5 · Parallel: yes
- Files: `.github/workflows/release.yml` (w), `images/controller/Dockerfile` (w)
- Pass: controller image multi-arch (amd64, arm64); runtime images amd64; pushes to `ghcr.io/<owner>/…` on tag; `actionlint` clean; controller image distroless/non-root.
- Fail: runtime images published as arm64 (steamcmd won't run); controller image > 50 MB.
- Gotchas: controller needs socket group access — document `group_add` for docker GID.

### T12 — Thunderstore provider
- Score: 3 (eng) · Tier: standard · Executor: subagent
- Status: done (index cache w/ conditional GET + singleflight, semver dedupe, URL allowlist; shape verified live)
- Depends: T11
- Wave: 6 · Parallel: yes
- Files: `internal/mods/thunderstore/**` (w), `internal/mods/provider.go` (r), `cmd/controller/main.go` (w: register in Options.ModProviders)
- Pass: search + fetch package by community (`valheim`), resolve dependency tree with version pins, download zip; tests against recorded fixtures (no live network in unit tests).
- Fail: dependency cycles hang; full community index downloaded on every search (cache it with ETag).
- Gotchas: community package index is large — cache to disk, refresh daily; BepInExPack is itself a dependency.

### T13 — NexusMods provider
- Score: 3 (eng) · Tier: standard · Executor: subagent
- Status: done (keyring: validate-then-seal, offline status; GraphQL search verified live; REST v1 shapes assumed — needs a real key to verify)
- Depends: T11
- Wave: 6 · Parallel: yes
- Files: `internal/mods/nexus/**` (w), `internal/mods/provider.go` (r), `internal/api/` nexus-api-key routes (w, replace 501), `cmd/controller/main.go` (w)
- Pass: API key validated via `/v1/users/validate.json`; mod/file listing; premium → direct download link; non-premium → UI flow to upload the file manually; respects rate-limit headers; fixture tests.
- Fail: API key logged or returned; non-premium path errors instead of falling back.
- Gotchas: Nexus requires an app `User-Agent`/`Application-Name` header; Nexus ToS forbids scraping download pages.

### T17 — E2E smoke on Unraid
- Score: 3 (eng) · Tier: standard · Executor: subagent
- Status: done (PASS on Unraid 7.3.2: 2 Enshrouded instances, A2S on 15637/15638, logs, stop 9 s, backup, clean delete)
- Depends: T7, T8, T9, T15
- Wave: 6 · Parallel: no
- Files: `test/e2e/enshrouded.sh` (w)
- Pass: script deploys controller on Unraid via template settings, creates Enshrouded instance via API, installs, starts, sees ready marker within 10 min, A2S_INFO on UDP 15637 answers with server name, stops cleanly, backup file exists; second instance on 15647 runs concurrently.
- Fail: any step times out; port collision; leftover containers after teardown.
- Gotchas: ~13 GB download — reuse install volume across runs; binds real ports on Unraid — never parallel; run via SSH, no secrets in script args.

### T10d — Mods UI
- Score: 3 (eng) · Tier: standard · Executor: subagent
- Status: done (mods page, Nexus key settings, manual upload flow; 71 web tests)
- Depends: T12, T13, T10a
- Wave: 7 · Parallel: yes
- Files: `web/src/routes/instances/[id]/mods/**` (w), `web/src/routes/settings/**` (w)
- Pass: search/install/remove mods per instance; Nexus key entry (write-only); manual upload path; disabled with explanation for games with `mods: none`.
- Fail: install button active while instance running.
- Gotchas: show dependency list before install.

### T18 — Docs
- Score: 1.5 (jr) · Tier: light · Executor: subagent
- Status: done (README, install guides for Docker/Podman/Unraid, Enshrouded guide; markdownlint clean)
- Depends: T14, T15, T17
- Wave: 7 · Parallel: yes
- Files: `README.md` (w), `docs/install-*.md` (w), `docs/games/enshrouded.md` (w)
- Pass: README quickstart for Docker/Podman/Unraid; Enshrouded guide lists ports, RAM/disk needs, preset vs Custom behavior, userGroups; socket security warning present; `markdownlint` clean.
- Fail: commands that don't match `deploy/` files.
- Gotchas: copy facts from the "Enshrouded reference" section, don't re-derive.

### T19 — (stretch) Cross-platform thin client
- Score: 3 (eng) · Tier: standard · Executor: subagent
- Status: done with known limitation — connect page and error paths verified on a MacBook Air; unsigned macOS builds get no Local Network grant (macOS 15+), so LAN controllers are unreachable from the app on macOS until it is Developer ID signed (documented; app shows a hint)
- Depends: T8, T9
- Wave: 8 · Parallel: yes
- Files: `client/**` (w)
- Pass: Wails app connects to a configured controller URL, shows instance list + start/stop, builds on macOS/Windows/Linux in CI.
- Fail: stores session token in plaintext config file.
- Gotchas: reuse web UI build where possible rather than a second UI.

## Waves
W1: T1 · W2: T2 T3 T5 T4(main) · W3: T6(main) T10a T14 T15 T4V(no-parallel, blocked) · W4: T7(main) T9(main) T8 T10b · W5: T11(main) T10c T16 · W6: T12 T13 T17(no-parallel) · W7: T10d T18 · W8: T19

## Audit fixes applied
1. T5 sourced from official PDFs; ligature-damaged keys corrected.
2. Q12 added: SteamCMD server AppID ≠ game AppID 1203620.
3. T7 owns config rendering because server rewrites its own file.
4. T7 readiness uses official log marker.
5. T6 resource preflight (6 GB RAM, 30 GB disk) from FAQ.
6. T17 moved to Unraid, Parallel: no, blocked on Q11; depends on T15.
7. T11 now depends on T7 (mods reapplied after updates).
8. T10b depends on T5 schema hints (`x-unit`, `x-ui`).

## Cost note
23 tasks: 3 jr · 14 eng · 5 sr · 1 ar. 17/23 (74%) on light/standard tiers; main agent keeps T1, T4, T6, T7, T9, T11.
