# Team crests — where they come from

The Mini App renders boards of teams, and a board of names is much slower
to read than a board of crests. This is where those crests come from, why,
and what happens when a team has none.

## Sources

| Source | Games | Coverage | Cost |
|---|---|---|---|
| **PandaScore** (`image_url` on the match's opponents) | every game this bot follows | every team that appears in a match, including qualifier fields | none — it arrives inside the match sync that already runs |
| **HLTV world ranking** (`team.logo` in the Apify payload) | Counter-Strike only | the ranked teams — thirty of them | none — it arrives inside the weekly ranking fetch that already runs |

Both are collected as a by-product of requests the bot already makes. No
new integration, no extra request budget, and no scraping of a page that
was not already being read.

PandaScore is the default because it is the only source that answers for
every game and every team. It was checked against the live API before
being chosen: CS2, Dota 2, Valorant and LoL all return `image_url` on
their teams, and the CS2 teams anybody recognises (Vitality, FURIA,
Eternal Fire…) all carry one.

HLTV is kept because for Counter-Strike it is the picture that audience
knows. It is a **display preference**, not a data decision: both URLs are
stored side by side (`team.logo_url`, `team.hltv_logo_url`), and the chat
setting *«Логотипы команд (CS2)»* switches which one is rendered. Changing
your mind costs nothing and re-fetches nothing.

## Fallbacks

1. The preferred source's crest.
2. The other source's crest — HLTV ranks thirty teams, so most teams fall
   here.
3. The team's initials, drawn by the Mini App itself.

A crest that fails to load at render time also falls back to initials
rather than leaving a broken image.

## Other games

Valorant and League of Legends are already covered by the same PandaScore
field, so no second source is needed for them today. If a game-specific
feed is ever worth adding — Liquipedia for a discipline PandaScore covers
thinly, say — it should follow the shape HLTV already uses here: its own
column, its own writer, and a preference that decides what is shown.
Nothing should overwrite another source's column.

## Production note

The prototype consumed a hand-written JSON file of six HLTV URLs. That is
gone: crests now come from `GET /api/miniapp/v1/teams?game=<code>`, which
serves this bot's own catalogue. The URLs it returns still point at the
providers' CDNs; caching or proxying them under our own domain is the next
step if third-party CDN availability ever becomes a problem, and the API
shape does not change when it does.
