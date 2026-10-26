#!/bin/sh
# Start (or restart) the c3pkg container from the image c3pkg:latest. Runs on the server.
# Hardened: non-root, read-only root filesystem, no capabilities, memory and process limits.
set -eu

NAME=c3pkg
NET=${C3PKG_NETWORK:-geogame_web}
IP=${C3PKG_IP:-10.20.0.10}
IMAGE=${C3PKG_IMAGE:-c3pkg:latest}

docker rm -f "$NAME" >/dev/null 2>&1 || true
docker run -d --name "$NAME" --restart unless-stopped \
  --network "$NET" --ip "$IP" \
  --read-only --tmpfs /tmp:rw,nosuid,size=64m \
  --cap-drop=ALL --security-opt no-new-privileges \
  --memory=512m --memory-swap=512m --pids-limit=200 \
  --log-opt max-size=10m --log-opt max-file=3 \
  -v /srv/c3pkg/data:/data -v /srv/c3pkg/backups:/backups \
  --env-file /srv/c3pkg/.env \
  "$IMAGE" >/dev/null

i=0
while [ $i -lt 40 ]; do
  status=$(docker inspect -f '{{.State.Health.Status}}' "$NAME" 2>/dev/null || echo none)
  if [ "$status" = healthy ]; then echo "c3pkg is healthy on $IP:8080"; exit 0; fi
  i=$((i + 1)); sleep 1
done
docker logs --tail 50 "$NAME" >&2
echo "c3pkg did not become healthy" >&2
exit 1
