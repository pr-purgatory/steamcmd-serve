# Enshrouded

This guide covers running an Enshrouded dedicated server with steamcmd-serve.
The server is a Windows program. steamcmd-serve runs it under Wine in the
`steamcmd-wine` image.

Enshrouded has no server-side mod support in steamcmd-serve. Mods are not
available for this game.

## Requirements

- An x86_64 host. Game servers do not run on arm64 hosts.
- Install size: about 7.6 GB per instance. Measured on 2026-10-21. The
  official FAQ says about 13 GB and recommends keeping 30 GB free. The game's
  manifest asks for 30 GB.
- Memory: the official figure is about 4.4 GB when idle, plus about 100 MB per
  player, up to 6 GB. steamcmd-serve sets an 8 GB memory limit for each
  instance by default.
- CPU: the official recommendation for 16 players is 8 cores and 16 threads.

## Ports and firewall

Each instance uses one UDP port, `15637/udp` by default. The server uses IPv4
only.

- The first instance uses `15637`. The next instance gets `15638` if it is
  free. You can choose another port when you create the instance.
- The controller refuses a port that another instance uses, or that a running
  container uses.
- Forward or allow the port for each instance on your router and host firewall.
  Only the web UI port (`8080`) is published by the controller itself.

## Create an instance

1. Open the web UI and create a new instance. Pick **Enshrouded**, give the
   instance a name and choose its ports.
2. **Install.** steamcmd downloads the Windows server and the progress is shown
   in the UI. The install goes to the games path, under `enshrouded/<instance
   id>/`.
3. Open **Settings** and review the settings below. You can change them before
   the first start. Passwords for the user groups are created for you.
4. **Start.** The controller waits for the game to report that the server is
   up. On Unraid, this took about 50 seconds after the container started.

The start waits for the game's ready log line. The timeout for that wait is 900
seconds, as set in the game's manifest.

## Controls

| Action | What it does |
|---|---|
| Start | Renders the config file from your settings, then starts the server. |
| Stop | Sends `SIGINT`. The game saves the world on shutdown. |
| Restart | Stops, then starts the server. |
| Update | Backs up the saves, then updates the server files. |
| Logs | Shows the live log. |
| Backups | Creates, lists and restores backups. |
| Delete | Removes the instance. You can also delete its data. |

The stop timeout is 90 seconds. Settings apply on the next start. If the server
is running, stop or restart it to apply a change.

## Settings

The settings form comes from the game's settings schema. Keys are shown here
exactly as they appear in the API. The controller owns the server paths, the
IP address and the query port. You cannot set them.

### Top-level settings

| Key | Setting | Type | Default |
|---|---|---|---|
| `name` | Server name | string | `Enshrouded Server` |
| `slotCount` | Player slots | integer, 1 to 16 | `16` |
| `tags` | Tags | list of strings | empty |
| `voiceChatMode` | Voice chat mode | `Proximity` or `Global` | `Proximity` |
| `enableVoiceChat` | Voice chat | boolean | `false` |
| `enableTextChat` | Text chat | boolean | `false` |
| `gameSettingsPreset` | Difficulty preset | one of five presets | `Default` |
| `gameSettings` | Custom game settings | object, see below | see below |
| `userGroups` | User groups | list, see below | see below |
| `bans` | Bans | list of player IDs | empty |

`Default` is the vanilla game. `Custom` is the only preset that applies the
values in `gameSettings`. With any other preset, the form disables those fields.

### Custom game settings

These keys go under `gameSettings`. Each one applies only when
`gameSettingsPreset` is `Custom`.

The hunger, day and night durations are shown in minutes in the UI. The stored
value is in nanoseconds.

