# Load .env (if present) so every target sees the same settings as the app.
# Note: make treats '$' and '#' in .env values specially; quote or avoid them there.
-include .env
export

GO             ?= go
MIGRATIONS_DIR := migrations
N              ?= 1

# Stay on the installed Go instead of silently downloading a newer toolchain.
export GOTOOLCHAIN := local

.PHONY: run build test test-race migrate-up migrate-down migrate-new migrate-version sqlc lint tidy tools

run: ## Start the API (and workers) using .env
	$(GO) run ./cmd/api

build: ## Build bin/api
	$(GO) build -o bin/api ./cmd/api

test: ## Unit tests; integration tests run too when TEST_DATABASE_URL / TEST_REDIS_URL are set
	$(GO) test ./... -count=1

test-race: ## Tests with the race detector (needs a 64-bit C compiler for cgo)
	$(GO) test ./... -count=1 -race

migrate-up: ## Apply all pending migrations to the primary
	migrate -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL_PRIMARY)" up

migrate-down: ## Roll back N migrations (default 1): make migrate-down N=2
	migrate -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL_PRIMARY)" down $(N)

migrate-version: ## Show the current migration version
	migrate -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL_PRIMARY)" version

migrate-new: ## Create a new migration pair: make migrate-new NAME=add_foo
	migrate create -ext sql -dir $(MIGRATIONS_DIR) -seq $(NAME)

sqlc: ## Regenerate type-safe query code
	sqlc generate

lint: ## Run golangci-lint
	golangci-lint run ./...

tidy: ## Tidy go.mod / go.sum
	$(GO) mod tidy

# Versions pinned to ones that build with Go 1.25. sqlc needs cgo when built from
# source; on Windows download the release binary instead (see README).
tools: ## Install migrate, sqlc and golangci-lint into GOPATH/bin
	$(GO) install -tags postgres github.com/golang-migrate/migrate/v4/cmd/migrate@v4.19.1
	$(GO) install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
