# Runbook 01: Unit + Integration Tests

**Type:** Non-control (standard CI)
**Trigger:** Every push, every PR
**Duration:** ~2 minutes

## Purpose

Run the existing Go test suite — unit tests for MCP tool handling, store operations, container registry, and the integration tests for audit log flows. This is the baseline gate; nothing else runs if this fails.

## Steps

### 1. Build

```bash
go build -o human-relay .
```

### 2. Unit Tests

```bash
go test ./mcp/... -v -race -count=1
```

Covers:
- `tools_test.go` — tool handler dispatch, argument parsing, request creation for all 7 tools
- Store CRUD, container registry operations (tested indirectly)

### 3. Integration Tests

```bash
HUMAN_RELAY_BIN=$(pwd)/human-relay go test -v -count=1 ./integration/...
```

These start a real binary, hit it over HTTP, and verify end-to-end flows:
- `audit_test.go` — `TestAuditLogApprovalFlow` (4 events: created/approved/started/completed), `TestAuditLogDenialFlow` (2 events: created/denied)
- `integration_test.go` — full request lifecycle over MCP JSON-RPC + web API
- `cooldown_test.go` — approval cooldown enforcement
- `container_test.go` — container registration and exec routing
- `write_file_test.go` — file write via base64 content piping

### 4. Race Detector

The `-race` flag on unit tests catches concurrency bugs in:
- `audit.Logger` (mutex-protected JSONL writes)
- `store.Store` (concurrent request access)
- SSE event broadcasting

## Pass Criteria

- All tests pass
- No race conditions detected
- Binary compiles without errors

## Failure Actions

- Test failure: read output, fix code, re-run
- Race detector fires: the mutex-protected code in `audit/` or `store/` has a concurrency bug — fix before anything else
- Build failure: check Go version (requires 1.24+), run `go mod download`