| Key | Setting | Default | Range or options |
|---|---|---|---|
| `playerHealthFactor` | Player health | `1` | 0.25 to 4 |
| `playerManaFactor` | Player mana | `1` | 0.25 to 4 |
| `playerStaminaFactor` | Player stamina | `1` | 0.25 to 4 |
| `playerBodyHeatFactor` | Body heat against cold | `1` | 0.5 to 2 |
| `playerDivingTimeFactor` | Diving time | `1` | 0.5 to 2 |
| `enableDurability` | Weapon durability | `true` | true / false |
| `enableStarvingDebuff` | Hunger and starvation | `false` | true / false |
| `foodBuffDurationFactor` | Food buff duration | `1` | 0.5 to 2 |
| `fromHungerToStarving` | Hungry state duration | 10 min | 5 min to 20 min |
| `shroudTimeFactor` | Shroud time | `1` | 0.5 to 2 |
| `tombstoneMode` | Tombstone mode | `AddBackpackMaterials` | see list |
| `enableGliderTurbulences` | Glider turbulences | `true` | true / false |
| `weatherFrequency` | Weather frequency | `Normal` | see list |
| `fishingDifficulty` | Fishing difficulty | `Normal` | see list |
| `miningDamageFactor` | Mining effectiveness | `1` | 0.5 to 2 |
| `plantGrowthSpeedFactor` | Plant growth speed | `1` | 0.25 to 2 |
| `resourceDropStackAmountFactor` | Resource gain | `1` | 0.25 to 2 |
| `factoryProductionSpeedFactor` | Workstation effectiveness | `1` | 0.25 to 2 |
| `perkUpgradeRecyclingFactor` | Weapon recycling yield | `0.5` | 0 to 1 |
| `perkCostFactor` | Weapon upgrade cost | `1` | 0.25 to 2 |
| `experienceCombatFactor` | Combat experience | `1` | 0.25 to 2 |
| `experienceMiningFactor` | Mining experience | `1` | 0 to 2 |
| `experienceExplorationQuestsFactor` | Exploration XP | `1` | 0.25 to 2 |
| `randomSpawnerAmount` | Enemy amount | `Normal` | see list |
| `aggroPoolAmount` | Simultaneous enemy attacks | `Normal` | see list |
| `enemyDamageFactor` | Enemy damage | `1` | 0.25 to 5 |
| `enemyHealthFactor` | Enemy health | `1` | 0.25 to 4 |
| `enemyStaminaFactor` | Enemy stun resistance | `1` | 0.5 to 2 |
| `enemyPerceptionRangeFactor` | Enemy perception | `1` | 0.5 to 2 |
| `bossDamageFactor` | Boss damage | `1` | 0.2 to 5 |
| `bossHealthFactor` | Boss health | `1` | 0.2 to 5 |
| `threatBonus` | Enemy attack frequency | `1` | 0.25 to 4 |
| `pacifyAllEnemies` | Pacified enemies | `false` | true / false |
| `tamingStartleRepercussion` | Taming failure | `LoseSomeProgress` | see list |
| `dayTimeDuration` | Daytime length | 30 min | 2 min to 60 min |
| `nightTimeDuration` | Nighttime length | 12 min | 2 min to 60 min |
| `curseModifier` | Shroud curse | `Normal` | `Easy` / `Normal` / `Hard` |

The rows marked "see list" take one of these values:

- `tombstoneMode`: `AddBackpackMaterials`, `Everything`, `NoTombstone`.
- `weatherFrequency`: `Disabled`, `Rare`, `Normal`, `Often`.
- `fishingDifficulty`: `VeryEasy`, `Easy`, `Normal`, `Hard`, `VeryHard`.
- `randomSpawnerAmount`: `Few`, `Normal`, `Many`, `Extreme`.
- `aggroPoolAmount`: `Few`, `Normal`, `Many`, `Extreme`.
- `tamingStartleRepercussion`: `KeepProgress`, `LoseSomeProgress`,
  `LoseAllProgress`.

### User groups and passwords

`userGroups` controls what a player can do, depending on the password they
enter when they join. Each group has these fields:

- `name`: the group name. Names must be unique.
- `password`: the password players enter to join this group. The controller
  generates it at instance creation. Edit it in **Settings** if you want your
  own. The API never returns stored passwords.
- `canKickBan`, `canAccessInventories`, `canEditWorld`, `canEditBase`,
  `canExtendBase`: permissions as true or false.
- `reservedSlots`: the number of player slots reserved for this group.

By default there are four groups: `Admin`, `Friend`, `Guest` and `Visitor`.
Each has a generated password. Give the password for `Friend` only to people you
want to have that access.

The single `password` key of the server config is not used. Passwords belong
to the user groups.

## Saves and backups

- **Saves.** The world is saved every 5 minutes into the `savegame` folder of
  the instance's data directory. That folder is separate from the game install,
  so updating or reinstalling the server does not remove your saves.
- **Logs.** The server logs are kept in the `logs` folder of the same data
  directory.
- **Backups.** The controller makes a backup:
  - when you choose **Backups** and create one,
  - automatically every `BACKUP_INTERVAL` (default `6h`) for running instances,
  - before each update and each restore.

  The controller keeps the newest 20 backups.
- **Restore.** A restore needs the server stopped. The controller backs up the
  current data before it restores.

## Updating

**Update** backs up the saves first, then updates the server files from Steam.
Enshrouded has no mods, so nothing else is reapplied.

## Connecting

Players can find the server in the in-game server browser. Turn on the
**Show dedicated servers** filter. You can also add `IP:15637` as a Steam
favourite. Use the port of the instance if it is not `15637`.

## Known notes

- **Exit code 1 after stop.** After a clean stop, the container exits with code
  1, after a line about a broken connection to display `:99`. The world is
  already saved. The controller does not treat this exit code as an error.
- **Analytics errors.** The game tries to send analytics to
  `enshrouded-analytics.keengames.com`. If that request fails, the server keeps
  running.
- **Plain-text passwords in the config file.** The game needs the passwords in
  `enshrouded_server.json`, so the controller writes them there in plain text.
  The file is in the game install folder, next to the server executable. Copies
  of the last rendered file are in the instance's data folder. Protect the games
  and data directories.
- **Config file changes.** The game rewrites its own config file. The
  controller renders the file from your settings on every start, so hand
  edits are overwritten on the next start. If the game changes the file while
  the server runs, the UI tells you.
