# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.2.3] - 2026-09-20

### Security
- Relay: bound the pre-cookie pending store globally with `QVOLE_RELAY_MAX_PENDING` (default 65536). The per-IP cap alone could not stop a flood spread across many source IPs.
- Relay: give REG its own rate limit (`QVOLE_RELAY_REG_RATE`, default 10/s) instead of sharing the MSG rate.
- Relay: keep the write pool and write mutex per listener instead of in package globals, so concurrent relay instances no longer share write state.
- SPAKE2: build the counter-appended PBKDF2 input in `PasswordToScalar` with a dedicated allocation, removing an `append` aliasing hazard on the retry path.
- Logger: make `util.Debug` atomic, closing a data race when debug mode is toggled while logging.

### Fixed
- `exec`: a peer stdin EOF no longer kills the child. The child is stopped only when the parent context is canceled or the QUIC connection is lost, so commands that read stdin keep running and report their real exit code.
- `pipe`: stats teardown, including the final totals and the finish delay, now runs on every return path, so the periodic reporter always stops and trailing stream data survives CONNECTION_CLOSE.
- Relay: sweep stale rooms across all shards before taking the target shard lock during cookie completion, closing a lock-order deadlock.
- Relay: evict stale clients and expired in-room pending entries before the cap checks when a cookie completes into an existing room, so an idle room no longer blocks a valid admission.
- Relay: `countPendingForIP` and the in-room pending count are O(1) via per-IP counters instead of full scans.
- Relay: count a MSG in the stats only when it was actually relayed to at least one peer.
- Relay: drop and log a REG when the pending store is full instead of growing it without bound.

### Added
- Relay environment knobs `QVOLE_RELAY_REG_RATE` and `QVOLE_RELAY_MAX_PENDING`.

### Changed
- Update `github.com/quic-go/quic-go` to v0.62.0 (adds RFC 9218 stream priorities; requires Go 1.26+).
- Update `golang.org/x/crypto` to v0.57.0, `golang.org/x/net` to v0.59.0, `golang.org/x/sys` to v0.48.0.
- Bump `golangci-lint` to v2.13.2 (CI).
- Bump CI actions to `checkout@v7`, `setup-go@v7`, `upload-artifact@v7`, `download-artifact@v8`, and `action-gh-release@v3`.
- Container runtime image moves to `alpine:3.24`.

### Docs
- Document the quic-go stream-announcement limit for locally opened streams in `docs/PROTOCOL.md` (Known Limitations): a stream that produces no output before reading stdin is not accepted by the peer until it writes or exits. A real fix needs a coordinated protocol change.
- Note the Rust port at https://github.com/fernjager/qvole-rs.

## [0.2.2] - 2026-09-07

### Security
- Relay: per-IP pending cap now counts each room's pending map too. REG floods can no longer grow it unboundedly.
- Relay: hold the shard lock until the room lock is taken. Closes an eviction race that could orphan admissions.
- Forbid incoming unidirectional QUIC streams (`MaxIncomingUniStreams: -1`). The previous 0 meant quic-go's default of 100.

### Added
- Library `Exec` and `Tunnel` now take the full engine config, so `With*` tuning options apply to them too.

### Changed
- Generated codes now print a ready-to-paste peer command for pipe, exec, and tunnel (previously just the code).
- Library zero-value options defer to the engine's env/default chain instead of CLI defaults.
- Bump Go toolchain to 1.27.1.

### Fixed
- `RunPipe` no longer waits on a blocked stdin read once the peer finishes. Local stdin gets a 5s half-close grace first.
- Stream closes now wait 500ms before closing the connection, so final data survives CONNECTION_CLOSE.
- `SplitCommand` keeps quoted empty arguments (e.g. `run '' x`).
- `exec`: signal-killed child exits 130 on local interrupt, 0 on peer close (was 255).
- `-v`/`--version` only works as the subcommand, so `exec --cmd -v` no longer prints the version.
- CLI tunnel validation fails fast on bad ports and over 100 `-L`/`-R` specs.
- Tunnel acceptor no longer hangs on a full guard after cancel. The guard is re-created when its size changes between sessions.
- Enforce the per-side tunnel request cap at 100 (was 2x on the read path).
- Relay: recycle packet read buffers after handling.

## [0.2.1] - 2026-08-29

### Security
- Peer forward targets are resolved once and dialed only to allowlisted IP literals, closing the DNS-rebinding and host-alias window.

### Changed
- Forwarded tunnel streams are no longer force-closed by an absolute 5-minute cap.
- Default QUIC keepalive interval raised from 2s to 5s (`QVOLE_KEEPALIVE_MS` / `WithKeepAlive`).

### Added
- `install.sh` verifies the download's SHA-256 against the release `SHA256SUMS` before installing.

## [0.2.0] - 2026-08-23

### Security
- Tunnel requests are denied by default: peer `-L`/`-R` requests are honored only against explicit `-aL` (listen) / `-aF` (forward) allowlist entries, or `-a` (allow all, not safe). Removed `--allow-tunnel`; library `WithAllowTunnel` replaced by `WithAllowListen`/`WithAllowForward`/`WithAllowAll` (breaking API). Invalid entries fail fast; denied requests log the exact flag to run.

### Added
- Generated codes print the peer's ready-to-paste command (`QVOLE_CODE=… qvole tunnel -aL … -aF …`).

### Changed
- Tunnel exchange: the client opens the control stream, the server accepts; both sides order requests canonically, so both-peers-request tunnels work (previously deadlocked) and stream indexes map consistently. Requires 0.2.0+ on both sides.
- Disconnect handling: remote disconnects print `Remote host disconnected` (detail only with `--debug`); local SIGINT exits quietly; tunnel bind failures surface as errors instead of exiting silently.
- Logging: `Tunnel established: ↔ target` once per spec, `Tunnel forwarding to <target>` on the dialing side, `Received N tunnel request(s) from peer` on both sides, `Awaiting tunnel request(s) from peer` when no requests are configured; per-connection and copy teardown logs only with `--debug`.
- Version 0.2.0, including the 0.1.4 toolchain bumps (Go 1.27, x/crypto v0.55, x/net v0.58, golangci-lint v2.13.1). Wire format unchanged (ALPN `qvole-v0.1`); tunnel sessions require 0.2.0+ on both sides, pipe/exec remain interoperable with 0.1.x.

### Docs
- README, SECURITY, PROTOCOL, LIBRARY, and the static site updated for the allowlist model.

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

