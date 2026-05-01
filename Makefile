.PHONY: help build run seed test test-race test-cover test-e2e vet fmt fmt-check lint qa qa-docker tidy clean demo demo-curls verify acceptance docker-build docker-up docker-down docker-reset

GO            ?= go
SERVER_PKG    := ./cmd/server
SEED_PKG      := ./test/seed
HEALTH_PKG    := ./cmd/healthcheck
BIN_DIR       := bin
SERVER_BIN    := $(BIN_DIR)/server
SEED_BIN      := $(BIN_DIR)/seed
HEALTH_BIN    := $(BIN_DIR)/healthcheck
DB_FILE       ?= yuno.db
TESTDATA_FILE ?= testdata/transactions.json
EXPECTED_FILE ?= testdata/expected_counts.json
SEED_TOTAL    ?= 500
SEED_VALUE    ?= 42
PORT          ?= 8080

help:
	@echo "Targets:"
	@echo "  build           - build server, seed and healthcheck binaries into ./bin"
	@echo "  run             - run the server (PORT=$(PORT))"
	@echo "  seed            - generate testdata/transactions.json + expected_counts.json"
	@echo ""
	@echo "  test            - go test ./... -race -count=1"
	@echo "  test-cover      - test with coverprofile + cover -func summary"
	@echo "  test-e2e        - go test ./e2e -tags=e2e -race"
	@echo "  test-race       - alias of test"
	@echo "  vet             - go vet ./..."
	@echo "  fmt             - gofmt -w ."
	@echo "  fmt-check       - fail if gofmt -l finds issues"
	@echo "  lint            - go vet ./... + gofmt-check"
	@echo "  qa              - fmt-check + vet + test + test-e2e (full local QA gate)"
	@echo ""
	@echo "  docker-build    - docker compose build"
	@echo "  docker-up       - docker compose up -d"
	@echo "  docker-down     - docker compose down"
	@echo "  docker-reset    - docker compose down -v (drops the volume / sentinel)"
	@echo "  demo            - docker compose up --build (foreground demo)"
	@echo "  demo-curls      - run docs/examples/curl.sh against the running service"
	@echo "  verify          - run docs/examples/verify.sh against the running service"
	@echo "  qa-docker       - run the full test suite inside Docker (no local Go required)"
	@echo "  acceptance      - reset + up + wait-healthy + verify (end-to-end demo gate)"
	@echo ""
	@echo "  tidy            - go mod tidy"
	@echo "  clean           - remove ./bin and the local SQLite files"

$(BIN_DIR):
	@mkdir -p $(BIN_DIR)

build: | $(BIN_DIR)
	$(GO) build -o $(SERVER_BIN) $(SERVER_PKG)
	$(GO) build -o $(SEED_BIN) $(SEED_PKG)
	$(GO) build -o $(HEALTH_BIN) $(HEALTH_PKG)

run:
	PORT=$(PORT) SQLITE_DSN="file:$(DB_FILE)?_pragma=journal_mode(WAL)" $(GO) run $(SERVER_PKG)

seed: | $(BIN_DIR)
	$(GO) run $(SEED_PKG) --seed $(SEED_VALUE) --total $(SEED_TOTAL) --out $(TESTDATA_FILE) --expected $(EXPECTED_FILE)

# --- testing -----------------------------------------------------------

test:
	$(GO) test ./... -race -count=1

test-race: test

# test-cover excludes cmd/server and cmd/healthcheck because they have no
# *_test.go files, which on some Go 1.25 toolchains triggers the missing
# `covdata` tool (no-test packages get an empty coverage profile by default).
test-cover:
	$(GO) test $$($(GO) list ./... | grep -vE 'cmd/server|cmd/healthcheck') -race -coverprofile=coverage.out -count=1
	$(GO) tool cover -func=coverage.out | tail -n 30

test-e2e:
	$(GO) test ./e2e -tags=e2e -race -count=1

vet:
	$(GO) vet ./...

fmt:
	gofmt -w .

fmt-check:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt issues:"; echo "$$out"; exit 1; fi

lint: vet fmt-check

qa: fmt-check vet test test-e2e

# --- docker / demo -----------------------------------------------------

docker-build:
	docker compose build

docker-up:
	docker compose up -d

docker-down:
	docker compose down

docker-reset:
	docker compose down -v

demo:
	docker compose up --build

demo-curls:
	bash docs/examples/curl.sh

verify:
	bash docs/examples/verify.sh

qa-docker:
	docker build -f Dockerfile.test --target test -t yuno-qa:latest .

acceptance:
	$(MAKE) docker-reset
	docker compose up -d --build
	bash scripts/wait-healthy.sh
	$(MAKE) verify

# --- housekeeping ------------------------------------------------------

tidy:
	$(GO) mod tidy

clean:
	rm -rf $(BIN_DIR)
	rm -f $(DB_FILE) $(DB_FILE)-journal $(DB_FILE)-shm $(DB_FILE)-wal coverage.out coverage.html
