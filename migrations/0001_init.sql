CREATE TABLE users (
    id            INTEGER PRIMARY KEY,
    email         TEXT NOT NULL UNIQUE COLLATE NOCASE,
    login         TEXT NOT NULL UNIQUE COLLATE NOCASE,
    name          TEXT NOT NULL DEFAULT '',
    password_hash TEXT,
    github_id     INTEGER UNIQUE,
    github_login  TEXT NOT NULL DEFAULT '',
    bio           TEXT NOT NULL DEFAULT '',
    avatar_url    TEXT NOT NULL DEFAULT '',
    is_admin      INTEGER NOT NULL DEFAULT 0,
    banned_at     TEXT,
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);

CREATE TABLE sessions (
    token_hash BLOB PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);
CREATE INDEX sessions_user ON sessions(user_id);

CREATE TABLE api_tokens (
    id           INTEGER PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    token_hash   BLOB NOT NULL UNIQUE,
    last_used_at TEXT,
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    revoked_at   TEXT
);

CREATE TABLE packages (
    id              INTEGER PRIMARY KEY,
    name            TEXT NOT NULL UNIQUE,
    name_key        TEXT NOT NULL UNIQUE, -- lowercase with - and _ folded, blocks look-alike names
    owner_id        INTEGER NOT NULL REFERENCES users(id),
    description     TEXT NOT NULL DEFAULT '',
    docs_url        TEXT NOT NULL DEFAULT '',
    git_url         TEXT NOT NULL DEFAULT '',
    license         TEXT NOT NULL DEFAULT '',
    readme_md       TEXT NOT NULL DEFAULT '',
    readme_html     TEXT NOT NULL DEFAULT '',
    stars_count     INTEGER NOT NULL DEFAULT 0,
    downloads_total INTEGER NOT NULL DEFAULT 0,
    created_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    updated_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    removed_at      TEXT
);
CREATE INDEX packages_owner ON packages(owner_id);
CREATE INDEX packages_updated ON packages(updated_at DESC);
CREATE INDEX packages_downloads ON packages(downloads_total DESC);
CREATE INDEX packages_stars ON packages(stars_count DESC);

CREATE TABLE package_tags (
    package_id INTEGER NOT NULL REFERENCES packages(id) ON DELETE CASCADE,
    tag        TEXT NOT NULL,
    PRIMARY KEY (package_id, tag)
);
CREATE INDEX package_tags_tag ON package_tags(tag);

CREATE TABLE versions (
    id          INTEGER PRIMARY KEY,
    package_id  INTEGER NOT NULL REFERENCES packages(id) ON DELETE CASCADE,
    version     TEXT NOT NULL,
    c3_version  TEXT NOT NULL DEFAULT '',
    targets     TEXT NOT NULL DEFAULT '[]', -- JSON array of platform names from manifest.json
    size_bytes  INTEGER NOT NULL,
    sha256_file TEXT NOT NULL,
    sha256_tree TEXT NOT NULL,
    storage_key TEXT NOT NULL,
    requires    TEXT NOT NULL DEFAULT '[]',
    yanked_at   TEXT,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    UNIQUE (package_id, version)
);

CREATE TABLE stars (
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    package_id INTEGER NOT NULL REFERENCES packages(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    PRIMARY KEY (user_id, package_id)
);

CREATE TABLE downloads_daily (
    version_id INTEGER NOT NULL REFERENCES versions(id) ON DELETE CASCADE,
    day        TEXT NOT NULL,
    count      INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (version_id, day)
);

CREATE TABLE reports (
    id          INTEGER PRIMARY KEY,
    package_id  INTEGER NOT NULL REFERENCES packages(id) ON DELETE CASCADE,
    reporter_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
    reason      TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'open',
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);

-- Search index maintained by the application (name, description, space-separated tags).
CREATE VIRTUAL TABLE packages_fts USING fts5(name, description, tags, tokenize = 'unicode61 remove_diacritics 2');
