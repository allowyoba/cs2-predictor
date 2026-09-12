# syntax=docker/dockerfile:1
#
# BuildKit is required rather than optional: the RUN --mount lines below
# are BuildKit syntax and the classic builder cannot parse them at all.
# That is the point — the build fails loudly instead of falling back to a
# mode where the cache and secret mounts would become ordinary layers.
# Docker Desktop and CI runners ship the buildx plugin; a plain Docker
# Engine or Colima install needs it added separately.
#
# Credentials never travel through ARG or ENV. Both are recorded in the
# image's history, so a token passed that way stays readable by anyone
# who can pull the image, forever. When a build genuinely needs one — a
# private Go module, a company proxy — pass it as a build secret:
#
#   docker buildx build --secret id=netrc,src="$HOME/.netrc" \
#     -f docker/Dockerfile -t cs2predictor:local .
#
# The secret is mounted as a tmpfs file into the single RUN that needs
# it and is gone from the filesystem the moment that RUN finishes; it
# never becomes part of any layer. The mount is optional, so with no
# --secret flag the file simply isn't there and the build proceeds
# unchanged — which is the normal case, since every dependency this
# project has is public.

FROM golang:1.27-alpine AS build

WORKDIR /src

# Dependencies resolve in their own layer, so editing Go source doesn't
# re-download the module graph. Both mounts are caches shared between
# builds instead of image content: nothing they hold ends up shipped.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=secret,id=netrc,target=/root/.netrc \
    go mod download

# Named directories rather than `COPY . .`: what reaches the build stage
# is then decided here, in the file itself, and does not depend on an
# ignore file being picked up correctly. A stray .env or key in the
# working tree cannot end up in a layer even if the context filter is
# bypassed. Both directories hold embedded assets as well as Go sources —
# the SQL migrations and the i18n property files.
COPY cmd ./cmd
COPY internal ./internal

# Build metadata, reported by GET /version. Public values are the only
# kind an ARG may ever carry — see the note at the top of this file.
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildTime=${BUILD_TIME}" \
    -o /out/cs2predictor ./cmd/bot

# One-off maintenance binaries (see cmd/backfill-team-order's own doc
# comment) — never run by the container's own ENTRYPOINT, only invoked by
# an operator via `docker exec` against a running container, which already
# has the production DATABASE_URL/DATABASE_USER/DATABASE_PASSWORD in its
# environment. Shipping it in the same image means no separate build or
# file transfer is ever needed to run a maintenance job against production.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -o /out/backfill-team-order ./cmd/backfill-team-order

FROM alpine:3.24

# curl backs the HEALTHCHECK below, ca-certificates the outbound HTTPS
# calls to Telegram and PandaScore, tzdata the IANA zone names chats
# store as their timezone and the binary resolves at runtime.
RUN apk upgrade --no-cache && \
    apk add --no-cache ca-certificates curl tzdata && \
    adduser -D -u 10001 app

WORKDIR /app
COPY --from=build /out/cs2predictor /app/cs2predictor
COPY --from=build /out/backfill-team-order /app/backfill-team-order
USER app
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=5 \
    CMD curl --fail --silent http://localhost:8080/healthz/live || exit 1
ENTRYPOINT ["/app/cs2predictor"]
