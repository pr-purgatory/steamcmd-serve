#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
usage:
  entrypoint.sh install <appid> [--platform linux|windows] [--branch NAME] [--validate]
  entrypoint.sh run [--workdir DIR] -- CMD [ARGS...]
USAGE
  exit 64
}

PUID="${PUID:-1000}"
PGID="${PGID:-1000}"
export PUID PGID

# --- privilege setup (root only) ------------------------------------------
if [ "$(id -u)" = "0" ]; then
  if getent group steam >/dev/null; then
    [ "$(getent group steam | cut -d: -f3)" = "$PGID" ] || groupmod -o -g "$PGID" steam
  else
    groupadd -o -g "$PGID" steam
  fi
  if id steam >/dev/null 2>&1; then
    usermod -o -u "$PUID" -g "$PGID" -d /steamcmd steam
  else
    useradd -o -u "$PUID" -g "$PGID" -d /steamcmd -M -s /bin/bash steam
  fi
  for d in /steamcmd /server /data; do
    [ -d "$d" ] || continue
    if [ "$(stat -c '%u:%g' "$d")" != "$PUID:$PGID" ]; then
      chown -R "$PUID:$PGID" "$d"
    fi
  done
  export HOME=/steamcmd
  exec setpriv --reuid="$PUID" --regid="$PGID" --init-groups \
    "$0" "$@"
fi

export HOME="${HOME:-/steamcmd}"

# --- subcommands ----------------------------------------------------------
cmd_install() {
  [ $# -ge 1 ] || usage
  local appid="$1" platform="" branch="" validate=0
  shift
  case "$appid" in ''|*[!0-9]*) echo "invalid appid: $appid" >&2; exit 64 ;; esac
  while [ $# -gt 0 ]; do
    case "$1" in
      --platform) [ $# -ge 2 ] || usage; platform="$2"; shift 2 ;;
      --branch)   [ $# -ge 2 ] || usage; branch="$2"; shift 2 ;;
      --validate) validate=1; shift ;;
      *) usage ;;
    esac
  done
  case "$platform" in ''|linux|windows) ;; *) usage ;; esac

  local args=()
  [ -z "$platform" ] || args+=("+@sSteamCmdForcePlatformType" "$platform")
  args+=("+force_install_dir" "/server" "+login" "anonymous" "+app_update" "$appid")
  [ -z "$branch" ] || args+=("-beta" "$branch")
  [ "$validate" = 0 ] || args+=("validate")
  args+=("+quit")

  # steamcmd self-updates into its own directory, so run a writable copy kept in the
  # /steamcmd volume; the image's /opt/steamcmd is only the seed.
  local steamcmd=/steamcmd/steamcmd/steamcmd.sh
  if [ ! -x "$steamcmd" ]; then
    mkdir -p /steamcmd/steamcmd
    cp -a /opt/steamcmd/. /steamcmd/steamcmd/
  fi

  local rc=0
  "$steamcmd" "${args[@]}" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "steamcmd exited with $rc; retrying once" >&2
    rc=0
    "$steamcmd" "${args[@]}" || rc=$?
  fi
  exit "$rc"
}

cmd_run() {
  local workdir=/server
  while [ $# -gt 0 ]; do
    case "$1" in
      --workdir) [ $# -ge 2 ] || usage; workdir="$2"; shift 2 ;;
      --) shift; break ;;
      *) usage ;;
    esac
  done
  [ $# -ge 1 ] || usage
  case "$workdir" in /*) ;; *) workdir="/server/$workdir" ;; esac
  cd "$workdir"

  local f
  for f in /usr/local/lib/steamcmd-serve/run.d/*.sh; do
    [ -e "$f" ] || continue
    # shellcheck disable=SC1090
    . "$f"
  done
  exec "$@"
}

[ $# -ge 1 ] || usage
sub="$1"
shift
case "$sub" in
  install) cmd_install "$@" ;;
  run)     cmd_run "$@" ;;
  *)       usage ;;
esac
