# CS2 Predictor

*[Читать по-русски](README.ru.md)*

A Telegram bot that runs **non-anonymous** match-outcome prediction polls for CS2 in group chats — everyone in the
chat can see who called it right. Matches and results come from PandaScore. Every chat is its own isolated world:
its own subscriptions, settings, leaderboard, and medals, with zero bleed-over between chats.

**The core loop:** a match gets scheduled → the bot posts a poll → people vote before it starts → once it's over,
everyone gets scored and ranked. Everything else in this repo exists to make that loop pleasant to use and safe to
run unattended for months.

## At a glance

- **Message-editing UI.** Tapping through a menu rewrites the same message instead of spamming new ones; a Back
  button is always there. Small confirmations (toggling a setting, subscribing) answer with a popup toast, not a
  new chat line.
- **Chat management lives in DM.** Any admin/moderator manages every group they have rights in from one private
  chat, several panels open at once, each button scoped to its own chat. In the group itself, a menu only answers
  whoever tapped it — everyone else gets a private "not yours" warning.
- **Read-only peeking without admin rights.** A group's stats screen has a "📲 Open in DM" link — a read-only copy
  of that leaderboard in your own DM, nothing you do there can post back to the group.
- **Paginated leaderboards** (10 rows/page) with quick filters for month / year / all-time / by tournament, your
  own row always visible even off-page, plus a ↑/↓ trend versus the previous period.
- **A Mini App** for reading prediction history: an overview of current form, analytics (disciplines/teams you
  read well or badly), full history, awards, and a profile — one shared filter bar drives every screen. **Closed by
  default**; access is requested and granted per person, verified against Telegram's own launch signature (see
  `docs/miniapp/SECURITY.md`).
- **Every notification is opt-in**, each with its own switch — nudges, recaps, digests, alerts, all silent until
  asked for. Direct replies to something you just did (a poll result, an unsubscribe confirmation) are not
  switchable — a reply nobody receives is a broken command, not a quiet one.
- **Personal stats**: current streak, best streak ever, form over the last 10 predictions, per-team accuracy, and a
  30-day trend comparison.
- **Monthly/yearly digests** post automatically at 20:00 chat-local time; December 31st gets a full year-end wrap
  with six awards. Backed by a transactional outbox — nothing skips or double-sends on a failure.
- **Team crests/flags are source-selectable** per chat — PandaScore (every game) or HLTV (CS2 only, generally the
  better-recognized source) — two independent switches, both falling back to the provider when HLTV has no data.
- **Team-data enrichment** (Valve Regional Standings, optionally HLTV ranking / GRID / Liquipedia) is context only
  — PandaScore alone is the source of truth for events, matches, and results. Providers are swappable with a
  circuit breaker: a failing one is skipped with exponentially growing cooldown.
