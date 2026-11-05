-- Fedigest schema. Later changes go in new numbered files next to this one;
-- each file runs once, in filename order, tracked in schema_migrations.

CREATE TABLE users (
    id                  INTEGER PRIMARY KEY,
    username            TEXT NOT NULL UNIQUE,
    display_name        TEXT,
    email               TEXT,
    password_hash       TEXT NOT NULL,
    role                TEXT NOT NULL CHECK (role IN ('admin','user')),
    avatar_upload_path  TEXT,
    created_at          TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at          TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE sessions (
    id          TEXT PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users(id),
    expires_at  TEXT NOT NULL
);

CREATE TABLE password_resets (
    id          INTEGER PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users(id),
    token       TEXT NOT NULL UNIQUE,
    expires_at  TEXT NOT NULL,
    used        INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE categories (
    id                 INTEGER PRIMARY KEY,
    name               TEXT NOT NULL,
    slug               TEXT NOT NULL UNIQUE,
    sort_order         INTEGER NOT NULL DEFAULT 0,
    show_on_frontpage  INTEGER NOT NULL DEFAULT 1,
    created_at         TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Auto-ingest: a community/magazine, person or whole instance to watch.
CREATE TABLE sources (
    id                    INTEGER PRIMARY KEY,
    platform              TEXT NOT NULL CHECK (platform IN ('lemmy','piefed','mbin')),
    instance_url          TEXT NOT NULL,
    watch_type            TEXT NOT NULL DEFAULT 'community' CHECK (watch_type IN ('community','person','instance')),
    watch_target          TEXT,
    auth_token            TEXT,
    mode                  TEXT NOT NULL DEFAULT 'manual' CHECK (mode IN ('manual','auto-review','auto-publish')),
    poll_interval_sec     INTEGER NOT NULL DEFAULT 900,
    last_polled_at        TEXT,
    consecutive_failures  INTEGER NOT NULL DEFAULT 0,
    source_unavailable    INTEGER NOT NULL DEFAULT 0,
    default_category_id   INTEGER REFERENCES categories(id),
    created_by_user_id    INTEGER REFERENCES users(id),
    ingest_since          TEXT,
    created_at            TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Extra categories every pick from a source gets.
CREATE TABLE source_categories (
    source_id    INTEGER NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    category_id  INTEGER NOT NULL REFERENCES categories(id) ON DELETE CASCADE,
    PRIMARY KEY (source_id, category_id)
);

CREATE TABLE picks (
    id                          INTEGER PRIMARY KEY,
    slug                        TEXT NOT NULL,
    title                       TEXT NOT NULL,
    excerpt                     TEXT NOT NULL,
    excerpt_auto                INTEGER NOT NULL DEFAULT 0,
    body                        TEXT NOT NULL,
    use_original_as_body        INTEGER NOT NULL DEFAULT 0,
    original_body_cache         TEXT,
    category_id                 INTEGER NOT NULL REFERENCES categories(id),
    curator_id                  INTEGER NOT NULL REFERENCES users(id),

    source_type                 TEXT NOT NULL CHECK (source_type IN ('fedi','local')),
    source_id                   INTEGER REFERENCES sources(id),
    auto_ingested               INTEGER NOT NULL DEFAULT 0,
    source_platform             TEXT CHECK (source_platform IN ('lemmy','piefed','mbin')),
    original_url                TEXT,
    original_ap_id              TEXT,
    community_post_url          TEXT,
    original_author             TEXT,
    original_community          TEXT,
    original_published_at       TEXT,
    link_url                    TEXT,

    image_url                   TEXT,
    image_upload_path           TEXT,
    alt_text                    TEXT,

    status                      TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','published')),
    published_at                TEXT,
    publish_at                  TEXT,
    pinned_at                   TEXT,

    comment_count               INTEGER,
    comment_count_updated_at    TEXT,
    comments_cache              TEXT,
    comments_cache_updated_at   TEXT,
    consecutive_failures        INTEGER NOT NULL DEFAULT 0,
    source_unavailable          INTEGER NOT NULL DEFAULT 0,

    created_at                  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at                  TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_picks_published ON picks (status, published_at DESC);
CREATE INDEX idx_picks_category ON picks (category_id, status, published_at DESC);
CREATE INDEX idx_picks_curator ON picks (curator_id, status, published_at DESC);

-- Extra categories of a pick, besides picks.category_id.
CREATE TABLE pick_categories (
    pick_id      INTEGER NOT NULL REFERENCES picks(id) ON DELETE CASCADE,
    category_id  INTEGER NOT NULL REFERENCES categories(id) ON DELETE CASCADE,
    PRIMARY KEY (pick_id, category_id)
);
CREATE INDEX idx_pick_categories_category ON pick_categories (category_id);

-- Kept apart from picks so counting a view doesn't rewrite the pick row
-- and its search index entry.
CREATE TABLE pick_views (
    pick_id  INTEGER PRIMARY KEY REFERENCES picks(id) ON DELETE CASCADE,
    views    INTEGER NOT NULL DEFAULT 0
);

CREATE VIRTUAL TABLE picks_fts USING fts5(
    title, excerpt, body,
    content='picks', content_rowid='id'
);

CREATE TRIGGER picks_ai AFTER INSERT ON picks BEGIN
    INSERT INTO picks_fts(rowid, title, excerpt, body) VALUES (new.id, new.title, new.excerpt, new.body);
END;
CREATE TRIGGER picks_ad AFTER DELETE ON picks BEGIN
    INSERT INTO picks_fts(picks_fts, rowid, title, excerpt, body) VALUES ('delete', old.id, old.title, old.excerpt, old.body);
END;
CREATE TRIGGER picks_au AFTER UPDATE ON picks BEGIN
    INSERT INTO picks_fts(picks_fts, rowid, title, excerpt, body) VALUES ('delete', old.id, old.title, old.excerpt, old.body);
    INSERT INTO picks_fts(rowid, title, excerpt, body) VALUES (new.id, new.title, new.excerpt, new.body);
END;

CREATE TABLE sidebar_blocks (
    id            INTEGER PRIMARY KEY,
    title         TEXT NOT NULL,
    content_md    TEXT NOT NULL,
    content_html  TEXT,
    sort_order    INTEGER NOT NULL DEFAULT 0,
    enabled       INTEGER NOT NULL DEFAULT 1,
    created_at    TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at    TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Single row of site-wide settings.
CREATE TABLE site_settings (
    id               INTEGER PRIMARY KEY CHECK (id = 1),
    site_name        TEXT NOT NULL DEFAULT 'Fedigest',
    icon_path        TEXT,
    theme            TEXT NOT NULL DEFAULT 'light',
    visitor_themes   TEXT NOT NULL DEFAULT '*',
    auto_post_label  TEXT NOT NULL DEFAULT 'Auto post',
    multi_category   INTEGER NOT NULL DEFAULT 1,
    privacy_md       TEXT
);
INSERT INTO site_settings (id) VALUES (1);

-- Themes made in the admin UI (a stylesheet only).
CREATE TABLE custom_themes (
    name        TEXT PRIMARY KEY,
    css         TEXT NOT NULL,
    based_on    TEXT,
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Whether each theme is light, dark or fixed, and the theme to switch to
-- when the visitor's device is in the other mode.
CREATE TABLE theme_modes (
    name     TEXT PRIMARY KEY,
    mode     TEXT NOT NULL CHECK (mode IN ('light','dark','fixed')),
    partner  TEXT
);
INSERT INTO theme_modes (name, mode, partner) VALUES
    ('light', 'light', 'dark'),
    ('dark', 'dark', 'light'),
    ('dim', 'dark', 'light'),
    ('nord', 'dark', 'nord-light'),
    ('nord-light', 'light', 'nord'),
    ('sepia', 'light', 'sepia-dark'),
    ('sepia-dark', 'dark', 'sepia'),
    ('newspaper', 'light', 'newspaper-dark'),
    ('newspaper-dark', 'dark', 'newspaper'),
    ('solarized-light', 'light', 'solarized-dark'),
    ('solarized-dark', 'dark', 'solarized-light'),
    ('terminal-light', 'light', 'terminal'),
    ('terminal', 'dark', 'terminal-light'),
    ('terminal-green', 'dark', 'terminal-light'),
    ('high-contrast', 'fixed', NULL);
