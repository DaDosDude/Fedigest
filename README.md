# Fedigest

Self-hostable curated "front page" for the threadiverse (Lemmy, PieFed, Mbin).
Curators pick posts from any instance (or write local posts), add an excerpt
and write-up, and publish them as a single-column, newest-first news feed.

## Quick start

You need git and Docker with Compose v2 (`docker compose`).

```bash
git clone https://github.com/DaDosDude/Fedigest.git /opt/fedigest
cd /opt/fedigest
cp .env.example .env
docker compose up -d --build
```

Every setting in `.env` is optional and explained in `.env.example`.

Fedigest now answers on `http://127.0.0.1:8080`, on the server itself only.
To put it online, run it behind a reverse proxy that handles HTTPS (see the
next section) and set `SITE_DOMAIN` in `.env`.

On first boot, an `admin` account is created automatically with a random
password printed once to the container logs:

```bash
docker compose logs app | grep -A2 "First-run setup"
```

Log in at `http://localhost:8080/login` and change that password
immediately (Account page in the admin UI).

## Deploying as a subdomain behind an existing host Caddy

This is written for running Fedigest alongside other services on a server
that already has Caddy running on the host (not in Docker) — so the
container publishes a localhost-only port and Caddy reverse-proxies to
that, rather than joining a shared Docker network. Substitute your own
domain and paths for the placeholders below.

1. Copy this project to the server (e.g. `/opt/fedigest`) and set up `.env`:
   ```bash
   cp .env.example .env
   ```
   Then set at least:
   ```
   SITE_NAME=Fedigest
   SITE_DOMAIN=fedigest.example.com
   ```

2. Build and start:
   ```bash
   cd /opt/fedigest
   docker compose up -d --build
   ```
   This publishes port 8080 bound to `127.0.0.1` only (see
   `docker-compose.yml`) — not reachable from outside the host, only by
   Caddy. If port 8080 is already taken by something else on the server,
   use another one in a `docker-compose.override.yml` next to it (updates
   never touch that file; `!override` needs Compose 2.24 or newer):
   ```yaml
   services:
     app:
       ports: !override
         - "127.0.0.1:8090:8080"
   ```
   and use the same port in the Caddy block in step 4.

3. Add the DNS record: an A (and/or AAAA) record for the `fedigest`
   subdomain pointing at the server's IP, same as your other subdomains.

4. Add a site block like the one in `deploy/caddy-fedigest.snippet` to the
   server's Caddyfile (append, don't replace), updating the domain to
   match, then reload:
   ```bash
   caddy reload --config /path/to/Caddyfile
   ```
   Caddy issues the TLS cert for the subdomain automatically on first
   request, the same way it does for other sites on that host.

5. Get the first-run admin password and log in:
   ```bash
   docker compose logs app | grep -A2 "First-run setup"
   ```
   `https://fedigest.example.com/login` — change the password immediately.

## Updating

`deploy.sh` in the install folder does the whole update, like PieFed's
deploy script:

```bash
/opt/fedigest/deploy.sh            # update
/opt/fedigest/deploy.sh --check    # only show installed vs available version
/opt/fedigest/deploy.sh --rollback # back to the code before the last update
```

In a git checkout (as in the quick start) it pulls the latest code from the
branch you're on; otherwise it unpacks
`fedigest.tar.gz` from the folder above the install (or a release archive
you pass: `deploy.sh /path/to/fedigest.tar.gz`). An archive with the same
version as the installed one is skipped unless you add `--force`, which
also recreates the container (needed after editing `.env`). Each update:

1. backs up the database, uploads and session secret to `backups/` (the
   last 5 are kept),
2. remembers the current code,
3. installs the new code, rebuilds the image and restarts the container,
4. waits for the site to answer, and if it doesn't, puts the previous code
   back and restarts that.

