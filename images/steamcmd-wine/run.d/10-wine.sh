# shellcheck shell=bash
# Sourced by entrypoint.sh `run` before exec, as the unprivileged user.
# Starts a virtual display (the server opens a console window) and initializes the Wine prefix once.

# A restarted container keeps /tmp; clear the stale lock from the previous run.
rm -f "/tmp/.X${DISPLAY#:}-lock"
Xvfb "${DISPLAY}" -screen 0 640x480x16 -nolisten tcp >/dev/null 2>&1 &

if [[ ! -f "${WINEPREFIX}/system.reg" ]]; then
  echo "steamcmd-wine: initializing Wine prefix in ${WINEPREFIX} (first start only)" >&2
  wineboot --init
  wineserver --wait
fi
