# AGENTS.md

## Build & Test

```bash
make build          # go build -o bin/qvole ./cmd/qvole
make test           # unit + integration
make test-unit      # all packages except ./tests/
make test-integration   # ./tests/ only
make lint           # golangci-lint (CI uses latest v2)
```

- Go 1.26+ (see `go.mod`).
- Lint config is `version: "2"` format. CI uses `golangci-lint v2.12.2` via `golangci-lint-action@v9`.
- Integration tests (`tests/`) compile the binary via `TestMain` into a temp dir, then run it against a temp relay on random ports starting at 19000.

## Architecture

```
github.com/fernjager/qvole          # public library API (Dial, Accept, Connect, Exec, Tunnel)
cmd/qvole/                           # CLI binary entrypoint (package main)
internal/app/                        # CLI command logic (pipe, exec, tunnel)
internal/engine/                     # core: SPAKE2 exchange, hole punch, QUIC transport, stats
internal/util/                       # certs, code/nameplate, env helpers, logger
relay/                               # relay server (standalone package)
spake2/                              # SPAKE2 PAKE (standalone package)
tests/                               # integration tests (compile + run binary)
```

- **Root package** = public Go library. `internal/` packages are not importable externally.
- `relay/` and `spake2/` are **not under `internal/`** — they are part of the public API.
- The single binary is at `cmd/qvole/`. Subcommands are dispatched in `main.go`.
- Platform-specific files use `_unix.go` / `_windows.go` suffixes: `signal_*.go`, `conn_rebind_*.go`.

## Configuration Pattern

All tunables use a three-tier fallback: **option value → env var → default**. Zero values mean "use default".

```go
// engine/connect.go — PeerConfig methods delegate to resolveDur/resolveInt
func (c PeerConfig) punchTimeout() time.Duration {
    return resolveDur(c.PunchTimeout, "QVOLE_PUNCH_TIMEOUT_MS", defaultPunchTimeout)
}
```

When adding a zero-means-default field, remember that `time.Duration(0)` is treated as absent. Never use `0` as an actual config value for durations or ints.

## Fuzz Tests

Fuzz tests are in `cmd/qvole/fuzz_test.go` (package `main`), **not** in the packages they exercise. CI runs each fuzz target for 30s with custom error handling:

```bash
go test -fuzz=FuzzNameplate -fuzztime=30s -run='^$' ./cmd/qvole/
```

## Key Environment Variables

All `QVOLE_*` env vars are read at runtime (not just via CLI flags). See README for full list. Notable ones:
- `QVOLE_CODE` — connection code (alternative to `--code`)
- `QUIC_GO_DISABLE_RECEIVE_BUFFER_WARNING` — set to `1` in `main.go` to suppress quic-go warnings
- `NO_COLOR` — disables ANSI color in log output

## Docker

`docker-compose.yml` only runs the relay service (`qvole relay`). Not used for dev or testing.
