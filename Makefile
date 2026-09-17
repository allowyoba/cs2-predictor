MODULE      := cs2predictor
BIN_DIR     := bin
BIN         := $(BIN_DIR)/cs2predictor
DOCKER_TAG  := cs2predictor:local
GOLANGCI    := github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2

# The Compose file lives in docker/, but the build context and the .env
# it reads are the repository root — hence --project-directory.
COMPOSE     := docker compose --project-directory . --file docker/compose.yml

VERSION     := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME  := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildTime=$(BUILD_TIME)

# .env is intentionally NOT loaded/exported here: it holds real secrets, and
# unconditionally exporting it into every target's environment (including
# test) leaks those secrets into subprocesses that should run hermetically —
# e.g. tests asserting "missing token is rejected" would silently observe
# the real token instead. `run` and `db-status` load it explicitly, scoped
# to just that command, via `env $$(grep -v '^#' .env | xargs)`.

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

## --- Build & run ---

.PHONY: build
build: ## Build the cs2predictor binary into bin/ (stamped with version/commit/build time)
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN) ./cmd/bot

.PHONY: run
run: ## Run the bot locally, loading .env if present (requires a reachable Postgres)
	@if [ -f .env ]; then env $$(grep -v '^\s*#' .env | grep -v '^\s*$$' | xargs) go run ./cmd/bot; \
	else go run ./cmd/bot; fi

.PHONY: clean
clean: ## Remove build artifacts and coverage output
	rm -rf $(BIN_DIR) coverage.out coverage.html

## --- Quality ---

.PHONY: fmt
fmt: ## Reformat all Go source (gofmt -w)
	gofmt -w .

.PHONY: fmt-check
fmt-check: ## Fail if any file isn't gofmt-formatted
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needed on:"; echo "$$unformatted"; exit 1; \
	fi

.PHONY: vet
vet: ## go vet ./...
	go vet ./...

.PHONY: lint
lint: ## Run golangci-lint (downloaded on demand, no global install needed)
	go run $(GOLANGCI) run ./...

.PHONY: tidy
tidy: ## go mod tidy, then fail if go.mod/go.sum drifted
	go mod tidy
	git diff --exit-code go.mod go.sum

.PHONY: check
# vet isn't listed here: golangci-lint's govet linter (enabled in
# .golangci.yml) already runs the same passes, so a separate `go vet` step
# would just duplicate that work. `make vet` stays as its own target for a
# fast standalone check.
check: fmt-check lint test ## Everything CI runs, minus the integration test and docker build

## --- Tests ---

.PHONY: test
test: ## Run unit tests
	go test ./...

.PHONY: test-integration
test-integration: ## Run integration tests (spins up real Postgres via testcontainers-go; needs Docker)
	go test -tags integration ./...

.PHONY: test-all
test-all: test test-integration ## Run unit + integration tests

.PHONY: cover
cover: ## Run unit tests with coverage and open an HTML report
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "coverage report: coverage.html"

## --- Docker / Compose ---

.PHONY: docker-build
docker-build: ## Build the production image (linux/amd64 — the only architecture this project ships)
	docker build --file docker/Dockerfile \
		--build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILD_TIME=$(BUILD_TIME) \
		-t $(DOCKER_TAG) .

.PHONY: up
up: ## Start the development stack (postgres + bot) in the background
	$(COMPOSE) up -d --build

.PHONY: down
down: ## Stop the development stack and remove its volumes
	$(COMPOSE) down -v

.PHONY: logs
logs: ## Follow the bot's logs
	$(COMPOSE) logs -f bot

.PHONY: ps
ps: ## Show development stack container status
	$(COMPOSE) ps

## --- Deployment ---

ANSIBLE_PLAYBOOKS := site.yml bootstrap.yml deploy.yml webhook.yml notify_failure.yml scheduled_backup.yml

.PHONY: deploy-check
deploy-check: ## Syntax-check the Ansible playbooks and shellcheck scripts/ — what CI runs (needs: pip install ansible-core)
	cd ansible && ansible-playbook -i inventory.example.yml $(ANSIBLE_PLAYBOOKS) --syntax-check
	shellcheck scripts/configure-repository.sh

.PHONY: ansible-lint
ansible-lint: ## Lint the Ansible roles (not run in CI — see ansible/README.md for known pre-existing findings)
	cd ansible && ansible-lint .

.PHONY: ansible-check
ansible-check: deploy-check ansible-lint ## Full local Ansible validation: syntax-check, shellcheck and lint

.PHONY: ansible-test
ansible-test: ## Run the Ansible role test suite in Docker (backup, rollback, migration, webhook, notify — see ansible/tests)
	docker build -t cs2predictor-ansible-tests -f ansible/tests/Dockerfile ansible
	docker run --rm \
		-v "$(CURDIR)/ansible:/src/ansible:ro" \
		-v "$(CURDIR)/docker:/src/docker:ro" \
		-v "$(CURDIR)/scripts:/src/scripts:ro" \
		cs2predictor-ansible-tests

.PHONY: node-check
node-check: ## Install release tooling and confirm release.config.mjs loads
	npm ci --ignore-scripts
	node -e "import('./release.config.mjs').then(m => { if (!m.default) throw new Error('no default export') })"

.PHONY: lint-actions
lint-actions: ## Validate GitHub Actions workflow syntax (shellcheck included)
	go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12

.PHONY: check-pr-title
check-pr-title: ## Validate a pull request title against Conventional Commits (usage: PR_TITLE="feat: ..." make check-pr-title)
	@# Read from the environment, not a Make command-line variable: Make
	@# substitutes the latter directly into this recipe's text, and a
	@# pull request title is attacker-controlled — bash's own quoted
	@# expansion of $$PR_TITLE is what actually keeps this safe.
	@if [ -z "$$PR_TITLE" ]; then echo 'usage: PR_TITLE="feat: ..." make check-pr-title' >&2; exit 64; fi
	@if ! printf '%s' "$$PR_TITLE" | grep -qE '^(feat|fix|perf|docs|style|refactor|test|build|ci|chore|revert)(\([^()]+\))?!?: .+'; then \
		echo 'Pull request title must use Conventional Commits, e.g. "feat: add deployment"' >&2; exit 1; \
	fi

.PHONY: check-tag
check-tag: ## Validate a release tag is vMAJOR.MINOR.PATCH (usage: TAG=v1.2.3 make check-tag)
	@# Read from the environment — see check-pr-title for why.
	@if ! printf '%s' "$$TAG" | grep -qE '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$$'; then \
		echo "tag must be vMAJOR.MINOR.PATCH, e.g. v1.2.3" >&2; exit 1; \
	fi

.PHONY: deploy
deploy: ## Install a release (used by .github/workflows/deploy.yml; needs the same env vars ansible/site.yml reads)
	ansible-playbook ansible/site.yml

.PHONY: notify-deploy-failure
notify-deploy-failure: ## DM the configured admins about a failed deploy (used by .github/workflows/deploy.yml)
	ansible-playbook ansible/notify_failure.yml

.PHONY: register-webhook
register-webhook: ## Re-register the Telegram webhook (used by .github/workflows/webhook.yml)
	ansible-playbook ansible/webhook.yml

## --- Database ---

.PHONY: db-status
db-status: ## Show applied/pending migrations against .env's DATABASE_URL (needs goose installed: go install github.com/pressly/goose/v3/cmd/goose@latest)
	@env $$(grep -v '^\s*#' .env | grep -v '^\s*$$' | xargs) sh -c \
		'goose -dir internal/adapter/postgres/migrations postgres "$$DATABASE_URL" status'
