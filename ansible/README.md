# Ansible deployment

The repository owns **application deployment**, plus the one-time migration of a host's SSH/sudo access to the model
that deployment needs. Everything else about the VM — Docker Engine, the Compose plugin, firewalling, the
`cs2predictor` service account and directory, `<APP_PATH>/.env` — is provisioned outside this repository, before either
`bootstrap.yml` or
`site.yml` ever runs.

By the time the ongoing deploy flow (`site.yml`, driven by GitHub Actions)
runs, the target must already have:

- SSH access for `cs2deploy` using the private key stored as `DEPLOY_KEY`, with ordinary SSH exec/SFTP (no forced
  command) and passwordless `sudo` — see "Host access migration" below if it does not yet;
- Python 3 available as `/usr/bin/python3`;
- Docker Engine and the Docker Compose plugin;
- the service account `cs2predictor`;
- the application directory, normally `/opt/cs2predictor`;
- `<APP_PATH>/.env`, owned by the service account and mode `0600`;
- inbound TCP 80/443 for Caddy and the configured SSH port (normally 22).

Git, Ansible and repository credentials are not required on the target VM.

## Entry points and roles

Every playbook is a thin list of roles — none carries its own inline `tasks:`
beyond a single `include_role` where composing two roles needs precise ordering (`prepare_deploy`, see below). All the
actual logic lives in roles.

| Entry point          | Responsibility                                                                                                                |
|----------------------|-------------------------------------------------------------------------------------------------------------------------------|
| `site.yml`           | Full GitHub Actions deployment: prepare controller state, build dynamic inventory, deploy release, remove temporary SSH files |
| `bootstrap.yml`      | One-time migration: apply `host_access`, then `validate_host`                                                                 |
| `deploy.yml`         | Install an already-resolved release against an existing inventory                                                             |
| `webhook.yml`        | Manual, optional: re-register the Telegram webhook using secrets read from the server's own `.env`                            |
| `notify_failure.yml` | Triggered only by `deploy.yml`'s own failure: DM the bot's administrators, using secrets read from the server's own `.env`    |

| Role                   | Responsibility                                                                                                                                                             |
|------------------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `prepare_connection`   | Validate SSH/APP_PATH inputs, write the private key and known_hosts, add the target to the in-memory inventory — shared by every playbook that needs to reach `production` |
| `prepare_deploy`       | Validate release inputs, resolve the GHCR tag to an immutable digest, **then** include `prepare_connection` and attach the release metadata to that host                   |
| `host_access`          | Migrate a host's SSH/sudo policy for `cs2deploy` to ordinary exec/SFTP plus passwordless root sudo, reusing its existing keypair                                           |
| `validate_host`        | Read-only: confirm Python, Docker, Compose and the application account/directory are all present                                                                           |
| `configuration_backup` | Archive and verify existing application configuration before changes                                                                                                       |
| `application_deploy`   | Lock the target, back up configuration, pull the image, install Compose/Caddy configuration and wait for health checks                                                     |
| `register_webhook`     | Read the server's `.env` and call Telegram's `setWebhook`                                                                                                                  |
| `notify_admins`        | Read the server's `.env` and DM every configured administrator                                                                                                             |

`prepare_deploy` composes `prepare_connection` with an explicit
`include_role` (`public: true`) rather than a `meta` dependency, and deliberately late in its own task list: a `meta`
dependency runs to completion *before* the depending role's own tasks, which would write the SSH key before
`prepare_deploy` had validated its own release inputs (tag, repository, registry credentials) or resolved the digest —
both must fail without anything having touched disk first, and both are tested (see
`tests/test_prepare.py`).

`site.yml` is the normal CI entry point. `deploy.yml` is useful for local administrative runs where inventory and
release variables are supplied directly. `webhook.yml`, `bootstrap.yml` and `notify_failure.yml` are never run as part
of `site.yml` or the ordinary deploy workflow — the first two by hand, the third automatically but only on failure.

## Host access migration

A host provisioned with an older, more restrictive setup — a `cs2deploy` SSH key locked to a single fixed command via
`ForceCommand`, with no general
`sudo` — cannot run this repository's deploy flow at all: Ansible needs an ordinary SSH exec/SFTP channel and
`become: true`. `ansible/bootstrap.yml`
migrates such a host once, using the `host_access` role.

Run it by hand, connecting as the pre-existing `admin` account (which already has unrestricted root `sudo`), against a
private inventory that is **never committed**:

