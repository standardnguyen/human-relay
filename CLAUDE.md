# CLAUDE.md

## Testing

**Integration tests live in `integration/`** in this repo. They start the compiled binary and hit it over HTTP. See `integration/helpers.go` for the test harness (`StartServer`, `WebGet`, `WebPost`, `MCPClient`).

```bash
go build -o human-relay .
HUMAN_RELAY_BIN=$(pwd)/human-relay go test -v -count=1 ./integration/...
```

Unit tests for MCP tool definitions live in `mcp/tools_test.go` and run with `go test ./mcp/...`.
