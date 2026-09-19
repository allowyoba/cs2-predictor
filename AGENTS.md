# Agent instructions for this repository

Standing requirements the project owner has given across sessions. Follow these
without being asked again.

## Git workflow

- Never run `git config`.
- Never push to a remote branch unless it's the natural conclusion of a
  completed, tested feature — not as an intermediate step.
- Always `git fetch` and check the remote branch state before assuming it's
  current — `main`/other branches can move forward from actions outside the
  current session (merges, other sessions, CI).
- Commit messages: **title only**, in Conventional Commits form
  (`feat: …`, `fix: …`, `chore(scope): …`) so the title alone carries the
  semver bump — no body, no `Co-Authored-By` footer, no "Generated with
  Claude Code" line.
- Pull requests: title only, same Conventional Commits form. No description
  body, no co-authors, no "🤖 Generated with Claude Code" footer.

## Verification before calling anything done

- Every code change must pass the full `make check` (fmt, vet, lint, unit
  tests) and `make test-integration` (real Postgres via testcontainers)
  before being considered finished — not just a targeted subset, unless
  explicitly doing a quick intermediate check mid-task.
- Never fabricate version numbers, API shapes, pricing, or other external
  facts. Verify anything used in code or told to the user via a live
  WebSearch/WebFetch (or an actual sample response/run) first. If something
  can't be verified, say so explicitly rather than presenting a guess as
  fact.

## Config and deploy safety

- Any new optional environment variable must be safe to leave completely
  undeclared end to end: GitHub Actions vars/secrets resolving to an empty
  string, Ansible writing an empty value into `.env`, Docker Compose's
  `${VAR:-}` substitution, and the app's own env-parsing all treating "unset"
  and "empty" the same way (falling back to a sane default, never failing to
  start or breaking config validation).

## Code reuse

- When adding a new data source or provider that's conceptually analogous to
  an existing one (e.g. a new ranking feed alongside an existing one), prefer
  generalizing the existing code to handle both rather than duplicating it
  into a parallel implementation.

## Comments

- Keep comments brief. A short note on the non-obvious *why* is enough — no
  multi-paragraph rationale, no restating what the code already says.
