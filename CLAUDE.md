# CLAUDE.md

## Testing

**All tests go in the separate test repo: `~/human-relay-tests/`** (git.ekaterina.net/administrator/human-relay-tests). Do NOT write tests in this repo. The test repo contains integration tests that start the compiled binary and hit it over HTTP. See `helpers.go` in that repo for the test harness (`StartServer`, `WebGet`, `WebPost`, `MCPClient`).
