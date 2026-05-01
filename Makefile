.PHONY: help build run seed test test-race lint fmt tidy clean demo-curls

GO            ?= go
SERVER_PKG    := ./cmd/server
SEED_PKG      := ./cmd/seed
BIN_DIR       := bin
SERVER_BIN    := $(BIN_DIR)/server
SEED_BIN      := $(BIN_DIR)/seed
DB_FILE       ?= yuno.db
TESTDATA_FILE ?= testdata/transactions.json
EXPECTED_FILE ?= testdata/expected_counts.json
SEED_TOTAL    ?= 500
SEED_VALUE    ?= 42
PORT          ?= 8080

help:
	@echo "Targets:"
	@echo "  build        - build server and seed binaries into ./bin"
	@echo "  run          - run the server (PORT=$(PORT))"
	@echo "  seed         - generate testdata/transactions.json and expected_counts.json"
	@echo "  test         - go test ./..."
	@echo "  test-race    - go test -race ./..."
	@echo "  lint         - go vet ./... + gofmt -l"
	@echo "  fmt          - gofmt -w ."
	@echo "  tidy         - go mod tidy"
	@echo "  clean        - remove ./bin and the database"
	@echo "  demo-curls   - run docs/examples/curl.sh against the server"

$(BIN_DIR):
	@mkdir -p $(BIN_DIR)

build: | $(BIN_DIR)
	$(GO) build -o $(SERVER_BIN) $(SERVER_PKG)
	$(GO) build -o $(SEED_BIN) $(SEED_PKG)

run:
	PORT=$(PORT) SQLITE_DSN="file:$(DB_FILE)?_pragma=journal_mode(WAL)" $(GO) run $(SERVER_PKG)

seed: | $(BIN_DIR)
	$(GO) run $(SEED_PKG) --seed $(SEED_VALUE) --total $(SEED_TOTAL) --out $(TESTDATA_FILE) --expected $(EXPECTED_FILE)

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

lint:
	$(GO) vet ./...
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt issues:"; echo "$$out"; exit 1; fi

fmt:
	gofmt -w .

tidy:
	$(GO) mod tidy

clean:
	rm -rf $(BIN_DIR)
	rm -f $(DB_FILE) $(DB_FILE)-journal $(DB_FILE)-shm $(DB_FILE)-wal

demo-curls:
	bash docs/examples/curl.sh
