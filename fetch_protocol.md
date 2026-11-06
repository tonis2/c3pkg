# The `c3c fetch` registry protocol

This describes what `c3c fetch`, `c3c project fetch`, `c3c fetch-list` and `c3c fetch-info` expect
from a library registry, and what they do with it, as of c3c 0.8.6. The client code is in
`src/build/libraries.c` (`fetch_lib`, `load_fetch_info`, `registry_fetch_single`, `download_library`),
`src/build/project_manipulation.c` (`fetch_project`, `resolve_fetch`) and `src/compiler/compiler.c`
(`fetch_info`, `fetch_list`, `registry_find_library`).

**c3pkg does not serve `/index.json`**, so against c3pkg `c3c fetch-list` fails, and so does a bare
name in `project fetch`. Dependencies must be written in full; see "`project fetch`" below.

A registry is a plain HTTP server that answers three `GET` requests. Everything is addressed by
path, nothing needs a query string, a request body or a custom header, so a registry can be a
directory of static files behind any web server, a CDN or an `rsync`'d mirror.

## Commands

| Command | Does |
|---|---|
| `c3c fetch-list` | Lists the `<namespace>/<name>` of every library. |
| `c3c fetch-list <text>` | Libraries whose id contains `<text>`; shows description, latest version, versions, updated. |
| `c3c fetch-list tag:<tag>` | Libraries with exactly that tag; shows description only. |
| `c3c fetch-info <ns>/<name>` | Full record for one library, including every version. |
| `c3c fetch <ns>/<name>[@<version>] ...` | Downloads and installs one or more libraries. |
| `c3c project fetch` | Lists the dependencies that are missing. Downloads nothing. |
| `c3c project fetch <library> ...` | Downloads the named dependencies of the project. |

Options:

- `--fetch-url <url>`: registry base URL (default `DEFAULT_FETCH_URL` in `src/build/build.h`,
  currently the development placeholder `http://localhost:8080`). The request path is appended
  directly to it, so give it **without a trailing slash**. A base path is fine:
  `--fetch-url https://example.org/c3/v1`.
- `--decompress=<yes|no>`: `yes` (default) unpacks the library into a `<name>.c3l` directory,
  `no` keeps the downloaded zip as a `<name>.c3l` file.
- `--path <dir>`: applied before the project file is looked up (as for `vendor-fetch`).

All of them need libcurl (or WinHTTP on Windows). Requests are plain `GET`s with the user agent
`C3C/1.0`, redirects are followed, and any HTTP status of 400 or above is a failure (the body is
never parsed for an error message).

## Requests

| Request | Used by | Response |
|---|---|---|
| `GET /index.json` | `fetch-list`, bare names in `project fetch` | JSON, the catalog |
| `GET /info/<ns>/<name>` | `fetch-info`, `fetch` | JSON, one library |
| `GET /download/<ns>/<name>@<version>` | `fetch` | The `.c3l` bytes (zip) |

`fetch` always makes two requests: `/info` to resolve the version, then `/download` with the
**explicit** version. The client never sends a bare `/download/<ns>/<name>` or `@latest`.

Servers are encouraged to also answer those two (both returning the current latest, so a browser
or `curl -OJ` just works) but `c3c` does not depend on them.

### `GET /index.json`

```json
{
  "libraries": [
    {
      "library": "raylib/raylib",
      "description": "Bindings for raylib 6.2.",
      "tags": ["graphics", "gamedev", "bindings"],
      "latest": "0.4.0",
      "updated": "2026-10-13T18:22:05Z",
      "versions": ["0.4.0", "0.3.1"]
    }
  ]
}
```

| Field | Type | Required | Notes |
|---|---|---|---|
| `libraries` | array | yes | Top level must be an object holding this. May be empty. |
| `library` | string | yes | The id, `<namespace>/<name>`. |
| `description` | string | yes | Taken from the latest version. |
| `latest` | string | yes | Key of the latest version in `versions`. |
| `updated` | string | yes | Publish time of the latest version. Display only, any format. |
| `tags` | array of strings | no | Used by `tag:` filtering. Non-strings are ignored. |
| `versions` | array of strings | no | Newest first. Shown by `fetch-list <text>`. |

The client validates **every** entry, also those the filter would skip. One entry with a missing
required field or a wrong type makes the whole command fail with
`Corrupt library index - unreadable entry.` / `Malformed library index entry.`