```sh
python3 -m venv .venv-ansible
.venv-ansible/bin/pip install -r ansible/requirements.txt
.venv-ansible/bin/ansible-playbook -i /path/to/admin-inventory.yml ansible/bootstrap.yml
```

`host_access` reuses the SSH keypair already installed for `cs2deploy` rather than rotating it — provided
`authorized_keys` holds exactly one key — so
`DEPLOY_KEY` in GitHub Actions does not need to change after migration. It is idempotent: running it again on an
already-migrated host changes nothing. This is a one-time operation per host, not part of the ongoing GitHub Actions
deploy flow, which always targets the already-migrated `cs2deploy` account.

## Least-privilege deploy

`host_access` grants `cs2deploy` sudo scoped to the application account, not root:

```sudoers
cs2deploy ALL=(cs2predictor) NOPASSWD: ALL
```

`site.yml`/`deploy.yml`/`webhook.yml`/`notify_failure.yml` all set
`become_user: "{{ app_user }}"` on their `production` play to match — the ongoing deploy flow never asks for real root,
because nothing in it needs root. `application_deploy`'s own backup (`configuration_backup` with
`backup_owner`/`backup_group` set to `app_user`) only ever touches files
`cs2predictor` already owns; everything else is `docker`/`docker compose`, which `cs2predictor`'s membership in the
`docker` group already covers.

This is real defense in depth, not a complete one: Docker group membership is its own well-known path to root
(`docker run -v /:/host … chroot /host`), so a compromised `DEPLOY_KEY` still ultimately reaches root through `docker`,
just not directly through `sudo`. What scoping sudo this way does buy — cheaply, with no new infrastructure — is a
meaningfully different audit trail (`sudo -u cs2predictor` instead of `sudo -u root` in the log), and a bug or a bad
value in a playbook can no longer touch anything outside
`cs2predictor`'s own scope by simple oversight. `host_access` (SSH/sudoers/ account management) and `admin`'s own access
remain root-only, deliberately — that migration genuinely needs it.

Ansible's `become` to a **non-root** user additionally requires the `acl`
package on the target (it uses POSIX ACLs to hand the become_user read access to temp files the connecting user creates,
without making them world readable) — see `ansible/tests/Dockerfile` for the exact requirement this mirrors. Without
`acl` installed, every play here fails immediately with a
"Failed to set permissions on the temporary files..." error.

### Auditing sudo and Docker usage

Not something this repository can configure — it's server-side, outside Ansible's scope here — but worth setting up
given `cs2deploy` and `admin` both ultimately reach root (see above): install `auditd` and watch `execve` calls made
through `sudo`, so a compromised key leaves a full command history instead of just an SSH login line:

```
-a always,exit -F arch=b64 -S execve -F euid=0 -F auid!=-1 -k root_exec
```

