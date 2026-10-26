# Deploying c3pkg

Production runs on the shared server as one container behind the existing Caddy.

| Thing | Where |
|---|---|
| Container | `c3pkg`, network `geogame_web`, IP `10.20.0.10`, listens on 8080 |
| Image | built on the server from the source in `/srv/c3pkg/src` (tag `c3pkg:latest`) |
| Data | `/srv/c3pkg/data` (SQLite `c3pkg.db` + `uploads/`), owned by uid 65532 |
| Backups | `/srv/c3pkg/backups`, written nightly by `/etc/cron.d/c3pkg-backup` |
| Config | `/srv/c3pkg/.env` (mode 600), see below |
| Caddy | site block in `/root/loodus/Caddyfile` (the file bind-mounted into the `caddy` container) |
| URL | https://www.c3pkg.eu |

## Redeploy

```sh
make deploy SERVER=root@your-host   # tests, uploads the source, builds the image on the server, restarts the container
make logs   SERVER=root@your-host
```

## `/srv/c3pkg/.env`

```
C3PKG_BASE_URL=https://www.c3pkg.eu
C3PKG_SESSION_SECRET=<32+ random characters: openssl rand -hex 32>
GITHUB_CLIENT_ID=...
GITHUB_CLIENT_SECRET=...
```

Optional: `C3PKG_MAX_UPLOAD_MB` (20), `C3PKG_MAX_UNZIPPED_MB` (200), `C3PKG_MAX_FILES` (5000),
`C3PKG_TRUSTED_PROXIES` (private ranges; the networks whose `X-Forwarded-For` is believed).
The GitHub OAuth app's callback URL must be `https://www.c3pkg.eu/auth/github/callback`.

## First-time setup (already done)

```sh
mkdir -p /srv/c3pkg/data /srv/c3pkg/backups && chown 65532:65532 /srv/c3pkg/data /srv/c3pkg/backups
# copy .env to /srv/c3pkg/.env, chmod 600
make deploy
cp -p /root/loodus/Caddyfile /root/loodus/Caddyfile.bak.$(date +%Y%m%d-%H%M%S)
cat deploy/Caddyfile.snippet >> /root/loodus/Caddyfile      # append in place: the file is a single-file bind mount
docker exec caddy caddy validate --config /etc/caddy/Caddyfile
docker exec caddy caddy reload   --config /etc/caddy/Caddyfile
cp deploy/backup.cron /etc/cron.d/c3pkg-backup
```

## Administration

```sh
docker exec c3pkg /c3pkg admin grant you@example.com     # make a registered user an admin (then open /admin)
docker exec c3pkg /c3pkg admin revoke you@example.com
docker exec c3pkg /c3pkg backup /backups                  # snapshot now
docker logs --tail 100 c3pkg
```

## Restore

```sh
docker stop c3pkg
cp /srv/c3pkg/backups/c3pkg-YYYYMMDD-HHMMSS.db /srv/c3pkg/data/c3pkg.db
rm -f /srv/c3pkg/data/c3pkg.db-wal /srv/c3pkg/data/c3pkg.db-shm
chown 65532:65532 /srv/c3pkg/data/c3pkg.db
sh /srv/c3pkg/src/deploy/run.sh
```

Backups are on the same disk as the data. Copy `/srv/c3pkg/backups` off the server regularly.