- **What it deliberately skips:** no links to hltv.org (no HLTV ID from PandaScore, and HLTV's ToS forbids
  scraping), no betting odds (needs PandaScore's separate, unavailable Odds product).

<details>
<summary>More features (moderators, unsubscribe flow, tournament search, settings sync, change log, Topics, ops
infrastructure)</summary>

- **A change log per chat** — who changed the language, timezone, tier filter, subscribed, unsubscribed, or became
  a moderator, and when. `Settings → Recent changes`.
- **Settings can be copied across every chat you manage** (language, timezone, tier filter), with a confirmation
  screen naming exactly which chats it touches. Permissions are re-checked per chat at apply time, not cached.
- **Settings live in both the bot and the Mini App**, split the same way in both: what you set for yourself vs.
  what you set for a chat you manage, never mixed on one screen. Every control writes immediately and rolls back
  visibly on failure. Appointing a moderator and the change log stay bot-only (reply-to-message / it's a record,
  not a setting).
- **Your own display name** for leaderboards, independent of your real Telegram name — `My stats → ✏️ My name in
  lists`, one-tap reset back to your Telegram name.
- **Tournament subscriptions**: cleanup of finished tournaments is one button + one confirmation; active ones
  always sort above finished. Search happens in-chat with tier badges (🌟 S, ⭐ A, 🔹 other); `/events top` restricts
  to S/A, `/events all` searches everything. New S/A tournaments get proactively offered to every unsubscribed
  chat.
- **Polls** support BO1/BO2/BO3/BO5 plus arbitrary formats, close on schedule or the moment a match goes live
  (whichever first), follow reschedules, and void outright on cancellation/forfeit. Fully idempotent against
  duplicate webhook deliveries and duplicate results.
- **Moderators** are appointed via `/moderator add` (reply to their message). Removal now requires an explicit
  confirmation screen instead of firing on the first tap.
- **Unsubscribing needs a second signature** from another active admin/moderator when one is reachable; otherwise
  you confirm it yourself a second time. A pending request can be withdrawn. Bulk cleanup of finished tournaments
  skips this — there's no live poll left to protect.
- **Telegram Topics** are supported (chat-wide default + per-tournament override), plus the operational basics:
  transactional outbox, PostgreSQL advisory locks, structured JSON logs, health checks, Prometheus metrics, a
  retention job with per-table TTLs, and Bot API calls that self-throttle against Telegram's rate limits.

</details>

## How the UI is put together

A few rules run through every screen, some learned the hard way:

- **Progressive disclosure.** A list exists to pick *one* thing, not to expose every action on every row — e.g.
  "My tournaments" lists tournaments; stats/unsubscribe/Topic-binding live one tap deeper, on that tournament's
  own card.
- **A setting's value lives on its own button.** `Language: English`, `Timezone: Europe/Moscow` — the current
  state sits on the button you'd tap to change it, never duplicated above the keyboard.
- **State reads as an outcome, not a toggle** — `Tournaments: S/A only` vs `Tournaments: all`, not a bare on/off.
- **Density matches the question being answered.** A leaderboard answers "who's ahead"; the schedule answers
  "what's next"; personal stats answers "how am I doing." Anything outside that question moves one screen deeper.
- **The schedule groups the way a human would** — nearest matches bucketed by tournament and local date, a small
  status dot (🔴 live, ✅ finished, ❌ cancelled, ⏸ postponed) instead of making you read timestamps.

## The stack

Go 1.27 · PostgreSQL 17 · [pgx](https://github.com/jackc/pgx) (no ORM) · [goose](https://github.com/pressly/goose)
migrations · standard library `net/http` (no web framework) ·
[Prometheus client_golang](https://github.com/prometheus/client_golang) ·
[testcontainers-go](https://github.com/testcontainers/testcontainers-go) for integration tests against a real
Postgres.

Builds with Docker and a handful of shell scripts. Getting it onto a server is Ansible's job — see
[Deploying](#deploying).

## How it's organized

Hexagonal-ish, wired by hand — no DI framework, `cmd/bot/main.go` just constructs everything.

```text
cmd/bot                      composition root (main.go)
internal/domain/competition  events, teams, matches, series formats, scores
internal/domain/chat         chat settings, Topics, authorization
internal/domain/subscription chat subscriptions to tournaments
internal/domain/prediction   polls and mutable votes
internal/domain/scoring      point awards, dense ranking, leaderboards, medals
internal/platform/common     shared identifiers and infrastructure ports
internal/adapter/postgres    pgx-backed port implementations, plus migrations
internal/adapter/pandascore  the external tournament/match catalog
internal/adapter/telegram    webhook, Bot API client, i18n, the whole localized UI
internal/miniapp             Mini App static assets (served by internal/app/httpapi)
internal/app                 scheduler, outbox dispatcher, metrics
internal/app/httpapi         HTTP router, middleware, health/version/Mini App endpoints
docker                       Dockerfile and Compose manifests, dev and production
```

**Enforced, not just convention:** a test walks the import graph with `go list -deps` — nothing under
`internal/domain/*` may import an adapter or `internal/app`. Everything crosses that boundary through an
interface; notifying the outside world goes through the transactional outbox, never a direct call.

## Running it locally

Needs either Docker, or Go 1.27+ with your own PostgreSQL 17.

```bash
cp .env.example .env
# fill in TELEGRAM_BOT_TOKEN, TELEGRAM_WEBHOOK_SECRET, PANDASCORE_TOKEN
make up          # docker/compose.yml — Postgres + the bot, built from your working tree
make logs        # tail the containers
make down        # tear the stack down and drop its volumes
```

`make up` listens on `127.0.0.1:8080` — no public hostname or certificate needed locally. Production uses a
separate Compose file (`docker/compose.prod.yml`) behind Caddy; see [Deploying](#deploying).

Without Docker, against a database you manage yourself:

```bash
go run ./cmd/bot
```

**A live Telegram bot needs a public HTTPS address** — Telegram refuses to deliver updates to `localhost`. Once
you have one, register the webhook:

```bash
curl -X POST "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/setWebhook" \
  -d "url=https://your-host.example/telegram/webhook" \
  -d "secret_token=${TELEGRAM_WEBHOOK_SECRET}" \
  -d 'allowed_updates=["message","callback_query","poll_answer","my_chat_member"]'

curl "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/getWebhookInfo"   # confirm it stuck
```

> The address must be reachable over IPv4 — Telegram
> [does not deliver webhooks over IPv6](https://core.telegram.org/bots/webhooks); an IPv6-only name registers
> successfully and then silently never receives an update. Dual-stack is fine. On a real server this is a one-time
> setup step — every later deploy only changes code and containers.

The app **won't start** without its required secrets (no degraded mode), and `PANDASCORE_TOKEN` specifically must
never reach a client — it's server-side only.

### Configuration

Every setting is an environment variable with sane defaults in `internal/app/config.go`. Required:
`TELEGRAM_BOT_TOKEN`, `TELEGRAM_WEBHOOK_SECRET`, `PANDASCORE_TOKEN`. Everything else is optional (sync delays,
HTTP timeouts, provider ordering, outbox batch sizes). Defaults: event catalog refreshes hourly
(`SYNC_EVENTS_DELAY_MS=3600000`), matches sync every 3 minutes (`SYNC_MATCHES_DELAY_MS=180000`), digest-timing
check runs every minute (`DIGEST_CHECK_DELAY_MS=60000`).

## Using it day to day

| Where | Command / action | What happens |
|---|---|---|
| Group | `/menu` | Opens the main menu |
| Group | `/events Cologne` (`top`/`all` variants) | Search + subscribe; `Tournaments → Add → Search by name` does the same via ForceReply. Already-subscribed events are filtered out. |
| Group | `/stats` | Latest month/year with data, all-time, per-tournament, a period picker for older data |
| Group | `/timezone Europe/Moscow` | Sets the chat's timezone (managers only) — or `Settings → Timezone` for 8 common zones one tap away |
| Group | `/topic here` | Binds the current Topic as the chat's default (managers only) |
| Group | `Settings → Moderators` | Lists moderators; `/moderator add`/`remove` (reply to their message) appoints/removes, removal now asks to confirm |
| DM | `/start`, `/menu` | Opens your personal cabinet — results, form, bets, settings |
| DM | `/stats` | Opens your results across every group you've voted in — all-time/yearly/monthly/per-chat. Votes in different chats count separately; a tournament tracked by two of your chats counts once toward your combined numbers. |
| DM | `🔥 Моя форма` | Streaks/form/accuracy/trend, with a link into the Mini App's deeper analytics |
| DM | Notifications toggle | Personal reminders and result recaps, off by default |
| Any | Unsubscribe | Needs the second confirmation described above, except the bulk "clean up finished tournaments" flow |

End of month, the bot posts that month's Top-3 plus the year's running Top-3; December 31st at 20:00 adds the six
yearly awards. The full participant list is one button away, not crammed into the digest itself.

## The Makefile

`make help` lists every target. The ones you'll actually reach for:

```bash
make build             # build the binary into bin/
make run               # run locally, picking up .env
make test              # unit tests
make test-integration  # real Postgres via testcontainers-go — needs Docker
make check             # fmt-check + vet + lint + test — same as CI
make cover             # unit tests with coverage → coverage.html
make up / make down    # bring the local stack up or down
make lint              # golangci-lint, downloaded on demand
```

## Tests

```bash
go vet ./...
go test ./...
go test -tags integration ./...   # real Postgres via testcontainers-go
```

Unit tests cover scoring/format business rules, changed votes, permission checks, dense ranking, PandaScore's
mapping layer, the Telegram client's retry behavior (`retry_after` on 429s), settlement/event-completion
orchestration, advisory locks (a genuine mutual-exclusion test against a real Postgres, not a mock), the
claim/release idempotency pattern, subscription soft-deletes, and the module boundary itself. The integration
suite spins up a real Postgres 17, runs every migration through goose, and walks poll → vote → scoring →
leaderboard → medal end to end.

`golangci-lint` (`.golangci.yml`) adds `gosec`, `bodyclose`, `unconvert`, `unparam`, and `misspell` to its default
set.

## Keeping an eye on it in production

- `GET /healthz/live` — is the process alive.
- `GET /healthz/ready` — database reachable (real `Ping`) + every configured data provider answering recently
  enough; `503` otherwise.
- `GET /metrics` — Prometheus surface: `competition_sync_runs_total`, `competition_sync_entities`,
  `competition_provider_calls_total` (with a `circuit_open` label), `competition_provider_latency_seconds`,
  `prediction_polls_total`, `outbox_events_total`, `http_requests_total`, `http_request_duration_seconds`,
  `http_panics_recovered_total`.
- `GET /version` — version/commit/build time, injected via `-ldflags` at build time (`dev`/`unknown` on a bare
  `go run`).
- Docker's `HEALTHCHECK` polls `/healthz/live`; production waits on `/healthz/ready` (needs migrations to have run
  and the database genuinely answering, not just the process having started).
- Structured JSON logs to stdout via `log/slog`. Secrets never reach a log line — transport errors from the
  Telegram client drop the underlying error text (which can carry the token in a URL) and keep only Telegram's own
  description.
- Graceful shutdown on SIGINT/SIGTERM: stop new requests → wait for background jobs (sync, outbox) to finish →
  close the database pool.
- A 429 from the Bot API retries automatically (up to 3×, honoring `retry_after`, capped at 30s) — never a plain
  transport timeout, so nothing risks sending twice.

Migrations live in `internal/adapter/postgres/migrations/`, run through goose on startup, one per transaction.
One-way by design — nothing rolls back automatically.

## Building the image

```bash
make docker-build   # linux/amd64 only
```

`docker/Dockerfile` needs BuildKit (`RUN --mount` for build caches and an optional build secret). Docker Desktop
and GitHub's runners have buildx by default; elsewhere install the plugin or the build silently falls back to the
classic builder, which doesn't understand `RUN --mount`.

Secrets reach the build only through `--secret`, never `ARG`/`ENV` (both get baked into image history):

```bash
docker buildx build --secret id=netrc,src="$HOME/.netrc" -f docker/Dockerfile .
```

The file is mounted into a tmpfs for one `RUN` step and never becomes part of a layer; skip the flag and it's
simply not there.

Only `cmd/` and `internal/` (explicitly named in `COPY`) make it into the image — no `COPY . .` anywhere.
`docker/Dockerfile.dockerignore` is an allow-list on purpose, so a stray new file doesn't silently ship.

## Deploying

- Merging to `main` triggers `release.yml`: publishes an image to GHCR and cuts a release tag if the merge
  warrants one (`feat`/`fix`/`perf`/`revert`/breaking — a pure `chore`/`docs` merge publishes nothing).
- Installing a release is a **separate, manual** step: `deploy.yml`, given a `vMAJOR.MINOR.PATCH` tag.
- The deploy itself is Ansible from GitHub Actions: automation code comes from the default branch, but
  Compose/Caddy config comes from the *tagged release*; the tag resolves to a GHCR image digest and its signature
  is verified (keyless `cosign`, checked against `release.yml`'s exact identity — a signature from any other
  workflow/repo is rejected, even under the same tag name). No git checkout ever happens on the target server.
  Before changing anything, Ansible takes and verifies a config backup — a failed backup stops the deploy. A
  failed health check after the new release comes up triggers an automatic rollback.
- The target VM must already exist, reachable over SSH as a scoped-down `cs2deploy` account with passwordless
  `sudo` limited to the `cs2predictor` service account — never real root. (An older, stricter access model has a
  one-time `ansible/bootstrap.yml` migration.) VM provisioning (Docker, the service account, directory layout)
  happens outside this repo, before the first deploy; `preflight.yml` checks SSH/sudo/Python/Docker are in place
  without executing anything from a pull request.
- The server's `.env` is assembled by `deploy.yml` itself, every run, from five GitHub `production` environment
  values (`POSTGRES_PASSWORD`, `TELEGRAM_BOT_TOKEN`, `TELEGRAM_WEBHOOK_SECRET`, `PANDASCORE_TOKEN` as secrets,
  `CADDY_DOMAIN` as a plain variable) — miss one and the deploy refuses to run. Registering the Telegram webhook is
  a separate, optional, manual job, needed only when the token or domain changes (most notably the first install).
- A failed deploy can DM the bot's administrators directly — set `DEPLOY_NOTIFY_CHAT_IDS` (comma-separated
  Telegram numeric IDs) as a GitHub environment variable; unset means silent. The same list doubles as the root
  operator list for `/team_matches` (the Valve VRS team-identity review queue) — those IDs can run
  `/team_match_admin` to delegate reviewer access, and are the only ones allowed to run `/provider_status` (DM-only
  health screen for every configured data source: PandaScore, VRS, HLTV, GRID, Liquipedia).

All of this — exact GitHub secrets/variables, least-privilege sudo, image signing, backup/restore, the full
Ansible role breakdown — is documented in depth in [ansible/README.md](ansible/README.md). This section is the
tour; that one's the reference manual.

### Mini App API

`GET /api/miniapp/v1/teams?game=<cs2|dota2>&logos=<provider|hltv>` returns the game's teams with their crests,
collected by the match sync (PandaScore, every game) and the weekly ranking fetch (HLTV, Counter-Strike). Carries
no personal data, so it's cacheable and needs no `initData` handshake — see `docs/miniapp/TEAM_LOGOS.md`.
