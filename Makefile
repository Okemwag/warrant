SHELL := /bin/bash
-include .env
export

GO ?= go

.PHONY: help db db-down dev serve migrate keygen test test-engine test-race test-sandbox lint gates gate1 gate2 gate3 console console-dev mcp build

help:
	@grep -E '^[a-z0-9-]+:.*?## ' $(MAKEFILE_LIST) | awk -F':.*?## ' '{printf "  %-14s %s\n", $$1, $$2}'

db: ## Start Postgres (port 5433)
	docker compose up -d --wait postgres

db-down: ## Stop Postgres
	docker compose down

dev: db ## Start Postgres and run the API (serves console/dist if built)
	$(GO) run ./server/cmd/warrant serve -migrate

console: ## Build the Console into console/dist
	cd console && npm ci --no-audit --no-fund && npm run build

console-dev: ## Run the Console dev server on :5173 (proxies the API on :8080)
	cd console && npm run dev

mcp: ## Build the agent MCP server to bin/warrant-mcp
	$(GO) build -o bin/warrant-mcp ./bridge/cmd/warrant-mcp

build: console mcp ## Build everything into bin/ and console/dist
	$(GO) build -o bin/warrant ./server/cmd/warrant

serve: ## Run the API (expects Postgres)
	$(GO) run ./server/cmd/warrant serve

migrate: ## Apply database migrations
	$(GO) run ./server/cmd/warrant migrate

keygen: ## Print a new WARRANT_SIGNING_KEY
	$(GO) run ./server/cmd/warrant keygen

test: ## Go tests (DB tests run if Postgres is up) + Console tests
	$(GO) test -race ./...
	cd console && npx vitest run

test-engine: ## Engine tests only
	$(GO) test -race ./engine/...

test-race: db ## Ledger concurrency tests against real Postgres
	$(GO) test -race -count=1 -run 'Concurrent|Idempot|Release|Pause' -v ./server/ledger/...

test-sandbox: ## Tests that call the PayPal sandbox
	$(GO) test -tags sandbox -count=1 -v ./bridge/paypal/...

lint: ## gofmt, go vet, golangci-lint
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)
	$(GO) vet ./...
	@if command -v golangci-lint >/dev/null; then golangci-lint run ./...; else echo "golangci-lint not installed, skipping"; fi
	cd console && npx tsc --noEmit

gates: gate2 gate3 gate1 ## Run week-one gates

gate1: ## Gate 1: PayPal payment path (interactive, needs sandbox keys)
	$(GO) run ./scripts/gates/paypal -mode $${PAYPAL_MODE:-approve}

gate2: ## Gate 2: untrusted text cannot bypass policy
	@$(GO) test -count=1 -run 'Untrusted|Injection' ./engine/... >/dev/null && echo "GATE 2 PASS" || (echo "GATE 2 FAIL"; exit 1)

gate3: db ## Gate 3: concurrent requests cannot exceed a shared limit
	@$(GO) test -race -count=1 -run 'Concurrent' ./server/ledger/... >/dev/null && echo "GATE 3 PASS" || (echo "GATE 3 FAIL"; exit 1)