The index is deliberately light (one row per library, latest only): clients may ingest it into a
local database for searching. It is meant to be cacheable with `ETag` / `Last-Modified`.

### `GET /info/<ns>/<name>`

```json
{
  "library": "raylib/raylib",
  "description": "Bindings for raylib 6.2.",
  "tags": ["graphics", "gamedev", "bindings"],
  "latest": "0.4.0",
  "updated": "2026-10-13T18:22:05Z",
  "versions": {
    "0.4.0": {
      "version": "0.4.0",
      "description": "Bindings for raylib 6.2.",
      "maintainer": "Jane Doe <jane@example.org>",
      "repository": "https://github.com/example/raylib-c3",
      "license": "Zlib",
      "targets": ["linux-x64", "macos-aarch64"],
      "checksum": "sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
      "size": 184320,
      "published": "2026-10-13T18:22:05Z"
    },
    "0.3.1": { "...": "..." }
  }
}
```

Top level, as validated by `load_fetch_info`:

| Field | Type | Required | Error if bad |
|---|---|---|---|
| `library` | string | yes | `corrupt library info (no 'library')` |
| `description` | string | yes | `corrupt library info (no 'description')` |
| `latest` | string | yes | `corrupt library info (no 'latest')` |
| `updated` | string | yes | `corrupt library info (no 'updated')` |
| `versions` | object | yes | `corrupt library info (no 'versions')` |
| `tags` | array | no | `corrupt library info (invalid 'tags')` if present but not an array |

Each value in `versions` must be a JSON object for the version to be downloadable. The **key** is
the version string; it is what `fetch` matches `@<version>` against and what `latest` must refer to.
The key order is the display order of `fetch-info`, so list the newest first.

Version entry fields. `c3c` only prints them (and silently skips any that are missing or of the
wrong type), but registries should provide all of them:

| Field | Type | Notes |
|---|---|---|
| `version` | string | Same as the key. Not read by the client. |
| `description` | string | |
| `maintainer` | string | Self-declared contact, e.g. `Name <mail>`. Not proof of who published. |
| `repository` | string | URL of the source. |
| `license` | string | SPDX identifier. |
| `targets` | array of strings | Optional. Omit if the library is not target specific. |
| `checksum` | string | `sha256:<hex>` of the `.c3l` file, assigned by the registry. `fetch` verifies it. |
| `size` | number | Size of the `.c3l` file in bytes, assigned by the registry. |
| `url` | string | Optional. Where to download this version from, instead of `/download`. Must be `http(s)`. |
| `published` | string | Publish time, assigned by the registry. |

What the library depends on is **not** in the record. That lives in the `manifest.json` inside the
`.c3l`, which the compiler reads after download.

### `GET /download/<ns>/<name>@<version>`

Responds `200` with the raw `.c3l`, which is a zip archive with `manifest.json` in its root.
The content type and file name are not looked at. An unknown library or version should be a `404`.
Since the client already has `latest` from `/info`, the server never has to resolve anything.

## What `fetch` does

For each argument `<ns>/<name>[@<version>]`:

1. Everything before `@` is the library id. It must contain a `/` that isn't the last character,
   otherwise the argument is skipped with `Skipping invalid library name`.
2. `GET /info/<id>` and validate it as above.
3. The version is the one after `@`, or `latest` from the record. It must be a key of `versions`
   and map to an object, else `no such version '<v>'`. (`@latest` is **not** special; it is looked
   up as a version string and fails.)
4. `GET /download/<id>@<version>` into a fresh temporary directory created next to the
   destination, so the final move stays on one filesystem.
5. If the record has a `checksum`, compare it to the file. A mismatch fails, and so does a `checksum`
   that doesn't start with `sha256:`.
6. Unless `--decompress=no`, unpack the zip. It must contain `manifest.json`, else
   `the library has no manifest`.
7. The manifest's `provides` must equal `<name>`, else `the library provides '<x>', not '<name>'`.
8. Move the result into place as `<dir>/<name>.c3l`, replacing any previous copy, and delete the
   temporary directory. A failure at any earlier step leaves an existing copy untouched.

`<name>` is the last path component of the id: `raylib/raylib` is installed as `raylib.c3l`.
Namespaces are only for the registry; they don't appear on disk or in the project.

`<dir>` is the first entry of `dependency-search-paths` when a `project.json` / `project.json5` is
present, otherwise the current directory (see `--path`).