Your `.env`, `backups/` and `docker-compose.override.yml` are left alone.
Put local changes to the Compose setup (a different port, extra networks) in
`docker-compose.override.yml` rather than editing `docker-compose.yml`, so
updates never overwrite or conflict with them. Database changes are applied
automatically on startup; a rollback restores code, not data, so if an
update changed the database, restore the newest file in `backups/` (see
[Restoring a backup](#restoring-a-backup)).

Go's module and compile caches live in BuildKit cache mounts, so rebuilds
reuse one cache. To reclaim unused build cache (never touches volumes):

```bash
docker builder prune -f
```

After an update that adds new details to fedi picks or changes how
automatic excerpts are made, run **Picks → Refresh metadata for all picks**
once as an admin to bring existing picks up to date.

## Backups

`deploy.sh` makes a backup before every update. To make one yourself, one
command writes the database (a consistent snapshot, safe while running),
the session secret and all uploads into a single archive:

```bash
docker exec fedigest ./fedigest backup /app/data/fedigest-backup.tar.gz
docker cp fedigest:/app/data/fedigest-backup.tar.gz /path/to/backups/
docker exec fedigest rm /app/data/fedigest-backup.tar.gz
```

The archive is readable only by its owner: the session secret in it could
be used to take over logged-in sessions, so keep it private.

### Restoring a backup

From the install folder, with the archive's name filled in:

```bash
docker compose stop app
mkdir restore && tar xzf backups/fedigest-YYYYMMDD-HHMMSS.tar.gz -C restore
docker compose run --rm --no-deps -v "$PWD/restore:/restore" --entrypoint sh app -c \
  'rm -f /app/data/fedigest.db-wal /app/data/fedigest.db-shm &&
   cp /restore/fedigest.db /restore/session_secret /app/data/ &&
   cp -a /restore/uploads/. /app/uploads/'
docker compose start app
rm -rf restore
```

`docker compose run` uses the same volumes as the app, so this works
whatever the volumes are called. The two `-wal`/`-shm` files belong to the
database being replaced and must go with it. Moving to another server works
the same way: install Fedigest there, copy `.env` and a backup across, and
restore it before the first start.

### Logs

```bash
docker compose logs -f app
```

Failed fetches from other instances, auto-ingest polls and scheduled
publishing are logged there.

## Features

**Picks.** Fedi picks (linked to a Lemmy/PieFed/Mbin post — paste any post
URL from any instance and press *Fetch metadata*; if the platform chosen
doesn't fit the link, the others are tried) or fully local posts.
Each has a title, excerpt (card teaser — unless you write your own, it's
the post's opening paragraph, plus the second when the first is under 30
words; the post page still shows the whole post, and cards with more to
read end with "Continue reading →"), optional body (the post page falls
back to the excerpt when it's empty), a required category, an image (linked
or uploaded, up to 10 MB — an upload always wins), and draft/published
status. Fedi picks can also pull the original post's text in below the
curator's own. If the form is sent back with a problem, everything typed
is kept (an attached file has to be chosen again).
When you're logged in, every card (frontpage, category, author and search
pages) and post page you're allowed to edit has an *Edit* link: admins see
it on every pick, users only on their own.

**Bylines.** Every card and post page shows the curator (or the admin-set
"Auto post" label for auto-ingested picks) with the pick's date, and for
fedi picks the original author and community with the original post's date.

**Link posts.** When the original post links to an article, a "Linked
article — domain ↗" box appears on the post page and a "🔗 domain ↗" line on
frontpage/category/search cards. Fetch metadata fills the link in; it's
editable on the pick form.

**Spoilers.** Lemmy/PieFed `::: spoiler` blocks render as click-to-reveal
sections in posts and comments. On cards they show as the same collapsed
box; clicking it opens the post with that spoiler already expanded.

**Comments.** Read-only, fetched live from the post's community instance
(where every comment gathers, so none go missing because some other
instance blocks the commenter's), falling back to the link the pick was
made from, with a short cache (`COMMENT_CACHE_TTL`). Comments on
crossposts of the same link are shown too, labelled with their community.
Deleted and moderator-removed comments show as such, without their text,
and their replies stay in place. When the original can't be reached the
last good copy is shown, with when it was fetched, and the instance isn't
asked again until the cache time has passed (with an admin-only warning
after repeated failures). Visitors arriving at once share one fetch, and
crawlers and link previews get the saved copy without a fetch. Card
comment counts are refreshed in the background: every 10 minutes for
pinned picks and those from the last day, hourly for the rest of the week,
every 6 hours up to 30 days; older picks update when someone opens them.
Fedigest asks each instance at most once a second, all its work together,
with visitors and new posts ahead of background refreshes. When an
instance says it's getting too many requests, Fedigest leaves it alone for
a while (a minute, longer if it keeps refusing) and shows the saved
comments meanwhile.

**View original / Interact from your instance.** "View original" opens the
post on its community's home instance (e.g. lemmy.world for
microblogmemes@lemmy.world, even if it was posted from another instance or
pasted from a third), falling back to the poster's instance when that
can't be looked up (e.g. Mbin communities). "Interact from your instance"
(Lemmy/PieFed picks): the visitor enters their home instance once
(remembered in their browser; on later visits a 3-second countdown with
**Go now** and **Change instance** buttons gives them a chance to switch),
and their browser asks that instance for its
local copy of the post and jumps straight to it
(`https://<instance>/post/<id>`), where they can vote and comment. If the
instance doesn't know the post yet, they land on its search page instead,
which fetches it once they're logged in. The lookup goes directly from the
visitor's browser to their instance; nothing about it is sent to Fedigest.
Mbin picks get it too once their original ID is known.

**Duplicates, scheduling, views.** Picking a post or article that's
already picked asks for confirmation, and auto-ingest skips it. Drafts can
be scheduled to publish at a set time. Each pick's views (once per visitor
per day, no cookies, bots and logged-in users not counted) show in the admin
picks list.

**Pinning.** Admins can pin picks from the picks list. Pinned picks sit at
the top of the frontpage and every category page, marked "📌 Pinned".

**Categories.** Drive the nav menu, category pages and per-category RSS
feeds. A pick or source can have extra categories besides its main
one (Site settings → "Allow multiple categories", on by default). When two
sources find the same post, the pick gets both sources' categories. Drag them into order on the admin categories page. The header shows
as many as fit (one row on desktop, two on phones) plus a **More…** link to
`/categories`, which lists them all. Each has a **Show on frontpage** checkbox: untick it to keep that
category's picks off the frontpage and main feed (they stay on the
category's own page, its feed, search and author pages).

**Auto-ingest.** Sources watch a community/magazine, a person, or a whole
instance's Local feed (every new post in the communities it hosts) on an
interval of one minute to one day (exponential backoff on failure, dedup by
original post) in manual, auto-review (lands as a draft) or auto-publish
mode. Each poll reads the newest 50 posts and takes those at least 10
minutes old (so link previews have loaded), so a feed with more than 50 new
posts between two polls needs a shorter interval. A new source can also
pick up posts from the last few minutes, hours or days. Sources can have a
name, and person and whole-instance sources can skip a list of
communities (by name on any instance, or name@instance).

**Themes.** Light (default), Dark, Dim, High contrast, Sepia and Sepia
dark, Newspaper and Newspaper dark, Solarized light and dark, Nord light and
Nord, and Terminal, Terminal light and Terminal green (double-line borders
and a bitmap PC font). Each theme is light, dark or fixed and can have a
partner of the other kind: paired themes follow the visitor's device
light/dark setting, so the default Light switches to Dark at night. The
site theme is set in **Site settings**, which also chooses which themes
visitors can pick from the footer menu: each pair as "auto", always light
or always dark. Visitors can tick **Pure black (AMOLED)** to always get the
dark version on pure black. **Admin → Themes** sets each theme's light/dark
partner and makes your own theme by copying one and changing colours, font,
text size, corners, width, lines and buttons with a live preview, or by
editing its stylesheet. Custom themes are stored in the database, so
backups include them and updates leave them alone. The built-in themes'
text, links and buttons meet WCAG AA contrast.

**Privacy page.** `/privacy`, linked from the footer, describes what
Fedigest stores (login and theme cookies, the in-memory view-count and
login-attempt records, images loaded from other servers). The text is fixed so it always matches
what the software does; Site settings sets the contact line at the end.

**Deleting.** Picks, pick images, categories, users, sources, sidebar
blocks, avatars and the site icon can all be removed. A category can only be deleted once no picks use it; if sources still
post into it, you're shown which ones and can cancel or delete them along
with the category. Deleting a user
unpublishes and unschedules their picks (they become drafts under the
deleting admin), so an abusive account's posts go offline immediately.
Everyone can upload or remove their own avatar on the Account page.

**Security.** Every form and admin action rejects cross-site requests,
and logins are rate limited (5 wrong passwords per IP or 10 per username in
15 minutes locks them out for 15 minutes); a wrong username takes as long to
answer as a wrong password. Changing or resetting a password logs that
account out everywhere else, and expired sessions and reset links are
deleted hourly. Uploads must be real PNG, JPEG, GIF, WebP or ICO images of
at most 10 MB and are stored under a name the server picks. Fetching a
pasted post never connects to private, local or other non-public network
addresses. No third-party scripts. Run Fedigest behind a reverse proxy (as
in `docker-compose.yml`, which only listens on localhost): the login limits
read the visitor's address from the proxy's `X-Forwarded-For`, and without
`SITE_DOMAIN` links use the proxy's `X-Forwarded-Proto`.

**Accessibility.** Pages have a "Skip to content" link, labelled form
controls, and menus that work from the keyboard: the footer theme menu and
the admin filters only apply on Enter or the Apply button when changed with
the keyboard, and the admin menu on phones opens with the keyboard too.
The full-image view keeps focus inside it and returns it when closed.

**Unavailable originals.** Picks whose original post can no longer be
fetched (deleted, or the instance is gone) are flagged "Original unavailable"
in the admin picks list, with a filter to list just those.

**Admin UI.** Always dark, independent of the public theme. Two roles:
admins manage everything; users create picks and edit/delete their own, and
only see Picks and Account in the menu. Admins also get **Refresh metadata
for all picks** (see Updating) — it fills empty fields and brings
automatic excerpts up to date, and never changes titles, bodies, images or
anything edited by hand.

**Also:** closed registration (admins create accounts), public author pages,
an admin-editable markdown sidebar (beside content on desktop, below it on
mobile), full-text search (SQLite FTS5), RSS 2.0 feeds (site-wide at `/feed.xml`, matching the
frontpage; per category at `/c/<slug>/feed.xml`, which category pages link
to), SEO output (page titles with the site name, canonical URLs including
the page number, meta descriptions, Open Graph/Twitter Card tags, JSON-LD
on post pages, `sitemap.xml`, `robots.txt`, proper 404s, also past the
last page), and email password reset when SMTP is configured (otherwise an
admin resets passwords manually). Drafts are only visible to their curator
and admins — everyone else gets a plain 404.

## Testing

With Go 1.24 and a C compiler (for SQLite):

```bash
CGO_ENABLED=1 go test -tags sqlite_fts5 ./...
```

GitHub Actions runs the same tests, plus `gofmt` and `go vet`, on every
push and pull request.

Tests run against real temporary SQLite databases. They cover the adapters
(against fake HTTP servers), the comment cache, fallback, shared fetches
and failure handling, the poller's dedup/backoff/mode logic, the database
schema and upgrades, configuration, email headers, the backup archive, and
the web app end to end: every public and admin page rendering, access
control per role, cross-site request rejection, login rate limiting, the
pick, source, user and category forms (including keeping input after an
error), uploads, search, the sitemap and pagination, the privacy page,
theme files, light/dark pairing, the visitor theme menu and custom themes
(create, rename, delete).

## Not implemented

- Atom output alongside RSS (RSS 2.0 covers every common feed reader).
- Mbin instances that require a login for their API (e.g. fedia.io): pick
  their magazines through a Lemmy/PieFed instance that follows them instead.
- A direct jump for visitors whose home instance is Mbin (they land on
  their instance's search page instead).
- lemmyverse.link: it has no working, documented format for linking to a
  *post* (every URL shape tried 404s), which is why Fedigest does its own
  instance lookup instead.

## License

Fedigest is free software under the GNU Affero General Public License,
version 3 (see `LICENSE`). If you run a modified version for others, the
license asks you to offer them its source code: set `SOURCE_URL` in `.env`
to where your version's code is, and the footer's "Source" link points
there.

The Terminal themes' font is Px437 IBM VGA 8x16 by VileR (int10h.org),
CC BY-SA 4.0, with its licence next to it in `themes/*/fonts/`. The
Solarized themes use Ethan Schoonover's Solarized palette and the Nord
themes Arctic Ice Studio's Nord palette (both MIT), slightly adjusted for
contrast.

## Contributing and security

Bug reports and pull requests are welcome on
[GitHub](https://github.com/DaDosDude/Fedigest). Please report security
problems privately, as described in `SECURITY.md`, not in a public issue.
