# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.4] - 2026-08-22

### Changed
- Bump Go toolchain to 1.27.0.
- Update `golang.org/x/crypto` to v0.55.0.
- Update `golang.org/x/net` to v0.58.0.
- Bump `golangci-lint` to v2.13.1 (CI).

## [0.1.3] - 2026-08-08
### Security
- Zeroize intermediate scalar byte buffers after use in `PasswordToScalar`, `generatorScalar`, and `ComputeShared` to close zeroization gaps.
- `SessionAAD` now returns a fresh allocation instead of appending to the caller's slice, preventing accidental mutation of caller-owned buffers.
- Relay: defer room creation until cookie verification completes, preventing room-table exhaustion from spoofed initial REGs.
- Relay: re-check `maxClientsHard` and `maxRoomsPerIP` caps at cookie admission and at cookie completion into an already-existing room, closing a race where pending cookies could bypass limits.
- Relay: add `maxPendingPerIP` cap (50) on pre-cookie registrations to bound pending-store memory under flood.
- Library: enforce documented 8-256 character code length in `validate()` (previously only checked by CLI). `MinCodeLen`/`MaxCodeLen` exported.
- Tunnel: empty listen host in 4-part `-L :port:host:port` syntax now defaults to `127.0.0.1` instead of `0.0.0.0`.
- SPAKE2: reject reflected (equal) blinded points during exchange to prevent self-authentication sabotage.
- SPAKE2: zeroize the transient password+counter buffer on the PBKDF2 retry path in `PasswordToScalar`.
- install.sh: use `mktemp -d` with trap cleanup instead of predictable `/tmp/qvole` to close a TOCTOU race.

### Fixed
- CI fuzz job now fails on any non-zero exit code (including crasher-found = exit 1) instead of silently tolerating it, restoring the advertised fuzz assurance.
- Nil-deref panic in `ConnectPeer` when `crypto/rand.Read` fails in the hole-punch goroutine; the select now checks for nil before dereferencing `observedAddr`.
- Peer-supplied address string now printed with `%q` in error messages to prevent ANSI escape injection in terminal output.
- Correct misleading comment describing the QUIC rebind as a "wildcard socket" (the code binds the concrete local address, which is safer).
- Tunnel stream deadline comment now correctly describes the absolute 5-minute lifetime cap rather than a refreshing idle deadline.
- `LIBRARY.md` no longer instructs users to import `internal/engine` (which cannot compile externally).
- `WithForwardMaxStreams` library option was a silent no-op; `toPeerConfig` did not forward it. Now wired through `PeerConfig.ForwardMaxStreams` → `RunTunnel` → guard channel creation. The option overrides `QVOLE_FORWARD_MAX_STREAMS`; zero means use env/default.
- Remove 4 hyphenated words (`drop-down`, `felt-tip`, `t-shirt`, `yo-yo`) from the code wordlist. Hyphens in words broke `--`-joined code parsing (~0.15% of generated codes affected). Wordlist is now 7762 words.
- Relay: REG with a cookie for a room whose pending registration expired (`pendingRegTTL`) or was evicted now re-issues a fresh challenge via the pending store instead of dropping, so the handshake can still complete.

### Changed
- Update `golang.org/x/net` to v0.57.0.
- Update `github.com/quic-go/quic-go` to v0.61.0.

## [0.1.2] - 2026-07-12
### Security
- Bump Go toolchain to 1.26.5 for `crypto/tls` and `os` security fixes.
- Update `golang.org/x/crypto` to v0.54.0, `golang.org/x/sys` to v0.47.0.

### Fixed
- Suppress benign close errors in `bidirectionalCopy`: `net.ErrClosed`, `io.ErrClosedPipe`, and `quic.ApplicationError` with code 0 are now filtered instead of being logged as errors. These are normal teardown artifacts from concurrent stream shutdown.

### Changed
- Replace `time.Sleep` with `waitForListener` in tunnel integration tests for faster, more reliable CI runs.

## [0.1.1] - 2026-06-18
### Security
- Fix SPAKE2 offline dictionary oracle: independent ephemeral scalars per blinded point.
- Add UDP return-routability cookie to REG handshake (prevents source-IP spoofing).
- Relay hardening: writer pool, IPv6 canonicalization, bounded maps, per-IP log limiting.
- Tunnel hardening: control-stream deadlines, idle timeout, outbound guard.

### Changed
- Two-step REG handshake with cookie challenge (see PROTOCOL.md).
- Hard cap 20 clients per room (`maxClientsHard`), TTL 1 min, max 10 rooms/IP.
- Exchange deadline 90 s, re-reg interval 30 s.
- `QVOLE_KDF_ITERATIONS` floor 100k, MSG phase allowlist, deterministic scalar retry.

## [0.1.0] - 2026-06-07
### Added
- Initial release of qvole.
- P2P tunneling over QUIC with SPAKE2 authentication.
- Pipe, Exec, and Tunnel subcommands.
- UDP hole punching for NAT traversal.
- Stateless relay for peer discovery.
- Public Go library API: Dial, Accept, Connect, Exec, Tunnel, with functional options.

