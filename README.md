# CS2 Predictor

*[Читать по-русски](README.ru.md)*

A Telegram bot that runs match-outcome prediction polls for CS2 in group chats — the non-anonymous kind, where
everyone in the chat can see who called it right. It pulls matches and results from PandaScore, and every chat runs
its own little world: its own subscriptions, its own settings, its own leaderboard and medals, with zero bleed-over
between chats.

## What it actually does

The core loop is simple: a match gets scheduled, the bot posts a poll for it, people vote before it starts, and once
it's over everyone gets scored and ranked. Everything else in this repo exists to make that loop pleasant to use and
safe to run unattended for months.

**Talking to it.** The whole interface lives inside Telegram's own message-editing — tapping through a menu rewrites
the same message instead of spamming five new ones into the chat, and there's a Back button everywhere so you're
never stuck. A handful of buttons now carry an emoji (🏆 for tournaments, 📊 for stats, and so on) purely so the menu
scans faster at a glance — the actual hierarchy still comes from headings and button order, not from the icons doing
the work. Small confirmations (toggling a setting, subscribing) answer with a popup toast rather than another line in
the chat.

**Managing a chat happens in DM, mostly.** Any admin or moderator can open a private conversation with the bot and
manage every group they have rights in from there — and keep several groups' panels open in the same DM at once,
switching between them, because every button remembers which chat it belongs to. Tap into a group's panel and every
screen shows a small "🎮 Chat Name" header so you always know which chat you're editing before you tap something. In
the group itself, the menu still opens for whoever tapped it and only answers them; anyone else tapping the same
message gets a private "not yours" warning instead (Telegram genuinely has no way to whisper a message to one person
in a group, so this is the next best thing).

**Anyone can peek at a group's stats without being a manager.** The group's stats screen carries an "📲 Open in DM"
link — tap it and you get a read-only version of that chat's leaderboard menu in your own DM, no admin rights
required, and nothing you do there can ever post back into the group by accident.

**Leaderboards paginate now**, ten rows a page, with quick filter buttons for month / year / all-time / by tournament
that mark whichever one is currently active. If you're not on the visible page, your own row still shows up below a
divider so you're never wondering where you landed. Rows show rank, name, points, and — when there's a previous
period to compare against — a little ↑3 / ↓1 / • showing whether you moved.

**A change log per chat**: who turned on which language, changed the timezone, flipped the top-tier filter,
subscribed, unsubscribed, or got made a moderator, and when. `Settings → Recent changes`.

**Settings can be copied across every chat you manage** — language, timezone, "top tournaments only" — with a
confirmation screen that names exactly which chats it'll touch. Permissions get re-checked per chat at the moment it
actually applies, not cached from when you opened the screen.

**Personal notifications**, off by default and entirely opt-in: a nudge if you haven't voted yet and the poll is about
to close, and a short recap of how your own prediction landed once the match finishes.

**A personal stats breakdown** — current streak, best streak ever, your form over the last 10 predictions, accuracy
broken down per team, and how the last 30 days compare to the 30 before that.

**Your own display name.** `My stats → ✏️ My name in lists` lets you set a name for leaderboards and yearly awards
that's independent of your actual Telegram profile name — handy if you don't want your real name plastered across a
public group's rankings. A vote never overwrites it, and there's a one-tap reset back to your Telegram name.

**Cleaning up finished tournaments** from your subscription list is one button and one confirmation, and active
tournaments always sort above finished ones so the list stays useful even before you clean it.

**Finding and subscribing to tournaments** happens straight from the Telegram UI, with a tier badge on each one
(🌟 S-tier, ⭐ A-tier, 🔹 everything else). `/events top query` searches S/A only, `/events all query` searches
everything, and a chat can pick which one is the default for a bare `/events query`. When a new S/A tournament shows
up in the catalog, the bot doesn't wait to be asked — it proactively offers to add it to every chat not already
subscribed.

