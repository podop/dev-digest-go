# dev-digest-go — common tasks. `make` or `make help` lists them.
# Volumes are never removed here: there is deliberately no `down -v` target (see AGENTS.md).

SHELL := /bin/bash
.DEFAULT_GOAL := help

COMPOSE ?= docker compose
# The Go toolchain: unset an IDE-inherited GOROOT (Windows SDK) and prefer ~/.local/go
# over the older /usr/bin/go (backend/INSIGHTS.md). Override with `make GO=go …`.
GO_LOCAL_BIN := $(wildcard $(HOME)/.local/go/bin)
GO ?= env -u GOROOT PATH="$(if $(GO_LOCAL_BIN),$(GO_LOCAL_BIN):)$(PATH)" go
PNPM ?= pnpm
SERVICE ?=

BACKEND := backend
FRONTEND := frontend

# The source commit the backend image stamps into the CI runner it embeds (12 hex of HEAD, only when
# the working tree is clean, else "dev"). Compose passes it as the DEVDIGEST_BUILD_ID build arg;
# a plain `docker compose build` without make builds "dev". Override: make DEVDIGEST_BUILD_ID=… up
ifndef DEVDIGEST_BUILD_ID
DEVDIGEST_BUILD_ID := $(shell [ -z "$$(git status --porcelain 2>/dev/null)" ] && git rev-parse --short=12 HEAD 2>/dev/null || echo dev)
endif
export DEVDIGEST_BUILD_ID

.PHONY: help
help: ## List the targets
	@awk 'BEGIN {FS = ":.*## "} /^##@/ {printf "\n\033[1m%s\033[0m\n", substr($$0, 5)} \
	  /^[a-zA-Z0-9_-]+:.*## / {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

##@ Stack (docker compose)

.PHONY: up
up: ## Build and start db, backend, frontend (SERVICE=… for one)
	$(COMPOSE) up -d --build $(SERVICE)

.PHONY: down
down: ## Stop and remove the containers (volumes are kept)
	$(COMPOSE) down

.PHONY: build
build: ## Build the images (SERVICE=… for one)
	$(COMPOSE) build $(SERVICE)

.PHONY: restart
restart: ## Restart containers without rebuilding (SERVICE=… for one)
	$(COMPOSE) restart $(SERVICE)

.PHONY: ps
ps: ## Show container status and health
	$(COMPOSE) ps

.PHONY: logs
logs: ## Follow logs (SERVICE=backend to filter)
	$(COMPOSE) logs -f $(SERVICE)

.PHONY: config
config: ## Validate docker-compose.yml (and .env)
	$(COMPOSE) config -q && echo "compose config OK"

.PHONY: health
health: ## Probe the running API and UI
	@set -a; [ -f .env ] && . ./.env; set +a; \
	api=$${API_PORT:-3101}; web=$${WEB_PORT:-3100}; \
	printf 'GET /health        '; curl -fsS "http://localhost:$$api/health"; echo; \
	printf 'GET /health/ready  '; curl -fsS "http://localhost:$$api/health/ready"; echo; \
	printf 'UI  /              '; curl -fsS -o /dev/null -w '%{http_code}\n' "http://localhost:$$web/"

.PHONY: db-shell
db-shell: ## Open psql in the db container
	$(COMPOSE) exec db psql -U devdigest -d devdigest

##@ Database (backend CLI inside the stack; Phase 1)

.PHONY: migrate-up
migrate-up: ## Apply pending migrations
	$(COMPOSE) run --rm backend migrate up

.PHONY: migrate-down
migrate-down: ## Roll back the latest migration version
	$(COMPOSE) run --rm backend migrate down

.PHONY: migrate-status
migrate-status: ## Show applied / pending migrations
	$(COMPOSE) run --rm backend migrate status

.PHONY: seed
seed: ## Run the idempotent seed
	$(COMPOSE) run --rm backend seed

##@ Backend (Go)

.PHONY: backend-build
backend-build: ## go build ./...
	cd $(BACKEND) && $(GO) build ./...

.PHONY: backend-vet
backend-vet: ## go vet ./...
	cd $(BACKEND) && $(GO) vet ./...

.PHONY: backend-fmt
backend-fmt: ## List files gofmt would change (fails if any)
	@cd $(BACKEND) && out=$$("$$($(GO) env GOROOT)/bin/gofmt" -l .) || exit 1; \
	if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; else echo "gofmt OK"; fi

.PHONY: backend-test
backend-test: ## Full test gate: race, shuffle, no cache (Docker for integration tests)
	cd $(BACKEND) && $(GO) test -race -shuffle=on -count=1 ./...

.PHONY: backend-test-short
backend-test-short: ## Fast loop without Docker (integration tests skipped)
	cd $(BACKEND) && $(GO) test -short ./...

.PHONY: backend-cover
backend-cover: ## Coverage summary (writes backend/cover.out)
	cd $(BACKEND) && $(GO) test -short -coverprofile=cover.out ./... && $(GO) tool cover -func=cover.out | tail -1

.PHONY: backend-check
backend-check: backend-fmt backend-build backend-vet backend-test ## All backend gates

##@ Frontend (Next.js; needs Node 22 + pnpm on the host)

.PHONY: frontend-install
frontend-install: ## pnpm install --frozen-lockfile
	cd $(FRONTEND) && $(PNPM) install --frozen-lockfile

.PHONY: frontend-test
frontend-test: ## vitest (jsdom)
	cd $(FRONTEND) && $(PNPM) test

.PHONY: frontend-typecheck
frontend-typecheck: ## tsc --noEmit
	cd $(FRONTEND) && $(PNPM) typecheck

.PHONY: frontend-lint
frontend-lint: ## biome lint
	cd $(FRONTEND) && $(PNPM) lint

.PHONY: frontend-dev
frontend-dev: ## next dev against the API in the stack (port 3000)
	cd $(FRONTEND) && NEXT_PUBLIC_API_BASE=http://localhost:$${API_PORT:-3101} $(PNPM) dev

.PHONY: frontend-check
frontend-check: frontend-typecheck frontend-lint frontend-test ## All frontend gates

##@ Everything

.PHONY: check
check: backend-check frontend-check ## All gates of both packages
