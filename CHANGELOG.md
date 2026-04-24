# Changelog

Notable changes to Human Relay. Format loosely follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Version tags are not maintained yet; sections below group by merge date. See `git log` for the full commit-level history.

## Unreleased

### Added
- `create_then_run` MCP tool combines `create_script` + `run_script` into a single approval. Default target is `/opt/human-relay/scripts/oneshot/<name>.{sh,py,json}` (extension auto-detected from content, same rules as `create_script`). A slash in `<name>` is respected as-is for deliberate non-oneshot subdirs. Refuses on collision (cross-extension, so `create_then_run("foo", <shell>)` won't shadow an existing `foo.py`). Script persists on disk after the run — re-runnable via `run_script(name="oneshot/<name>")`.
- `run_script` accepts subpath names (e.g. `oneshot/foo`). New `validScriptPathRe` regex allows alphanumeric segments joined by single slashes; rejects traversal, leading/trailing/double slashes, and `.`/`..` segments.
- `write_file` pre-approval overwrite warning. Before queuing the approval, the relay probes the target via SSH with `stat -c '%s %Y' <path>` (same routing as the write — direct SSH or pct exec). If the file exists, the approval reason gains an `[OVERWRITE: <size>B, modified <YYYY-MM-DD HH:MM>]` line so the reviewer sees it before deciding. Fail-open: any probe error (SSH failure, permission denied, timeout) proceeds with the write and logs a `write_file_probe_failed` audit event. 3-second probe timeout.
- `.github/` scaffolding — issue templates (bug / feature), PR template, CODEOWNERS.
- `CHANGELOG.md` (this file).

### Changed
- Module path is now `github.com/standardnguyen/human-relay` (was an internal path). Unblocks `go install`, Go Report Card, and pkg.go.dev.
- README rewritten for discoverability: approval-gate framing, explicit MCP client list (Claude Code, Cursor, Windsurf, Continue, Cline, Zed, Goose), Alternatives section, badge row.
- Security section moved to `SECURITY.md`; README now links to it.
- `run_script` validator no longer rejects slashes outright — they now denote subpaths under the scripts directory.

## 2026-04-17

### Added
- `withdraw_request` MCP tool lets an agent retract a pending request it no longer wants executed. Withdrawn requests stay visible in the dashboard with a reason and a Mark Read button.
- `write_file` accepts plaintext `content` alongside the existing `content_base64`. Use plaintext for text files; base64 is only needed for binaries or byte sequences with embedded nulls.

## 2026-04-16

### Added
- `run_script` accepts a positional `args` array for shell and Python scripts.

## 2026-04-07

### Added
- Shell (`.sh`) script support in `create_script` / `run_script`.

### Changed
- License updated to Petty Software License v2.1.2.

## 2026-04-01

### Added
- `create_script` / `run_script` tools with JSON pipeline engine and Python script support.

### Fixed
- Whitelist button works for script-type requests.

## 2026-03-13

### Changed
- Hard rejection of `bash -c` argv-splitting patterns that caused crontab wipe incidents.
