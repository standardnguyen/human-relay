# human-relay-tui

A terminal approval screen for Human Relay. It does what the web dashboard does, from a terminal: it lists the relay's requests, shows every field of each one, and lets you approve, deny, release, whitelist and turbocharge from the keyboard.

It is its own Go module (`github.com/standardnguyen/human-relay/tui`), separate from the relay, and it only uses the relay's web API. It needs no change on the relay.

## Build

```sh
# from the repository root
make build-tui            # writes tui/bin/human-relay-tui

# or directly
cd tui && CGO_ENABLED=0 go build -o human-relay-tui .
```

Go 1.24 or newer. The only dependencies are `golang.org/x/term` and `golang.org/x/sys`.

## Run

```sh
human-relay-tui --url http://127.0.0.1:8090
```

| Flag | Meaning |
|---|---|
| `--url` | The relay's web address (default `http://127.0.0.1:8090`). A plain-`http` address that is not loopback prints a one-line warning, because the token then travels unencrypted unless the path is a tunnel such as a private tailnet. |
| `--ascii` | No colour, no text attributes and no non-ASCII glyphs. A missing or `dumb` `TERM` does the same. The screen is still drawn with cursor-addressing and alternate-screen sequences, so the terminal must support them. |
| `--no-bell` | Do not ring the terminal bell for new pending requests. |

`NO_COLOR` turns colour off but keeps attributes such as reverse video, which is how flagged characters are highlighted.

It asks for the approver token at a hidden prompt on the terminal and refuses to start without one. The token is trimmed and checked once by asking the relay to approve a request id that cannot exist (nothing is created or changed):

- `404` means the token is accepted for decisions. That is the approver token, or, on a relay with no approver token configured (legacy mode), any valid token. The header says so.
- `403` means the token is valid but is not the approver token. You are offered to enter another.
- `401` means the relay does not know the token.

`127.0.0.1:8090` being reachable from the relay host's own shell has not been verified; if it is not, point `--url` at an address that is.

## Keys

| Key | Action |
|---|---|
| `j` `k`, arrows, PgUp/PgDn, Home/End | Move in the list; scroll inside a request |
| `Enter`, `Esc` | Open the selected request / go back |
| `]` `[` | In a request: open the next / previous one |
| `0`-`9`, `Tab`, `Shift-Tab`, `[` `]` | Filter: `0` All, `1` pending, `2` running, `3` complete, `4` denied, `5` withdrawn, `6` error, `7` approved, `8` timeout, `9` whitelist |
| `s` | Show all requests instead of the first 20 |
| `a` | Approve the open pending request. From the list, the key opens the request first, so its full command, arguments and display text are on screen; press it again to approve |
| `g` | Approve, with its output held back from the agent until released (opens the request first, like `a`) |
| `A` | Approve and whitelist it (opens the request first, then asks; the rule is added only after the approve succeeded; never offered for registry operations) |
| `d` | Deny; type an optional reason, `Enter` sends, `Esc` cancels |
| `r` | Release gated output |
| `w` | Whitelist a finished request (asks first) |
| `x` | In the whitelist view, remove the selected rule (asks first) |
| `m` | Mark a withdrawn request read (hides it) |
| `C` | Clear history: hide finished requests until the TUI is restarted |
| `t` | Turbocharge on (5 minutes, 10 second cooldown) or off |
| `f` | In a request: the full file, or the full HTTP body |
| `e` | In a request: the whole reason instead of the first 200 characters |
| `b` `p` `H` | In a request: collapse decoded base64, show raw PowerShell base64, show HTTP response headers |
| `B` | Bell on or off |
| `R` | Refresh now |
| `P` | Forget the token and enter another |
| `?` | Help |
| `q`, `Ctrl-C` | Quit |

Approving, denying and releasing act on the request at once; after a decision in the request view the request stays open, showing its new status and output. While the approval cooldown runs, `a`, `g` and `A` say how long is left instead of sending the request; `d` still works. Whitelist changes, and approve-and-whitelist, ask `y/n` first. While the terminal is smaller than 40x8 the screen only says so, and no key acts except `q` and `Ctrl-C`.

## What it shows

- Every field of every request type: the command and each argument on its own line, the shell flag, working directory, client, timeout, stdin hash, HTTP method, URL, headers (credential headers shortened), body, multipart file and fields, script name and arguments, registry operation and arguments.
- Both the `display_command` and the real command and arguments, when they differ.
- The shell banner and the non-ASCII banner on pending requests, a `NON-ASCII` badge on any request, and under it the field and the characters that triggered it, with their Unicode script (for example `U+0430 (Cyrillic)`).
- Decoded base64 and PowerShell `-EncodedCommand` blocks, with the raw base64 one key away.
- The approval cooldown from the relay's headers and from a `429`, the turbocharge countdown, the connection state, and the pending count.

## Terminal safety

Every string that comes from an agent or from command output (commands, arguments, reasons, URLs, headers, bodies, file content, output, whitelist rules, client names) goes through one sanitizer before it is drawn. Escape, CSI, OSC and DCS sequences, every other control character, DEL, the C1 range, invalid bytes, bidi controls, zero-width and other invisible characters are shown as visible text such as `\x1b`, `\x9b`, `<U+202E>`. Non-ASCII characters are drawn highlighted. The only escape sequences the TUI writes are its own colour and cursor sequences.

## Updates and failures

The screen follows the relay's `/events` stream. The lists are refetched on every (re)connect, on every event frame, and every 15 seconds, so a missed frame costs at most one poll interval. The stream request carries no Authorization header and its read has no deadline. Errors from the relay (`401`, `403`, `404`, `409`, `429`, `5xx`, network failures) are shown in the status line. A `403` that says the token cannot approve offers to enter the approver token.

If the selected request disappears from the list while you are looking at it (someone else decided it), the selection moves to a neighbouring row and decision keys are ignored for a moment, so a key press meant for one request is never applied to another.

## Security note

The token is held in process memory only. It is never read from arguments, the environment or a file, and is never logged or written anywhere. Anyone who can run commands as root on the machine running the TUI can read its memory, so run it somewhere approved relay commands cannot reach, or close it when idle. The TUI turns off core files and marks itself non-dumpable at startup (Linux), and zeroes the token buffers it controls; the HTTP library's copy of the request header is outside its control.

## Tests

```sh
# from the repository root
make test-tui
```

This builds the relay and runs `go vet` and `go test` in `tui/`, including integration tests that start the built relay on free local ports.
