# Mini App — security notes

A Mini App runs in a WebView this bot does not control. Everything it
sends can be written by hand, and the page itself can be opened by anybody
who knows the URL. This is what that means here and what is done about it.

## Authentication

The only thing a caller cannot forge is Telegram's signature over the
launch parameters, so that signature is the whole of the authentication:
no session, no token of ours, nothing to leak or revoke.

- HMAC-SHA256 per Telegram's documented scheme, compared in constant time.
  A byte-by-byte compare leaks the expected hash one request at a time.
- `auth_date` is bounded (24h, both directions). Without it a signature is
  a credential that never expires, and initData copied out of a client once
  works forever. A launch from the future is somebody else's clock.
- The user is read **from the signed payload only**. Signing the launch and
  then trusting a `user_id` parameter beside it authenticates nothing; there
  is a test for exactly that mistake.
- A rejected launch gets `401` and no detail — and is never written to the
  log, because it is a credentials-shaped string.

## Authorisation

The app shows one person their entire prediction history, so it is closed
by default.

- Everybody starts with no access. They can ask; a root operator decides.
- Root operators (`DEPLOY_NOTIFY_CHAT_IDS`) are exempt in code — they are
  who grants it, and a bootstrap that must grant itself cannot start.
- A refusal is recorded as a decision, so it is not silently re-asked.
- Refused-but-authenticated gets `403` **with** the standing: by then the
  caller has proven who they are, and "not granted yet" is what they need.
- Endpoints about access itself are reachable without access; everything
  else is not. No personal read happens before both checks pass.

## Data handling

- Personal responses are `Cache-Control: no-store`. The public team/crest
  endpoint carries no personal data at all, which is why it is cacheable
  and needs no handshake.
- The page never writes network data into `innerHTML`; team names come from
  a provider and are set as text. XSS in a Telegram WebView would run
  against the launch parameters.
- The page is served by the bot's own binary (`/app/`), so it cannot drift
  from the API it calls, and `nosniff`/`Referrer-Policy` are set on it.
- Caddy's allow-list still governs what is reachable at all: `/app`,
  `/api/miniapp/*` and the three public bot paths. `/metrics` and
  `/healthz/live` remain unreachable from the internet.

## What is deliberately not done

- No cookies and no CSRF machinery: there is no ambient credential to
  forge a request with, since every call carries initData explicitly.
- No rate limit on the personal endpoints yet. They are authenticated and
  access-gated, so the exposure is a granted user hammering their own
  dashboard; the webhook's limiter remains the one that matters. If the
  app grows a public surface, that changes.