(`auid!=-1` excludes processes with no login session, mainly the kernel's own; everything else that ends up running as
root — via `sudo -u
cs2predictor`'s own subsequent `docker` calls included, once they reach root through the Docker socket — gets logged
with the original login's audit ID attached.) `ausearch -k root_exec` then answers "what did this key actually do" after
the fact, which SSH/sudo logs alone do not.

## GitHub production environment

Both `.github/workflows/deploy.yml` and `.github/workflows/webhook.yml` read these settings from the GitHub `production`
environment; `webhook.yml` uses only the connection-related ones (`APP_USER` is irrelevant to it):

| GitHub setting          | Kind     | Use                                                               |
|-------------------------|----------|-------------------------------------------------------------------|
| `DEPLOY_HOST`           | Variable | **Required.** VM hostname or IP address                           |
| `DEPLOY_KEY`            | Secret   | **Required.** Private SSH key for `cs2deploy`                     |
| `KNOWN_HOSTS`           | Secret   | **Required.** Trusted SSH host-key entries for the VM on port 22 |
| `DEPLOY_PORT`           | Variable | Optional override; default `22`                                  |
| `DEPLOY_USER`           | Variable | Optional override; default `cs2deploy`                            |
| `APP_USER`              | Variable | Optional override; default `cs2predictor`                         |
| `APP_PATH`              | Variable | Optional override; default `/opt/cs2predictor`                    |
| `PREFLIGHT_DEPLOY_KEY`  | Secret   | Optional. See "Preflight credential" below                        |
| `PREFLIGHT_KNOWN_HOSTS` | Secret   | Optional. See "Preflight credential" below                        |
| `PREFLIGHT_DEPLOY_USER` | Variable | Optional. See "Preflight credential" below                        |
| `POSTGRES_PASSWORD`     | Secret   | **Required.** Written into the server `.env` — see "Server environment file" below                                |
| `TELEGRAM_BOT_TOKEN`    | Secret   | **Required.** Written into the server `.env`                                                                      |
| `TELEGRAM_WEBHOOK_SECRET`| Secret  | **Required.** Written into the server `.env`                                                                      |
| `PANDASCORE_TOKEN`      | Secret   | **Required.** Written into the server `.env`                                                                      |
| `CADDY_DOMAIN`          | Variable | **Required.** Written into the server `.env`; not sensitive on its own, so it's a variable, not a secret          |
| `VALVE_VRS_ENABLED`, `VALVE_VRS_SYNC_INTERVAL` | Variable | Optional. Written into the server `.env`; unset leaves the enrichment provider on its own default (see `.env.example`) |
| `GRID_ENABLED`, `GRID_SYNC_INTERVAL`   | Variable | Optional. Written into the server `.env`; unset leaves the provider disabled |
| `GRID_API_KEY`          | Secret   | Optional. Written into the server `.env`; required only once `GRID_ENABLED=true` |
| `LIQUIPEDIA_ENABLED`, `LIQUIPEDIA_SYNC_INTERVAL` | Variable | Optional. Written into the server `.env`; unset leaves the provider disabled |
| `LIQUIPEDIA_API_KEY`    | Secret   | Optional. Written into the server `.env`; required only once `LIQUIPEDIA_ENABLED=true` |
| `HLTV_ENABLED`, `APIFY_RANKING_CHECK_INTERVAL`, `APIFY_MAX_TEAMS` | Variable | Optional. Written into the server `.env`; unset leaves the provider disabled |
| `APIFY_TOKEN`           | Secret   | Optional. Written into the server `.env`; required only once `HLTV_ENABLED=true` |
| `DB_BACKUP_ENABLED`, `DB_BACKUP_RETENTION_DAYS`, `DB_BACKUP_STORAGE` | Variable | Optional. Read directly by Ansible, not written to `.env`; unset defaults to enabled, 5 days, filesystem — see "Database backups" below |
| `DB_BACKUP_S3_ENDPOINT`, `DB_BACKUP_S3_REGION`, `DB_BACKUP_S3_BUCKET`, `DB_BACKUP_S3_PREFIX` | Variable | Optional, only used when `DB_BACKUP_STORAGE=s3`; endpoint/region default to Yandex Cloud Object Storage |
| `DB_BACKUP_S3_ACCESS_KEY_ID`, `DB_BACKUP_S3_SECRET_ACCESS_KEY` | Secret   | Required only once `DB_BACKUP_STORAGE=s3` |

These five are the one exception to "no application secret transits GitHub Actions": deploy needs to *guarantee*
`.env` exists on the target with correct content, ownership and mode, not depend on someone having created it by hand
first, so GitHub is their single source of truth. Each lives as its own GitHub secret/variable rather than one blob —
easier to rotate and audit individually. `webhook.yml` and `notify_failure.yml` don't read any of them — they read
whatever `.env` a previous `deploy.yml` run already assembled and wrote to the VM.

### Preflight credential

`preflight.yml` runs on every pull request (`pull_request_target`, unreviewed — see its own comment for why that's still
safe) while `deploy.yml` and
`webhook.yml` are manual and gated by the environment's required reviewers (see "Environment protection" below). Sharing
one SSH key between an automatic, unreviewed trigger and a manual, reviewed one means the automatic path holds the same
access as the reviewed one — worth avoiding once the environment actually has a reviewer configured.

Set `PREFLIGHT_DEPLOY_KEY`/`PREFLIGHT_KNOWN_HOSTS`/`PREFLIGHT_DEPLOY_USER` to give preflight a separate key for a
dedicated account; `preflight.yml` falls back to `DEPLOY_KEY`/`KNOWN_HOSTS`/`DEPLOY_USER` when they're unset, so
adopting this is optional and backward compatible. The dedicated account still needs `sudo -n -u cs2predictor` scoped
the same way as `cs2deploy` (see
"Least-privilege deploy") to run the same `docker`/`python3` checks — a separate key on its own only helps if it's
actually a separate account, not
`cs2deploy` reused under another name.

### Environment protection

`scripts/configure-repository.sh OWNER/REPO [REVIEWER...]` also configures the `production` environment:
`prevent_self_review` and, if you pass any usernames, `reviewers` — without at least one, `environment: production`
only supplies secrets, it isn't a gate on anything, since GitHub does not require a reviewer by default. It also
restricts deployments from this environment to protected branches, matching the branch protection the same script
applies above.

`deploy.yml` additionally supplies these deployment-scoped values:

- `TAG` — release tag in `vMAJOR.MINOR.PATCH` form;
- `REPOSITORY` — `owner/repository`, used to locate the GHCR package;
- `REGISTRY_USER` / `REGISTRY_TOKEN` — credentials that can pull the package;
- `RUNNER_TEMP` — temporary directory for controller-only SSH files.

`prepare_deploy` validates these values before writing the private key or known hosts file. It resolves
`ghcr.io/<repository>:<tag>` to a digest and adds the VM to an in-memory `production` inventory. The target therefore
receives an image reference of the form `ghcr.io/owner/repo@sha256:...`, never a mutable tag.
`webhook.yml` does the same SSH-transport preparation on its own — it needs no release information, so it skips
`prepare_deploy` entirely.

## Server environment file

`<APP_PATH>/.env` is assembled and written by `application_deploy` on every `deploy.yml` run, from the five
individual GitHub `production` environment entries listed above (`POSTGRES_PASSWORD`, `TELEGRAM_BOT_TOKEN`,
`TELEGRAM_WEBHOOK_SECRET`, `PANDASCORE_TOKEN`, `CADDY_DOMAIN`) — nothing to create by hand, and no deploy can ever run
against a VM where any of them is unset. It's written *after* `configuration_backup` runs, so the previous contents
are always recoverable, and a failed health check rolls it back alongside the previous image (see
`application_deploy`'s rescue block) — a bad value doesn't strand the VM on a release it can't run.

Every optional enrichment variable in the table above (`VALVE_VRS_*`, `GRID_*`, `LIQUIPEDIA_*`, `HLTV_*`, `APIFY_TOKEN`)
is written the same way, defaulting to an empty string when the GitHub `production` environment doesn't define it —
never omitted, so a deploy never fails just because one of these was never configured. An empty value reaches the bot
container as an empty string (see `compose.prod.yml`), and `internal/app/config.go` treats that exactly like the
variable being unset at all: each provider falls back to its own default (`VALVE_VRS_ENABLED` defaults on, the rest
default off).

The resulting file looks like:

```dotenv
POSTGRES_PASSWORD=...
TELEGRAM_BOT_TOKEN=...
TELEGRAM_WEBHOOK_SECRET=...
PANDASCORE_TOKEN=...
CADDY_DOMAIN=bot.example.com
DEPLOY_NOTIFY_CHAT_IDS=
VALVE_VRS_ENABLED=
VALVE_VRS_SYNC_INTERVAL=
GRID_ENABLED=
GRID_API_KEY=
GRID_SYNC_INTERVAL=
LIQUIPEDIA_ENABLED=
LIQUIPEDIA_API_KEY=
LIQUIPEDIA_SYNC_INTERVAL=
HLTV_ENABLED=
APIFY_TOKEN=
APIFY_RANKING_CHECK_INTERVAL=
APIFY_MAX_TEAMS=
```

Ansible always writes it with owner `APP_USER` and mode `0600`, regardless of what these values contain. `APP_IMAGE`
must not be stored there; Ansible supplies it only to the Compose invocation.

`register_webhook` and `notify_admins` read the file already on the VM with `ansible.builtin.slurp`
and parse it as plain `KEY=VALUE` lines — they never `source` it as a shell script, so a value can't execute anything
even if it were somehow malformed or tampered with.

Admin chat IDs to notify on a failed deploy are *not* part of this file — see `DEPLOY_NOTIFY_CHAT_IDS` in "GitHub
production environment" above. They aren't a real secret (just Telegram numeric user IDs, get one from a bot like
@userinfobot), and keeping them out of `.env` means `notify_admins` never needs `.env` to exist at all when nobody is
configured to be notified — useful before a VM has been fully provisioned. `DEPLOY_NOTIFY_CHAT_IDS` may freely contain
spaces after the commas; each ID is trimmed.

## Database backups

`application_deploy` runs the `database_backup` role immediately after `configuration_backup` and before the new
`.env` is written — a `pg_dump -Fc` of the *previous* release's still-running Postgres container, taken before
anything about the deploy changes. A failed backup fails the deploy the same way a failed configuration backup does
(same outer `rescue`/`always`, so the deployment lock is always released); nothing is pulled, installed, or health-checked
until it succeeds. The one exception is a genuine first install — there's no previous database to back up yet, so the
role skips itself when no `deployment.json` from an earlier deploy exists.

Every setting defaults safely with nothing configured: backups are enabled, kept for 5 days, and stored under
`<APP_PATH>/backups/database` on the VM's own filesystem (`ansible/roles/database_backup/defaults/main.yml`). Set
`DB_BACKUP_STORAGE=s3` to upload to an S3-compatible bucket instead — `DB_BACKUP_S3_ENDPOINT`/`DB_BACKUP_S3_REGION`
default to Yandex Cloud Object Storage (`https://storage.yandexcloud.net`, `ru-central1`), this project's default
provider, but any S3-compatible endpoint works by overriding them. S3 retention is enforced as a real bucket
lifecycle rule (`aws s3api put-bucket-lifecycle-configuration`, re-applied every deploy) rather than a manual prune,
so it holds even if backups stop running; filesystem retention prunes `*.dump.gz` files older than
`DB_BACKUP_RETENTION_DAYS` on every run instead. `DB_BACKUP_S3_ACCESS_KEY_ID`/`DB_BACKUP_S3_SECRET_ACCESS_KEY` are
required once S3 storage is selected — the deploy fails fast with a clear message if either is missing.

## Deployment flow

The normal GitHub Actions path is:

1. Check out automation from the default branch.
2. Check out or otherwise expose the selected release's `docker/compose.prod.yml`
   and `docker/Caddyfile` under `release_dir`.
3. Export the GitHub environment settings above.
4. Run `ansible-playbook ansible/site.yml`.
5. `prepare_deploy` validates inputs, resolves the GHCR digest, verifies the image's Sigstore signature, and creates the
   in-memory inventory with strict host-key checking — see "Image signing" below.
6. `deploy.yml` runs `application_deploy` with `become: true` on the VM.
7. The role acquires `/var/lock/cs2predictor-ansible`, creates a configuration backup, authenticates to GHCR using an
   isolated temporary Docker config, installs the selected Compose/Caddy files and runs `docker compose up` with
   `--wait`.
8. Controller SSH credentials created under `RUNNER_TEMP` are removed by the final play in `site.yml`.

The target never checks out the repository and never needs a GitHub SSH deploy key.

Registering the Telegram webhook is a separate, manual step (`.github/workflows/webhook.yml`, `ansible/webhook.yml`) —
see "Entry points and roles" above. Run it once after the release that first needs it; every later `deploy.yml` run
re-installs the same URL and does not need it run again.

If `deploy.yml`'s "Prepare, back up and deploy with Ansible" step fails for any reason, a following `if: failure()` step
runs `ansible/notify_failure.yml`, which DMs every ID in the `DEPLOY_NOTIFY_CHAT_IDS` GitHub environment variable.
Nothing is sent on a successful deploy, and nothing is sent at all if that variable is unset — see "GitHub production
environment" above.

## Image signing

`release.yml` signs the image it just pushed, keyless: `cosign sign` exchanges the job's own GitHub Actions OIDC token
for a short-lived Sigstore/Fulcio certificate and signs with that — no signing key is generated, stored, or exists
anywhere as a secret that could leak.

`prepare_deploy` verifies that signature before ever telling the target to pull the image, checking both the signing
identity and the issuer:

```
--certificate-identity   https://github.com/<owner>/<repo>/.github/workflows/release.yml@refs/heads/main
--certificate-oidc-issuer https://token.actions.githubusercontent.com
```

An exact match, not a pattern: a signature from any other workflow, any other repository, or a manually pushed tag under
the same name is rejected. This closes the gap that digest-pinning alone leaves open — a digest proves the bytes haven't
changed since it was resolved, not that they came from this project's own release process rather than from anyone else
with push access to the same GHCR package. Verification needs `cosign` on the GitHub Actions runner executing
`deploy.yml` (installed via `sigstore/cosign-installer` in that workflow) and outbound access to Sigstore's public
transparency log — nothing is installed on the target VM for this.

### Manual deployment

A direct `deploy.yml` run uses a normal inventory and explicit immutable release variables:

```sh
python3 -m venv .venv-ansible
.venv-ansible/bin/pip install -r ansible/requirements.txt

export REGISTRY_USER=github-user
# Export REGISTRY_TOKEN securely; it needs read access to the GHCR package.
.venv-ansible/bin/ansible-playbook \
  -i /path/to/inventory.yml \
  ansible/deploy.yml \
  -e @/path/to/release-vars.yml
```

Example `release-vars.yml`:

```yaml
release_tag: v1.0.1
release_dir: /absolute/path/to/checked-out-release
app_image: ghcr.io/allowyoba/cs2-predictor@sha256:REPLACE_WITH_VERIFIED_DIGEST
```

`release_dir` is local to the Ansible controller and must contain
`docker/compose.prod.yml` and `docker/Caddyfile` from the requested release.

## Production Compose model

The production project name is fixed to `cs2predictor`. PostgreSQL and the bot are not published to the host; only Caddy
exposes ports 80 and 443. The database is therefore reachable only inside the Compose network. Caddy proxies only the
public application paths declared in `docker/Caddyfile`.

Compose is invoked with:

- project directory `<APP_PATH>/docker`;
- `--env-file <APP_PATH>/.env`;
- compose file `<APP_PATH>/docker/compose.prod.yml`;
- `APP_IMAGE` in the process environment.

This makes `./Caddyfile` resolve to the deployed configuration directory while keeping application secrets outside that
directory.

## Backups and failures

Before replacing application configuration, deployment acquires the host lock and creates a unique directory under
`<APP_PATH>/backups` (`0700`, owned by `APP_USER` — kept inside `APP_PATH` rather than the role's own
`/var/backups/cs2predictor` default, which is root-owned and outside what `APP_USER`'s scoped sudo can manage; see
"Least-privilege deploy"). The backup contains existing `.env`, deployed Docker configuration, release
metadata and any legacy root-level Compose files that still exist. The archive is mode
`0600`, is verified with `tar`, and remains on the VM.

This is a **configuration backup only**. It does not contain PostgreSQL data, Docker volumes or Caddy certificates, and
it does not roll back database migrations.

If `docker compose up --wait` fails its health check, `application_deploy`
automatically re-pulls and brings back up whichever image `deployment.json`
named before this attempt started, then restores that file — so a bad release does not leave the site down while someone
notices. The deploy itself still fails (`deploy.yml`'s job goes red, and `notify_failure.yml` DMs the configured
admins), the message says exactly which of three things happened:
rolled back successfully, rollback also failed (the service may genuinely be down — investigate immediately), or there
was no previous release to roll back to (a first install). Only a genuinely broken host — Compose itself failing
regardless of which image it's given — reaches the second case.

Database migrations are never rolled back either way: they only ever move forward, by design (see the root README). A
release whose migrations are incompatible with the previous image can still leave the database in a state the old image
doesn't understand, even after a successful rollback of the containers themselves — check for that before relying on the
rollback alone.

An interrupted controller can leave `/var/lock/cs2predictor-ansible`. Remove it only after confirming no deployment or
Compose process is still active.

## Inventory example

`ansible/inventory.example.yml` documents a direct inventory. For the GitHub Actions `site.yml` path, `prepare_deploy`
creates this inventory dynamically, so no production host file needs to be committed.

## Validation

```sh
python3 -m venv .venv-ansible
.venv-ansible/bin/pip install -r ansible/requirements.txt

.venv-ansible/bin/ansible-playbook -i ansible/inventory.example.yml ansible/site.yml --syntax-check
.venv-ansible/bin/ansible-playbook -i ansible/inventory.example.yml ansible/bootstrap.yml --syntax-check
.venv-ansible/bin/ansible-playbook -i ansible/inventory.example.yml ansible/deploy.yml --syntax-check
.venv-ansible/bin/ansible-playbook -i ansible/inventory.example.yml ansible/webhook.yml --syntax-check
.venv-ansible/bin/ansible-playbook -i ansible/inventory.example.yml ansible/notify_failure.yml --syntax-check
shellcheck scripts/configure-repository.sh

docker build -t cs2predictor-ansible-tests -f ansible/tests/Dockerfile ansible
docker run --rm \
  -v "$PWD/ansible:/src/ansible:ro" \
  -v "$PWD/docker:/src/docker:ro" \
  -v "$PWD/scripts:/src/scripts:ro" \
  cs2predictor-ansible-tests
```

The Ansible unit/integration-style tests cover controller input validation, dynamic inventory, configuration backup,
deployment locking, successful release installation and failure handling, webhook registration and admin notification —
`docker` and `curl` are stubbed for those. Migrating host access (`host_access`, via `bootstrap.yml`) is tested
separately, against a real `sshd` and real `sudo` inside the test container: it reproduces a host still on the old
`ForceCommand`-restricted access model, runs the migration, and then runs a full `site.yml` deploy over the resulting
SSH connection. Nothing in any of this connects to a production server.
