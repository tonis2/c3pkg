# C3 Packages

A package registry for [C3](https://c3-lang.org) libraries, in the spirit of crates.io. People upload
`.c3l` files (a zip with a `manifest.json` and `.c3` sources) so others can find and download them.
Live at https://www.c3pkg.eu.

- **Browse and search**: latest, most downloaded and top rated lists, full-text search, tags.
- **Package pages**: README, versions, size, supported C3 version, platforms, checksums, docs and repository links, owner.
- **Publishing**: upload from the website or the JSON API. Versions are semver, immutable, and can be yanked.
- **Accounts**: email and password, or GitHub. API tokens for scripts and CI.
- **Moderation**: reports and an admin page to remove packages and ban users.
- **Machine-friendly**: a JSON API and a plain `/index/` endpoint for a future command-line client. See `/docs/api` on a running instance.

## How it is built

Go with the standard library's `net/http`, SQLite (pure Go driver, FTS5 for search) and uploaded files stored
on disk by SHA-256. Pages are rendered by Go templates; small [Lit](https://lit.dev) components (search
suggestions, login pop-up, drag-and-drop upload, star and copy buttons) enhance them. There is no front-end
build step, and the pages work without JavaScript. Lit is vendored in `web/static/vendor/lit`
(BSD-3-Clause, license header in the file).

```
cmd/c3pkg/        entry point and the admin, backup and healthcheck subcommands
internal/         auth, store (SQL), pkg (archive validation, semver, README), storage, web (handlers)
migrations/       SQL schema, applied on start-up
web/              templates, CSS and the Lit components (embedded in the binary)
web/test/         browser tests (Playwright driving Chromium)
deploy/           container start script, Caddy snippet, backup cron
design/           the Figma-style design (website.fig, made with open-pencil)
docs/             background notes on package management for C3
```

## Run it locally

```sh
cp .env.example .env        # fill in C3PKG_SESSION_SECRET if you want https, GitHub keys if you want GitHub login
go run ./cmd/c3pkg          # http://localhost:8080, data in ./data
go run ./cmd/c3pkg admin grant you@example.com    # after registering: makes you an admin
```

## Test

```sh
make test            # go vet + go test
make browser-test    # Chromium tests of the Lit components; needs Chromium (see web/test/e2e.test.mjs)
```

## Deploy

See [DEPLOY.md](DEPLOY.md): one container behind a reverse proxy, `make deploy SERVER=root@your-host`.

## How a `.c3l` is checked on upload

The zip must have `manifest.json` at its root with a `provides` name (which becomes the package name) and
`sources` that match at least one `.c3` file. Paths are checked (no `..`, absolute paths or symlinks), and
file count, compressed and unpacked size are limited, so a zip bomb is stopped while reading. The version and
supported C3 version come from the upload form because the manifest does not carry them.

Each version records two SHA-256 values: of the uploaded file, and of its contents (sorted
`<sha256>  <path>` lines), so a zipped library and its unpacked directory give the same value.
