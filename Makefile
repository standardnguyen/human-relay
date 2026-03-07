BIN := ./human-relay

.PHONY: build test test-unit test-integration test-e2e test-e2e-containers test-e2e-frontend test-e2e-frontend-update test-all clean

build:
	go build -o $(BIN) .

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
