#!/usr/bin/env bash
# End-to-end test on a Docker host (built for Unraid): builds all images from this
# checkout, starts the controller the way the Unraid template does, and runs driver.py
# (two Enshrouded instances, ~8 GB download each). Uses separate -e2e directories.
#
#   test/e2e/enshrouded.sh            run, then clean up
#   KEEP=1 test/e2e/enshrouded.sh     leave data, containers and images for inspection
set -euo pipefail

SRC=$(cd "$(dirname "$0")/../.." && pwd)
DATA=${DATA:-/mnt/user/appdata/steamcmd-serve-e2e}
GAMES=${GAMES:-/mnt/user/games/steamcmd-serve-e2e}
PUID=${PUID:-99}
PGID=${PGID:-100}
PORT=${PORT:-18080}
REPO=localhost/ss-e2e
TAG=e2e
NAME=steamcmd-serve-e2e
DOCKER_GID=$(stat -c %g /var/run/docker.sock)

# shellcheck disable=SC2329 # invoked via trap
cleanup() {
  [[ "${KEEP:-}" == 1 ]] && { echo "KEEP=1: leaving $NAME, $DATA, $GAMES and $REPO/* images"; return; }
  docker rm -f "$NAME" >/dev/null 2>&1 || true
  docker ps -aq --filter label=io.steamcmd-serve.managed=true | xargs -r docker rm -f >/dev/null
  rm -rf "$DATA" "$GAMES"
  docker images --format '{{.Repository}}:{{.Tag}}' | grep "^$REPO/" | xargs -r docker rmi >/dev/null || true
}
trap cleanup EXIT

if docker ps -q --filter label=io.steamcmd-serve.managed=true | grep -q .; then
  echo "managed containers already running on this host; refusing to run E2E" >&2
  exit 1
fi

echo "== build images"
docker build -q -t "$REPO/steamcmd-base:$TAG" "$SRC/images/steamcmd-base"
docker build -q --build-arg BASE_IMAGE="$REPO/steamcmd-base:$TAG" -t "$REPO/steamcmd-wine:$TAG" "$SRC/images/steamcmd-wine"
docker build -q -f "$SRC/images/controller/Dockerfile" --build-arg IMAGE_REPO="$REPO" --build-arg VERSION="$TAG" \
  -t "$REPO/steamcmd-serve:$TAG" "$SRC"

echo "== start controller"
mkdir -p "$DATA" "$GAMES"
chown "$PUID:$PGID" "$DATA" "$GAMES"
docker run -d --name "$NAME" --user "$PUID:$PGID" --group-add "$DOCKER_GID" \
  -p "127.0.0.1:$PORT:8080" \
  -v "$DATA:/data" -v "$GAMES:/games" -v /var/run/docker.sock:/var/run/docker.sock \
  -e DATA_DIR=/data -e HOST_DATA_ROOT="$DATA" -e GAMES_DIR=/games -e HOST_GAMES_ROOT="$GAMES" \
  -e PUID="$PUID" -e PGID="$PGID" -e TZ=UTC \
  "$REPO/steamcmd-serve:$TAG" >/dev/null
for _ in $(seq 30); do
  if curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then break; fi
  sleep 1
done
curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null || { docker logs "$NAME"; exit 1; }

echo "== driver"
status=0
docker run --rm --network host -v "$SRC/test/e2e/driver.py:/driver.py:ro" python:3.13-alpine \
  python -u /driver.py "http://127.0.0.1:$PORT" || status=$?

echo "== checks after driver"
left=$(docker ps -aq --filter label=io.steamcmd-serve.managed=true | wc -l)
echo "managed containers left: $left"
[[ "$left" == 0 ]] || status=1
echo "files not owned by $PUID under $DATA: $(find "$DATA" ! -uid "$PUID" | wc -l)"
docker logs "$NAME" 2>&1 | grep -E 'level=(ERROR|WARN)' | tail -10 || true
exit "$status"
