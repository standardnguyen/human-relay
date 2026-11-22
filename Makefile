BIN := ./human-relay

.PHONY: build build-tui test-tui test test-unit test-integration test-e2e test-e2e-containers test-e2e-frontend test-e2e-frontend-update test-all clean

build:
	go build -o $(BIN) .

# The terminal UI is its own Go module under tui/ (it is not part of the relay's image).
build-tui:
	cd tui && CGO_ENABLED=0 go build -o bin/human-relay-tui .

test-tui: build
	cd tui && go vet ./... && HUMAN_RELAY_BIN=$(abspath $(BIN)) go test -count=1 ./...

test-unit:
	go test -v -count=1 ./whitelist/... ./mcp/...

test-integration: build
	HUMAN_RELAY_BIN=$(abspath $(BIN)) go test -v -count=1 ./integration/...

test-e2e-containers: build
	cd e2e/containers && HUMAN_RELAY_BIN=$(abspath $(BIN)) go test -v -count=1 -timeout 120s ./...

test-e2e-frontend: build
	cd e2e/frontend && HUMAN_RELAY_BIN=$(abspath $(BIN)) WEB_PORT=38090 npx playwright test

test-e2e-frontend-update: build
	cd e2e/frontend && HUMAN_RELAY_BIN=$(abspath $(BIN)) WEB_PORT=38090 npx playwright test --update-snapshots

test-e2e: test-e2e-containers test-e2e-frontend

test: test-unit test-integration

test-all: test test-e2e

clean:
	rm -f $(BIN)
	cd e2e/containers && docker-compose down -v --remove-orphans 2>/dev/null || true