Afterwards the bare name (`raylib`) of every library that was fetched is added to the project's
`dependencies` array, if there is a project file and it isn't listed yet. This also happens when
only some of several libraries succeeded. `fetch` doesn't save where the library came from, so that
name alone can't be fetched again on another machine without `/index.json`.

## `project fetch`

A dependency is written as `[<url>:]<namespace>/<name>[@<version>][:<checksum>]`, or as a plain name.
The build only uses the name. The checksum is the 64 hex digits of a sha256, and needs a version.

Without arguments `project fetch` lists what's missing and downloads nothing. With arguments, each
must be a dependency of the project, or of an installed library:

- A dependency with a namespace is fetched from its own `<url>`, or else from `--fetch-url`, the
  project's `fetch-url` property, or `DEFAULT_FETCH_URL`, in that order.
- A plain name is looked up in `/index.json`, which must have exactly one library of that name.
- A dependency of an installed library ignores `--fetch-url` and `fetch-url`: without its own `<url>`
  it uses `DEFAULT_FETCH_URL`.
- If the project and an installed library spell the same library differently and neither is a plain
  name, `c3c` stops with `'<name>' is ambiguous`.

`project fetch` only checks that a library is installed, not its version, so after changing a version
in `dependencies` the library has to be named: `c3c project fetch <name>`.

The build only loads libraries listed in the project, so a library's own dependencies must be listed
there too, or it fails with `Required library '<name>' could not be found`.

c3pkg shows a project line `"<base>:<owner>/<name>@<version>"` on each package page, which needs no
`fetch-url`, and only accepts plain names in a published manifest's `dependencies`.

Failures are printed per library (`Failed to fetch library '<id>': <reason>`) and fetching
continues. At the end `c3c` exits with an error if nothing was downloaded
(`Failed to download any libraries`) or if only some were (`Only some libraries were downloaded`).

`fetch-info` and `fetch-list` exit with an error if the registry can't be reached or the JSON is
rejected.

## Output of `fetch-info`

```
Library:     raylib/raylib
Description: Bindings for raylib 6.2.
Tags:        graphics, gamedev, bindings
Latest:      0.4.0
Updated:     2026-10-13T18:22:05Z

Versions:
  0.4.0
    Description: Bindings for raylib 6.2.
    Maintainer:  Jane Doe <jane@example.org>
    Repository:  https://github.com/example/raylib-c3
    License:     Zlib
    Targets:     linux-x64, macos-aarch64
    Size:        184320 bytes
    Checksum:    sha256:9f86...
    Published:   2026-10-13T18:22:05Z
```

`Tags`, `Versions` and each version line are left out when the data isn't there.

## Registry conventions

Not enforced by `c3c`, but what the client assumes and what the prototype Ruby registry did:

- **Ids** are `<namespace>/<name>`, exactly two parts. Namespaces are granted to publishers by the
  registry; there is no self-service.
- **Versions are immutable.** A version is unique per library and can't be republished, so there is
  one `published` date per version and checksums never change.
- **`latest`** is the highest semantic version, with the most recently published winning a tie.
  `description`, `tags` and `updated` of the library are those of the latest version, and
  `updated` is its `published` time. The client trusts `latest` and does no version comparison.
- **The registry assigns** `checksum`, `size` and `published`. The rest of a version entry is the
  author's declaration, normally derived from the manifest at publish time.
- Libraries are plain files (like jars): users may keep them in their repository, copy them or
  re-host them. A registry going away doesn't break anyone.

A static registry is just this tree:

```
index.json
info/raylib/raylib                     # the /info JSON, served as application/json
download/raylib/raylib@0.4.0           # the .c3l
download/raylib/raylib@0.3.1
download/raylib/raylib                 # optional: copy of the latest
download/raylib/raylib@latest          # optional: copy of the latest
```

Publishing (a `POST` with an API key, handled by the registry) is outside this protocol; `c3c`
has no command for it.

## Not (yet) part of the client

- **Sizes are not verified.** `size` is only shown by `fetch-info`.
- **No protocol version** in the URL layout. A breaking change would have to move to a new base
  path, selected with `--fetch-url`.
- **`c3c vendor-fetch` doesn't use this registry.** It still downloads `<name>.c3l` from the
  `c3lang/vendor` GitHub releases (`vendor_fetch_single`). It does share `--decompress`.
- `fetch-list <text>` is a case-sensitive substring match on the id (not the description), and
  only a single filter argument is read.
