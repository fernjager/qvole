# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]
### Changed
- Update `golang.org/x/net` to v0.57.0.

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

