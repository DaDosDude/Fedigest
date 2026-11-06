# Changelog

## 0.1.4

**Added**
- Bylines on cards and post pages link the curator's name to their author
  page and each category to its page. Author pages existed (and were in
  the sitemap) but nothing on the site linked to them. On cards the title
  is now the link to the post, instead of the title and byline together.
- Auto posts have their own page, `/auto` ("Posts by Auto post", following
  the label set in Site settings), linked from their byline label and
  listed in the sitemap.

**Fixes**
- Auto posts are no longer listed on the author page of the admin who set
  up their source, nor counted for that author in the sitemap or named as
  the post's author in its structured data.

## 0.1.3

First public release, on GitHub. Edit links on public pages, shorter
automatic excerpts, and a much gentler way of asking other instances for
data.

**Added**
- The footer links to the source code ("Source"): the official repository,
  or `SOURCE_URL` from `.env`. If you run a modified version, point it at
  your own code, which the AGPL asks you to offer to your visitors.
- Logged-in curators get an "Edit" link on cards (frontpage, category,
  author and search pages) and on post pages for the picks they may edit:
  admins every pick, users their own.
- Automatic excerpts (auto posts, and Fetch metadata when you don't write
  your own) show the post's opening paragraph on cards, feeds and link
  previews, plus the second paragraph when the first is under 30 words,
  instead of the whole post. Existing picks follow without re-saving. The
  post page shows the original post once, in full (auto posts showed their
  title above it).
- Cards end with "Continue reading →" when the post page has more than the
  excerpt.
- Refresh metadata for all picks also stores each automatic excerpt the
  way cards now show it, so search finds the opening paragraph.
- When an instance says it's getting too many requests, Fedigest leaves it
  alone for a minute, then 2, then 5 on further refusals, for everything at
  once: post pages show the saved comments, the comment-count refresh and
  the metadata refresh go on with other instances, and sources on it wait
  for their next check. Such refusals no longer mark picks "Original
  unavailable" or sources unavailable.
- Refresh metadata for all picks no longer gets most of its requests
  refused by busy instances: each instance is asked at most once a second
  (further apart after a refusal), and picks on paused instances wait and
  are retried while the others carry on. The status line shows the time in
  your own time zone.
- All requests to an instance, from every part of Fedigest, take turns in
  one queue: at most one request a second per instance. Visitors, admins
  and new auto posts go first; the comment-count and metadata refreshes
  wait behind them. A new auto post's comment count is fetched right away.
- Comment counts on cards are refreshed less often as picks age: every 10
  minutes for pinned picks and the last day's, hourly for the rest of the
  week, every 6 hours up to 30 days. That's far fewer requests to the
  source instances.
- Refresh metadata falls back to other copies of a post (on its
  community's instance, then the poster's) when the link a pick was made
  from fails, and uses them while that instance is pausing.
- Errors from instances in the server log include the instance's own
  message (for example Lemmy's `rate_limit_error`).
- `deploy.sh` prints each build step once, with its time, instead of
  Docker's live view, which filled the terminal with a new "Building" line
  every tenth of a second. The backup step no longer prints Docker's copy
  progress (its output is shown only if it fails).

**Project**
- Published at https://github.com/DaDosDude/Fedigest, with the tests
  running on every push (GitHub Actions) and a security policy.
- The README's quick start installs from a git clone, so `deploy.sh`
  updates by pulling.

## 0.1.2

No new features; fixes, robustness, accessibility and documentation.

**Fixes**
- Mbin microblog post links (`/m/<magazine>/p/<id>`) fetched an unrelated
  thread with the same number; posts and threads are now told apart.
- Mbin bylines for remote users and magazines no longer get a doubled
  instance or a stray `@`, so excluding such a community works.
- Comments on Lemmy posts in remote communities could come back empty on
  instances whose default listing is Local.
- Deleted and moderator-removed comments no longer show their text; their
  replies stay in place.
- The pick, source and user forms keep everything typed when they come back
  with an error, including on the duplicate warning (image, alt text and
  schedule were lost).
- Renaming a pick updates its link slug; alt text can be cleared.
- Changing a fedi pick's original URL drops the old post's details and
  comments until the new one is fetched.
- Searching for only spaces gave an error page.
- Deleting a user now also cancels their scheduled picks.
- An unknown category, extra category or platform in a form is a form
  error instead of an error page.
- Category slugs are made URL-safe, and a new category goes last in the
  menu.
- "Fetch metadata" tries the other platforms when the link doesn't fit the
  one chosen.
- The source form's "Auth token" field is gone: it was never used, and
  sources only read public APIs.
- A visitor leaving a slow post page no longer counts as a failure of the
  original instance.

**Robustness and speed**
- Visitors arriving at once share one comment fetch; after a failure the
  instance isn't asked again until the cache time has passed; crawlers and
  link previews get the saved comments without a fetch.
- The background comment-count refresh covers the last 30 days and pinned
  picks instead of every pick ever made.
- Polls read the newest 50 posts instead of 20, so busy feeds lose fewer.
- List pages, the admin picks list and the sitemap no longer load every
  comment thread; the search index is only rewritten when a pick's text
  changes (migration 0004); far fewer queries per page.
- Uploads are limited to 10 MB.
- Password-reset email: timeouts, port 465 (TLS) support, port 587 by
  default, proper headers; sent in the background.
- Clean shutdown on `docker compose stop`; expired sessions and reset links
  are deleted hourly; backups are readable only by their owner.
- `deploy.sh` health-checks the port from `docker-compose.override.yml` too.

**Accessibility and pages**
- Skip link, labelled controls, keyboard-safe theme menu and admin filters,
  a keyboard-operable admin menu on phones, and a focus-managing image
  viewer.
- Built-in theme colours meet WCAG AA contrast (dark and dim buttons now
  have dark text).
- Page titles use the site name; meta descriptions are cut to a sensible
  length; pages past the last one are 404s.
- Long names and links no longer widen the page on phones.
- htmx, which nothing used, is no longer loaded.

**Documentation**
- README: restoring a backup, moving servers, logs, changing the port, the
  license. `.env.example` explains SMTP ports and `SITE_DOMAIN`.
- Added the AGPL-3.0 `LICENSE` file.