**Polls themselves** support BO1/BO2/BO3/BO5 plus arbitrary best-of / fixed-maps / first-to formats, and the question
carries hashtags for the tournament and both teams so it's searchable later in the chat's history. A poll closes on
schedule or the moment the match flips to "running," whichever comes first; a rescheduled match drags the poll's
deadline with it, and a cancelled or forfeited one voids the poll outright rather than scoring it. Changed votes,
duplicate webhook deliveries, duplicate results — all of it is idempotent, so nothing double-counts no matter how
many times Telegram or PandaScore decide to retry something.

**Monthly and yearly digests** post automatically at 20:00 in the chat's own timezone: the month's Top-3 plus a
running Top-3 for the year, and on December 31st a full year-end wrap with six awards — 🚀 comeback of the year,
🎯 sniper of the year, 🧠 expert of the year, 💎 sharpest read, 🔥 hottest streak, 🤝 the group's good-luck charm. The
full leaderboard isn't crammed into the digest message itself; it's one tap away behind a "Full year" button. This is
all backed by a transactional outbox so a delivery that fails partway through doesn't skip anyone or send twice, with
a recovery window that runs until 6am the next day.

**Moderators** get appointed by an admin (or an existing moderator) with `/moderator add`, replying to the person's
message — no special UI needed for that. Removing one now asks you to confirm first (there's a "⚠️ Remove moderator
rights?" screen with an explicit confirm button) instead of firing the moment you tap remove, because that one used
to be a single misclick away from an accidental removal.

**Unsubscribing needs a second signature** if there's another active admin or moderator around — they're the one who
confirms it, not you. If nobody else is reachable, or Telegram won't let the bot DM someone who's never started a
conversation with it, you get to confirm it yourself a second time instead. A pending request can be withdrawn while
it's waiting, and whoever was asked gets told if it was. Bulk cleanup of finished tournaments skips all of this —
there's no live poll left on a finished tournament that a second signature could be protecting.

**Team-data enrichment** — Valve Regional Standings, optionally HLTV's own world ranking, and optionally GRID and
Liquipedia — shows up as extra context inside a poll (a VRS rank, an HLTV rank, recent form, head-to-head record).
None of it is a source of truth for anything; PandaScore alone decides what events, matches, and results actually
exist. Providers form a swappable, ordered list with a circuit breaker: one that starts failing gets temporarily
skipped, with the cooldown growing exponentially, and plugging in a second provider some day won't need any change to
the actual business logic. HLTV's ranking, and a top-100 refresh of the VRS ranking itself, are both fetched via the
paid Apify actor `paco_nassa~hltv-org-team-ranking` — at most once a calendar week each, on HLTV's own update day
(Monday) at end of day (see `ApifyRankingGate`; deliberately tournament-independent — HLTV only republishes weekly
regardless). VRS keeps its free GitHub-based feed running independently the rest of the week, as a
fallback for whenever the paid one hasn't fired yet. Both HLTV and VRS feed the exact same team-identity matching
pipeline (a new team is fuzzy-matched against the cached ranking, then either auto-accepted or sent to
`/team_matches` for review) — the two rankings are cached and displayed as fully independent lines, one team having a
cached VRS rank never implies anything about its HLTV rank or vice versa.

**Telegram Topics** are supported — a chat-wide default and a per-tournament override — and there's the usual
infrastructure you'd expect from something meant to run unattended: a transactional outbox, PostgreSQL advisory
locks for anything that needs real mutual exclusion, structured JSON logs, health checks, Prometheus metrics, a
retention job that prunes the tables that grow with traffic instead of with the size of the data (webhook dedup
ledger, published outbox rows, resolved confirmation requests, the change log — each with its own TTL), and outbound
Bot API calls that throttle themselves against Telegram's rate limits proactively instead of just reacting to a 429
after it happens.

One thing it deliberately doesn't do: link out to hltv.org. PandaScore doesn't expose an HLTV ID, and HLTV's own terms
forbid scraping, so any attempt to bridge the two would be a guess dressed up as data. Everything shown comes from
PandaScore plus the local PostgreSQL cache — including a short win/loss balance line in the poll, built entirely from
already-cached finished matches in that tournament. Betting odds aren't part of it either; that needs PandaScore's
separate Odds product, which this project doesn't have access to, so it simply doesn't pretend to show odds.

## How the UI is put together

A few rules of thumb run through every screen in this bot, some of them learned the hard way:

- **Progressive disclosure.** A long list exists to pick *one* thing, not to expose every possible action on every
  row. "My tournaments" is a list of buttons, one per tournament; the actual actions — stats, unsubscribe, binding a
  Topic — live one tap deeper, on that tournament's own card.
- **A setting's value lives on its own button.** `Language: English`, `Timezone: Europe/Moscow`,
  `Tournaments: S/A only` — the current state is right there on the thing you'd tap to change it, not duplicated in a
  line of text above the keyboard.
- **State reads as an outcome, not a toggle.** Instead of an abstract on/off, a button says what's actually true right
  now: `Tournaments: S/A only` vs `Tournaments: all`, `Match results: on` vs `off`.
- **The density matches the question being answered.** A leaderboard answers "who's ahead, by how much"; the upcoming
  matches screen answers "what's playing next"; a personal stats screen answers "how am I doing." Everything that
  isn't that specific question gets pushed one screen deeper.
- **The schedule groups by what a human would group by** — the ten nearest matches, bucketed by tournament and by
  date in the chat's own timezone, tournaments ordered by their first match, matches within a day ordered by kickoff.
  A tournament's name and the date print once; each match keeps just its time, teams, format, and stage — plus a
  small status dot (🔴 live, ✅ finished, ❌ cancelled, ⏸ postponed) so you can tell what's actually happening without
  reading the timestamps.

## The stack

Go 1.27, PostgreSQL 17, [pgx](https://github.com/jackc/pgx) with no ORM in sight,
[goose](https://github.com/pressly/goose) for migrations, the standard library's `net/http` (no web framework),
[Prometheus client_golang](https://github.com/prometheus/client_golang) for metrics, and
[testcontainers-go](https://github.com/testcontainers/testcontainers-go) spinning up a real Postgres for the
integration tests.

The app itself builds with Docker and a handful of shell scripts. Getting it onto a server is Ansible's job — more on
that under "Deploying" below.

## How it's organized

Hexagonal-ish, wired up by hand — there's no DI framework, `cmd/bot/main.go` just constructs everything and passes it
where it needs to go.

```text
cmd/bot                      composition root (main.go)
internal/domain/competition  events, teams, matches, series formats, scores
internal/domain/chat         chat settings, Topics, authorization
internal/domain/subscription chat subscriptions to tournaments
internal/domain/prediction   polls and mutable votes
internal/domain/scoring      point awards, dense ranking, leaderboards, medals
internal/platform/common     shared identifiers and infrastructure ports
internal/adapter/postgres    the pgx-backed port implementations, plus migrations
internal/adapter/pandascore  the external tournament/match catalog
internal/adapter/telegram    webhook, Bot API client, i18n, the whole localized UI
internal/app                 the scheduler, outbox dispatcher, metrics
internal/app/httpapi         HTTP router, middleware, health/version endpoints
docker                       Dockerfile and Compose manifests, dev and production
```

The rule that's actually enforced (by a test walking the import graph with `go list -deps`, not just by convention):
nothing under `internal/domain/*` imports an adapter or `internal/app`. Everything crosses that boundary through an
interface, and anything that needs to notify the outside world goes through the transactional outbox instead of
calling out directly.

## Running it

You need either Docker, or Go 1.27+ with a PostgreSQL 17 of your own.

```bash
cp .env.example .env
# fill in TELEGRAM_BOT_TOKEN, TELEGRAM_WEBHOOK_SECRET, and PANDASCORE_TOKEN
make up
```

`make up` brings up `docker/compose.yml` — Postgres plus the bot built straight from your working tree. It listens on
`127.0.0.1:8080`; nothing about running it locally needs a public hostname or a certificate. `make logs` tails the
containers, `make down` tears the stack down and drops its volumes.

Production is a separate Compose file, `docker/compose.prod.yml` — the same bot, but pulled from a published image
and sitting behind Caddy, which handles its own Let's Encrypt certificate and only proxies the paths the app actually
needs exposed. How that gets onto an actual server is the whole "Deploying" section further down.

Running without Docker, against a database you're managing yourself:

```bash
go run ./cmd/bot
```

Telegram flatly refuses to deliver updates to `localhost`, so testing against a *live* Telegram bot needs a real
public HTTPS address somewhere. Register the webhook once you have one:

```bash
curl -X POST "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/setWebhook" \
  -d "url=https://your-host.example/telegram/webhook" \
  -d "secret_token=${TELEGRAM_WEBHOOK_SECRET}" \
  -d 'allowed_updates=["message","callback_query","poll_answer","my_chat_member"]'
```

Whatever address you use has to be reachable over IPv4: Telegram
[does not deliver webhooks over IPv6](https://core.telegram.org/bots/webhooks), so a name with only an AAAA record
— an IPv6 `nip.io` name, say — registers successfully and then never receives a single update. Dual-stack is fine.

and check it stuck with `curl "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/getWebhookInfo"`. On a real
production server this is a one-time thing done at first install — every deploy after that only changes code and
containers, the webhook URL itself never moves.

The app just won't start without its required secrets — there's no degraded mode. And the PandaScore token
specifically should never end up in anything that ships to a client; it's a server-side credential, full stop.

### Configuring it

Every setting is an environment variable, with sane defaults baked into `internal/app/config.go`. The three that are
actually required: `TELEGRAM_BOT_TOKEN`, `TELEGRAM_WEBHOOK_SECRET`, `PANDASCORE_TOKEN`. Everything past that is
optional — sync delays, HTTP client timeouts, provider ordering and health thresholds, outbox batch sizes. Out of the
box, the event catalog refreshes once an hour (`SYNC_EVENTS_DELAY_MS=3600000`), matches for active subscriptions sync
every 3 minutes (`SYNC_MATCHES_DELAY_MS=180000`), and the digest-timing check runs once a minute
(`DIGEST_CHECK_DELAY_MS=60000`).

## Using it day to day

- `/menu` opens the main menu, anywhere.
- `/events Cologne` searches the catalog and gives you a subscribe button. `/events top Cologne` restricts that to
  S/A tier, `/events all Cologne` searches everything (this overrides whatever the chat's default is). You don't
  even need the command — `Menu → Tournaments → Add tournament → Search by name` sends a ForceReply, and replying
  with part of a name does the same thing. Whatever the chat's already subscribed to gets filtered out of both the
  catalog browser and search results, so you're never looking at duplicates.
- `/stats` in a group opens the stats menu: the latest month and year with data, "All time," per-tournament
  breakdowns, and a period picker if you want something older. The leaderboard itself just shows rank, name, and
  points per row — no clutter repeated for every single person.
- In a DM with the bot, `/start`, `/menu`, and `/stats` all open *your* personal stats across every group you've
  voted in — all-time, yearly, monthly, and a per-chat breakdown if you want to drill in. Votes in different chats
  count separately; the same tournament only counts once toward your combined personal numbers even if two of your
  chats are tracking it.
- End of month, the bot posts that month's Top-3 plus the year's running Top-3. December 31st at 20:00 gets the
  full treatment — December's Top-3, the year's final Top-3, and the six yearly awards mentioned above. The complete
  participant list isn't part of the digest itself; there's a button for that.
- `/timezone Europe/Moscow` changes a chat's timezone (managers only). From the UI, `Settings → Timezone` gives you
  eight common zones one tap away with the current one marked, and the command is still there for anything not on
  that list.
- `/topic here` binds the current Telegram Topic as the chat's default (managers only).
- `Settings → Moderators` lists who's currently a moderator. An admin or an existing moderator appoints someone new
  with `/moderator add` in reply to their message, and `/moderator remove` used to remove instantly — now it asks you
  to confirm first. Stats themselves stay visible to everyone in the chat regardless of role.
- In DM, `My stats → Notifications` has the toggles for personal reminders and result recaps (both off until you turn
  them on), `My stats → Breakdown` has your streaks/form/accuracy/trend, and `My stats → ✏️ My name in lists` is
  where you set the display name mentioned earlier.
- Unsubscribing needs that second confirmation described above, unless it's the bulk "clean up finished tournaments"
  flow, which doesn't need one.

## The Makefile

`make help` lists every target with a one-line description. The ones you'll actually reach for:

```bash
make build             # build the binary into bin/
make run               # run locally, picking up .env
make test              # unit tests
make test-integration  # real Postgres via testcontainers-go — needs Docker
make check             # fmt-check + vet + lint + test, the same thing CI runs
make cover             # unit tests with coverage, report in coverage.html
make up / make down    # bring the local stack up or down
make lint              # golangci-lint, downloaded on demand — no global install needed
```

## Tests

```bash
go vet ./...
go test ./...
go test -tags integration ./...   # a real Postgres via testcontainers-go
```

Unit tests cover the scoring and format business rules, changed votes, permission checks, dense ranking, PandaScore's
mapping layer, the Telegram client's retry behavior (429s honoring `retry_after`), settlement and event-completion
orchestration, advisory locks (a genuine mutual-exclusion test against a real Postgres, not a mock), the
claim/release idempotency pattern, subscription soft-deletes, and the module boundary itself. The integration suite
spins up a real Postgres 17 through testcontainers-go, runs every migration through goose, and walks the full path —
poll, vote, scoring, leaderboard, medal — end to end.

`golangci-lint`, configured in `.golangci.yml`, runs `gosec`, `bodyclose`, `unconvert`, `unparam`, and `misspell` on
top of its usual default set.

## Keeping an eye on it in production

- `GET /healthz/live` just answers "is the process alive."
- `GET /healthz/ready` actually checks something: the database is reachable (a real `Ping`), and every configured
  data provider is answering recently enough. Returns `503` if the DB is down or a provider's gone quiet for too
  long.
- `GET /metrics` exposes the Prometheus surface: `competition_sync_runs_total`, `competition_sync_entities`,
  `competition_provider_calls_total` (with a `circuit_open` label for calls skipped because a breaker tripped),
  `competition_provider_latency_seconds`, `prediction_polls_total`, `outbox_events_total`, `http_requests_total`,
  `http_request_duration_seconds`, and `http_panics_recovered_total` for whatever's behind the observability
  middleware (currently just the webhook).
- `GET /version` reports version/commit/build time, injected via `-ldflags` at `make build`/`docker build` time — a
  bare `go run` will just show `dev`/`unknown` for all three.
- Docker's own `HEALTHCHECK` and the check in `docker/compose.yml` poll `/healthz/live`; production instead waits on
  `/healthz/ready`, because a deploy needs to know migrations actually ran and the database is genuinely answering,
  not just that the process managed to start.
- Logs are structured JSON to stdout via `log/slog`.
- Secrets and the bot token never end up in a log line — transport-level errors from the Telegram client
  deliberately drop the underlying error text (which can contain a URL with the token baked into it) and keep only
  Telegram's own API-provided description.
- Shutdown is graceful: on SIGINT/SIGTERM the server stops taking new requests first, waits for the background jobs
  (sync, outbox) to actually finish what they're doing, and only then closes the database pool — so nothing ends up
  running a query against a connection that's already gone.
- A 429 from the Telegram Bot API gets retried automatically, up to 3 times, honoring `retry_after` and capped at 30
  seconds — but only an explicit 429 triggers a retry, never a plain transport timeout, so a message never risks
  going out twice.

Migrations live in `internal/adapter/postgres/migrations/`, run through goose on startup, each one in its own
transaction. They're one-way by design — nothing in this app ever rolls a migration back automatically.

## Building the image

```bash
make docker-build   # linux/amd64 only — that's the one architecture this actually gets published for
```

`docker/Dockerfile` needs BuildKit — it uses `RUN --mount` for Go's build caches and for an optional build secret.
Docker Desktop and GitHub's own runners have buildx by default; on Colima or a bare Docker Engine you'll need to
install the plugin yourself (`brew install docker-buildx` on macOS), or the build quietly falls back to the classic
builder, which has no idea what `RUN --mount` even means.

Secrets only ever reach the build through `--secret`, never `ARG` or `ENV` — both of those get baked into the image's
history and stay readable by anyone who can pull it, forever. Nothing this project depends on currently needs
authentication to fetch, but the plumbing's already there for the day something does:

```bash
docker buildx build --secret id=netrc,src="$HOME/.netrc" -f docker/Dockerfile .
```

The file gets mounted into a tmpfs for exactly one `RUN` step and disappears with it — it never becomes part of a
layer. Skip the flag and it's simply not there; the build proceeds as if nothing changed.

Only what's explicitly named in `COPY` — `cmd/` and `internal/` — makes it into the image. There's no `COPY . .`
anywhere; what ends up in a layer is a decision made in the Dockerfile itself, not something left to whether some
ignore file happened to catch a stray file that day. `docker/Dockerfile.dockerignore` narrows the build context
further and is written as an allow-list on purpose, so a new file dropped into the working tree doesn't silently end
up inside the image by default.

## Deploying

Merging to `main` triggers `release.yml`, which publishes an image to GHCR and cuts a release tag if the merge
actually warrants one (a `feat`/`fix`/`perf`/`revert` commit, or anything marked breaking — a pure `chore`/`docs`
merge publishes nothing). Actually installing a release on the server is a separate, manual step —
`deploy.yml`, given a specific `vMAJOR.MINOR.PATCH` tag to install.

The deploy itself is Ansible, orchestrated from GitHub Actions: the runner takes its own automation code from the
default branch, pulls the Compose/Caddy configuration from the *tagged release* instead, resolves that tag to an
image digest in GHCR, and verifies the image's signature before touching anything — keyless `cosign`, checked against
the exact identity of `release.yml` itself, so a signature from any other workflow or repository (even one pushed
under the same tag name) gets rejected outright. No git checkout ever happens on the target server. Before changing
anything, Ansible takes and verifies a backup of the current configuration — a failed backup stops the deploy right
there — then brings up the new release and waits on its health checks. A failed health check triggers an automatic
rollback to whatever was running before.

The target VM itself needs to already exist and be reachable over SSH as a scoped-down `cs2deploy` account with
passwordless `sudo` — but only up to the application's own service account (`cs2predictor`), never real root; the
ongoing deploy flow genuinely doesn't need root for anything it does. (If a server was set up under an older,
stricter access model, there's a one-time `ansible/bootstrap.yml` migration for that.) Provisioning the VM itself —
Docker, the service account, the initial directory layout — happens outside this repository entirely, before the
first deploy ever runs; `preflight.yml` checks SSH/sudo/Python/Docker are all in place with a fixed command, without
ever executing anything from a pull request.

The server's `.env` file isn't something you create by hand, either — `deploy.yml` assembles it itself, every run,
from five separate GitHub `production` environment values (`POSTGRES_PASSWORD`, `TELEGRAM_BOT_TOKEN`,
`TELEGRAM_WEBHOOK_SECRET`, `PANDASCORE_TOKEN` as secrets, `CADDY_DOMAIN` as a plain variable). Miss one and the
deploy simply refuses to run. Registering the Telegram webhook is its own separate, optional, manual job — needed
after a release that changes the token or domain (most notably the very first install), not after every single
deploy.

If a deploy fails, the bot can DM its own administrators about it directly — set `DEPLOY_NOTIFY_CHAT_IDS` (a
comma-separated list of Telegram numeric IDs) as a GitHub environment variable and it'll happen automatically; leave
it unset and nothing gets sent, quietly. The same list doubles at runtime as the root operator list for
`/team_matches` (the Valve VRS team-identity review queue — see below): those IDs may run `/team_match_admin` to
delegate reviewer access to others without touching this variable again. It's also the only list allowed to run
`/provider_status`, a DM-only screen showing whether each configured data source (PandaScore, and every enabled
enrichment provider — VRS, HLTV, GRID, Liquipedia) is currently healthy: last successful sync, last error and when it
happened, and how many failures have struck in a row — the same numbers `/healthz/ready` already reports, just
readable from inside a chat instead of curling an endpoint.

All of this — the exact GitHub secrets/variables needed, least-privilege sudo, image signing details, what's actually
in a backup and how to restore one, the full Ansible role breakdown — is written up in much more depth in
[ansible/README.md](ansible/README.md). This section is the tour; that one's the reference manual.
