# qvole Protocol Specification

This document describes the wire-level protocol: how two qvole peers
authenticate, establish a direct QUIC connection, and transfer data. The relay
forwards opaque blobs via plaintext UDP but cannot derive
session keys, decrypt traffic, or impersonate a peer.

For the threat model, cryptographic design rationale, and security analysis,
see [SECURITY.md](./SECURITY.md).

---

## 1. Architecture Overview

```
Peer A                              Relay                              Peer B
  │                                   │                                   │
  │── REG <room> ────────────────────►│                                   │
  │◄─ REGD <room> <addr> ─────────────│                                   │
  │                                   │◄── REG <room> ────────────────────│
  │                                   │─── REGD <room> <addr> ───────────►│
  │                                   │                                   │
  │── MSG <room> spake2 <hex> ───────►│◄── MSG <room> spake2 <hex> ───────│
  │◄─ MSGD spake2 <hex> ──────────────│─── MSGD spake2 <hex> ────────────►│
  │                                   │                                   │
  │── MSG <room> confirm <hex> ──────►│◄── MSG <room> confirm <hex> ──────│
  │◄─ MSGD confirm <hex> ─────────────│─── MSGD confirm <hex> ───────────►│
  │                                   │                                   │
  │  ╔════════════════════════════════════════════════════════════════╗   │
  │  ║               Direct QUIC (hole-punched)                       ║   │
  │  ╚════════════════════════════════════════════════════════════════╝   │
  │                                                                       │
  │  ◄═══════════ TLS 1.3 handshake (pinned cert fingerprints) ════════►  │
  │                                                                       │
  │  ◄═══════════ QUIC streams, application data ──────────────────────►  │
```

**Relay transport:** The relay uses UDP text datagrams (`REG`/`MSG`/`MSGD`). Messages
are single UDP datagrams (≤ 1400 bytes) terminated by `\n`. The SPAKE2 payloads are
hex-encoded binary data.

## 2. Protocol Phases

### Phase 0, Code Sharing (out of band)

Before any connection, the two peers must share a **code**, the sole shared
secret. The code is agreed upon through any trusted channel (encrypted
messaging, QR code, voice call, etc.).

**Generated codes** have the format:

```
NNNN-word-word-word
```

Where `NNNN` is a 4-digit number (0000-9999) and each word is drawn from a
dictionary of 7766 words. Generated codes have ~52 bits of entropy.

**User-provided codes** are any string 8-256 characters in length.

**Room derivation**, the relay room name is derived from the code:

- Generated codes: the 4-digit prefix is the room name directly (e.g. `"9908"`).
- User-provided codes: the first 4 bytes of `SHA-256("qvole-nameplate:" + code)`
  as hex (e.g. `"a1b2c3d4"`).

Both peers compute the same room name and connect to the same relay.

---

### Phase 1, Relay Registration

```
Peer         Relay
  │            │
  │─── REG ───►│  Register in room "<room>"
  │◄── REGD ───│  Acknowledged with external address
  │            │
  │─── MSG ───►│  Send spake2/confirm payload
  │◄── MSGD ───│  Receive peer's messages
```

1. Peer sends `REG <room>\n`.
2. Relay validates the room name.
3. Relay replies `REGD <room> <addr>\n`, the `<addr>` is the peer's external
   (UDP source) address as seen by the relay.
4. Messages are sent as `MSG <room> <phase> <hex>\n` and delivered as `MSGD <phase> <hex>\n`.
   Messages are forwarded to all other registered clients in the room; there is
   no message queuing; if no other client is registered, the message is dropped.
   Peers retransmit REG every 60 s and SPAKE2/confirm payloads every 2 s
   throughout the exchange; each received SPAKE2 point creates an
   independent candidate, and confirms are tried against all known
   candidates.

**Registration lifecycle:**
- Clients expire after 5 min without a REG message.
- Rooms expire when empty for 5 min.
- A cleanup sweep runs every 60 s.
- Room capacity (10,000) uses stale-room eviction before rejecting new registrations.
- Per-IP room cap (100) prevents a single host from exhausting the relay.

---

### Phase 2, SPAKE2 Key Exchange

SPAKE2 is a Password-Authenticated Key Exchange. Both peers derive a shared
symmetric key from the code without the relay learning the code or the key.

```
                   Peer A                     Relay                     Peer B
                     │                          │                          │
                     │─ blindedPointA (spake2)─►│◄─ blindedPointB (spake2)─│
                     │◄─blindedPointB (MSGD)────│── blindedPointA (MSGD)──►│
                     │                          │                          │
                     │   compute session key    │   compute session key    │
                     │   role = A > B ? srv:cli │   role = B > A ? srv:cli │
                     │                          │                          │
                     │── confirmA + encAddrA ──►│◄── confirmB + encAddrB ──│
                     │◄─ confirmB + encAddrB ───│─── confirmA + encAddrA ─►│
                     │                          │                          │
                     │   verify confirm         │   verify confirm         │
                     │   decrypt peer address   │   decrypt peer address   │
```

The exchange is **stateless**: peers broadcast their SPAKE2 points continuously
and respond to every incoming point with a confirm. Each received point becomes
an independent *candidate* with its own confirm retry cycle. Candidates are capped
at 50 and buffered confirms at 200 to bound resource consumption. Incoming confirms
are buffered and tried against all known candidates. The exchange exits on the
first matching confirm or after the **5-minute exchange deadline**; no phase
coordination, no single-in-flight state machine, no replacement logic, no
reset-on-failure.

#### 2a. Blinded Point Exchange

Each peer:

1. Generates an ephemeral random 256-bit scalar.
2. Derives a **password scalar** from the code via PBKDF2-HMAC-SHA256
   (600,000 iterations by default, tunable via `QVOLE_KDF_ITERATIONS`)
   reduced modulo the P-256 curve order. Both peers must use the same
   iteration count.
3. Computes two **blinded points**:
   - `(scalar · G) + (password scalar · M)`, where G is the standard P-256
     generator and M is a domain-separated custom generator.
   - `(scalar · G) + (password scalar · N)`, using the second domain-separated
     generator N.
4. Serializes both points as uncompressed 65-byte P-256 points (`04 || X || Y`).
5. Appends its SHA-256 certificate fingerprint (32 bytes) to the payload.
6. Sends the 162-byte payload to the relay.

**Payload layout (162 bytes):**

```
 Offset  Size  Field
 ──────  ────  ─────
 0       65    Uncompressed P-256 M-blinded point (04 || X || Y)
 65      65    Uncompressed P-256 N-blinded point (04 || X || Y)
 130     32    SHA-256 certificate fingerprint
```

Sent as `MSG <room> spake2 <hex>\n` and retransmitted every 2 s continuously;
duplicate points from the same peer are deduplicated by point value.

**On receipt**, the peer:

1. Validates both points are on the P-256 curve.
2. Determines role: `bytes.Compare(myPointM, peerPointM) > 0` → server (uses N generator),
   otherwise → client (uses M generator).
3. Unblinds: subtracts (password scalar · appropriate generator) from the received point.
4. Multiplies the result by its own random scalar to produce a shared point.
5. The X-coordinate of the shared point becomes the shared secret.

#### 2b. Session Key Derivation

From the shared secret, two keys are derived via HKDF-SHA256:

```
shared secret (32 bytes, zero-padded)
    │
    └── HKDF-SHA256(secret, salt, info="qvole-spake2-session") ── 64 bytes
            │
            ├── bytes [0:32]   →   confirmKey   (HMAC key)
            └── bytes [32:64]  →   encKey       (AES-256-GCM key)
```

The HKDF salt is `SHA-256(sortedPoints || marshal(N))`, where N is the
domain-separated custom generator. This binds the session key to the
specific SPAKE2 group and ensures domain separation between sessions.

#### 2c. Server/Client Role Assignment

```
isServer = bytes.Compare(myPointM, peerPointM) > 0
```

Lexicographic comparison of the 65-byte uncompressed M-blinded point bytes.
Guarantees opposite server/client roles without an extra message or relay
involvement.

#### 2d. Confirmation Handshake

Each peer constructs a confirm payload:

**Payload layout (160 bytes):**

```
 Offset  Size  Field
 ──────  ────  ─────
 0       16    Random nonce
 16      32    HMAC-SHA256(confirmKey, sortedPoints || marshal(N) || peerFingerprint || nonce || "qvole-spake2-confirm")
 48      12    AES-256-GCM nonce
 60      52    Encrypted (null-padded) peer UDP address
 112     16    AES-256-GCM auth tag
 128     32    Random padding
 ──────  ────  ─────
 Total   160
```

Sent as `MSG <room> confirm <hex>\n` and retransmitted every 2 s for each
received candidate point until a matching confirm is found from the peer.

The encrypted address uses AES-256-GCM with the sorted blind points and
the marshalled N generator as Additional Authenticated Data (AAD), binding
the address to the specific exchange. Addresses are validated against a 52-byte maximum (rejecting
longer addresses) and null-padded to exactly 52 bytes before encryption,
ensuring a fixed ciphertext size regardless of IPv4/IPv6 address length.

**On receipt**, the peer verifies the HMAC, decrypts the address, and trims
null padding to recover the peer's UDP address string.

**At this point both peers possess:**
- Each other's UDP address (for hole punching / direct QUIC).
- Each other's certificate fingerprint (for TLS verification).
- Shared symmetric keys (confirm HMAC key, encryption key, zeroed after use).

#### 2e. Socket Re-binding

During the SPAKE2 exchange, each peer uses a **connected UDP socket**
(`net.DialUDP`) to the relay. This causes the kernel to filter incoming
datagrams at the socket level: only packets from the connected relay
address are delivered, preventing UDP source spoofing on shared networks.

After the exchange completes, the connected socket is closed and a **wildcard
socket** is re-bound on the same local port using `SO_REUSEADDR`. This
wildcard socket is then used for hole punching and the QUIC connection.
The local port is preserved across the transition so that the NAT mapping
established during relay communication remains valid for the direct P2P path.

---

### Phase 3, UDP Hole Punching

Both peers attempt to punch a hole through NATs by sending UDP packets directly
to each other.

```
Peer A                                    Peer B
  │                                         │
  │─── punch packet ───────────────────────►│  (direct UDP, may arrive or not)
  │◄── punch packet ────────────────────────│  (direct UDP, may arrive or not)
  │                                         │
  │     ... ramping interval & size ...     │
  │                                         │
  │    first packet from peer = success     │
```

**Strategy:**
- Intervals ramp: 50 ms → 100 ms → 200 ms (changed every 5 attempts).
- Payload sizes cycle: 5 B → 50 B → 100 B → 200 B (random content).
- A concurrent listener on the same UDP socket detects any packet from the
  peer's address; this signals success.

**Timeout:** 10 s (configurable via `QVOLE_PUNCH_TIMEOUT_MS`). If hole punching
times out, QUIC establishment is attempted anyway; the OS may already have an
open NAT path from relay communication or prior punch attempts.

**Same-machine testing:** Hole punching between two peers behind the same NAT
requires the NAT to support hairpinning (NAT loopback). Many consumer routers
do not, and in those cases the punch will time out. For local testing, resolve
peer addresses to localhost by running a local relay (`qvole relay --listen :9009`)
and pointing both peers at `127.0.0.1:9009`.

Once hole punching succeeds, a helper goroutine sends keep-alive packets
(`0x01`) to the peer every 50 ms for 3 s. This holds the NAT pin-hole open
while the QUIC TLS 1.3 handshake completes; without it, a NAT that times out
UDP mappings quickly might close the hole before QUIC establishes.

For security properties of hole punching, see [SECURITY.md](./SECURITY.md) §5.

---

### Phase 4, QUIC Connection Setup

```
Peer A (server)                           Peer B (client)
  │                                         │
  │   quic.Listen(udpConn, tlsConf)         │
  │   ln.Accept(ctx)                        │
  │                                         │   quic.Dial(ctx, udpConn, addr, tlsConf)
  │◄══════════ TLS 1.3 handshake ══════════►│
  │                                         │
  │   VerifyPeerCertificate:                │   VerifyPeerCertificate:
  │   sha256(peerCert) == peerFingerprint   │   sha256(peerCert) == peerFingerprint
```

**TLS configuration:**
- **Min version:** TLS 1.3.
- **Certificates:** Ephemeral self-signed ECDSA P-256, generated per connection.
  The certificate fingerprints were exchanged inside the SPAKE2 blinded point
  payload, binding the TLS session to the PAKE.
- **Peer verification:** `InsecureSkipVerify: true` disables standard CA-chain
  validation. Instead, a custom callback compares `SHA-256(peer's DER cert)`
  against the fingerprint from SPAKE2 using constant-time comparison.
- **ALPN:** `"qvole-v0.1"`.
- **Mutual auth:** Server requires client certificate; client sends its cert.
- **No uni-directional streams:** `MaxIncomingUniStreams = 0`.

**QUIC parameters:**

| Parameter | Default | Env Override |
|-----------|---------|-------------|
| MaxIncomingStreams | 100 | `QVOLE_MAX_STREAMS` |
| KeepAlivePeriod | 2 s | `QVOLE_KEEPALIVE_MS` |
| MaxIdleTimeout | 2 min | `QVOLE_IDLE_TIMEOUT_MS` |
| HandshakeIdleTimeout | 30 s | `QVOLE_HANDSHAKE_TIMEOUT_MS` |
| InitialStreamReceiveWindow | 1 MB | `QVOLE_INITIAL_STREAM_WINDOW` |
| InitialConnectionReceiveWindow | 4 MB | `QVOLE_INITIAL_CONNECTION_WINDOW` |

The same UDP socket is reused for relay communication, hole punching, and QUIC.

---

### Phase 5, Data Transfer

Once the QUIC connection is established, data flows over QUIC bidirectional
streams. The exact flow depends on the subcommand.

#### Pipe Mode (`qvole`)

```
Local peer                              Remote peer
  │                                         │
  │   stdin ───► OpenStream() ─────────────►│──► AcceptStream() ──► stdout
  │   stdout ◄── AcceptStream() ◄───────────│◄── OpenStream() ◄──── stdin
```

Two goroutines copy data in opposite directions using pooled 32 KB buffers.
A `sync.Once` ensures both sides close when either direction completes.

#### Exec Mode (`qvole exec --cmd "command"`)

```
├─ side with --cmd ───────────────────────────────────────────────┤
│                                                                 │
│  OpenStream() ──► exec command                                  │
│    stdin ◄── command input                                      │
│    stdout ──► data to stream                                    │
│    stderr ──► local terminal                                    │
│                                                                 │
├─ side without --cmd ────────────────────────────────────────────┤
│                                                                 │
│  AcceptStream()                                                 │
│    stdin ──► stream                                             │
│    stream ──► stdout                                            │
```

The `--cmd` side always opens the QUIC stream (regardless of SPAKE2 role),
runs the command directly via `os/exec`, and bridges stdin/stdout to the
stream while printing stderr locally. The peer side accepts the stream and
operates as a plain pipe (stdin → stream → stdout). The exit code propagates
from the command side to the peer side.

After the command exits, the stream is drained for up to 5 s (configurable via
`QVOLE_EXEC_DRAIN_TIMEOUT_MS`) to allow the peer to receive any buffered output
before the stream closes.

#### Tunnel Mode (`qvole tunnel -L ... -R ...`)

```
├─ Control Stream ────────────────────────────────────────────────┤
│                                                                 │
│  (race) ┌─ winner ─── sends requests ──► reads peer requests    │
│         └─ loser ──── reads requests ◄── sends requests         │
│                                                                 │
├─ Data Streams ──────────────────────────────────────────────────┤
│                                                                 │
│  Local forward (-L 8080:localhost:80):                          │
│    1. Listen on TCP :8080                                       │
│    2. Accept TCP connection                                     │
│    3. Open QUIC stream, write 2-byte spec index + data          │
│                                                                 │
│  Remote forward (-R 2222:localhost:22):                         │
│    1. Wait for QUIC stream with spec index 0                    │
│    2. Dial TCP localhost:22                                     │
│    3. Bridge stream ↔ TCP                                       │
│                                                                 │
│  My -L / peer's -R → I listen TCP, I open QUIC streams.         │
│  My -R / peer's -L → I accept QUIC streams, I dial TCP.         │
```

The control stream is opened immediately after QUIC connects. The peer with
tunnel requests opens the stream, writes its accept status and requests + `END`,
then reads the peer's accept status and requests from the same stream. The peer
without requests accepts, reads first, then writes its own accept status and
requests back. The accepting side sets a 30 s read deadline to prevent a stalled
peer from holding the stream open indefinitely.

**Control stream format:**
```
ACCEPT true
L <listenAddr> <targetAddr>
R <listenAddr> <targetAddr>
END
```

Each peer declares whether it accepts incoming tunnel connections
(`ACCEPT true` or `ACCEPT false`) before listing its requests. If a peer
declares `ACCEPT false`, the other peer skips binding for that peer's `-R`
requests and logs a warning.

- `L`: local forward, listen on `<listenAddr>`, forward to `<targetAddr>`.
- `R`: remote forward, peer listens on `<listenAddr>`, forwards to `<targetAddr>`.
- Up to 100 requests per side (200 total).

**Data stream format:**
```
 Offset  Size  Field
 ──────  ────  ─────
 0       2     uint16 big-endian spec index
 2       N     raw TCP payload
```

Each TCP connection gets its own QUIC stream, identified by the 2-byte spec
index that maps to the target address from the control stream exchange. The
receiving side sets a 15 s read deadline on the 2-byte header; streams that
do not deliver the header within this window are closed to prevent resource
exhaustion from stalled or malicious peers.

---

### Phase 6, Teardown

```
SIGINT / context cancellation
    │
    ├── conn.CloseWithError(0, "canceled")
    ├── QUIC listeners close
    ├── streams / TCP connections drain
    ├── stdin closed
    ├── io.CopyBuffer returns
    ├── sync.WaitGroup done
    └── process exits
```

When either peer's context is cancelled (e.g. SIGINT), the QUIC connection is
closed gracefully, streams drain, and the process exits.

---

## 3. Wire Format Reference

### 3.1 Relay Messages

All relay messages are single UDP datagrams (≤ 1400 bytes) terminated by `\n`.
Hex payloads in `MSG`/`MSGD` are lowercase hex-encoded bytes with no `0x` prefix,
one datagram per line. Messages are fire-and-forget: no message IDs, no
acknowledgements, and no delivery ordering. Peers retransmit to overcome loss
(see §5 for relay rate limits).

| Direction | Format | Description |
|-----------|--------|-------------|
| Client → Relay | `REG <room>\n` | Register in room |
| Relay → Client | `REGD <room> <addr>\n` | Registration acknowledged with external address |
| Client → Relay | `MSG <room> spake2 <hex>\n` | SPAKE2 blinded point + fingerprint |
| Client → Relay | `MSG <room> confirm <hex>\n` | HMAC confirm + encrypted address |
| Relay → Client | `MSGD spake2 <hex>\n` | Forwarded SPAKE2 payload |
| Relay → Client | `MSGD confirm <hex>\n` | Forwarded confirm payload |

### 3.2 SPAKE2 Blinded Point Payload

```
 Offset  Size  Field
 ──────  ────  ─────
 0       65    Uncompressed P-256 M-blinded point (04 || X || Y)
 65      65    Uncompressed P-256 N-blinded point (04 || X || Y)
 130     32    SHA-256 certificate fingerprint
 ──────  ────  ─────
 Total   162
```

### 3.3 SPAKE2 Confirm Payload

```
 Offset  Size  Field
 ──────  ────  ─────
 0       16    Random nonce
 16      32    HMAC-SHA256(confirmKey, sortedPoints || marshal(N) || peerFingerprint || nonce || "qvole-spake2-confirm")
 48      12    AES-256-GCM nonce
 60      52    Encrypted (null-padded) peer UDP address
 112     16    AES-256-GCM auth tag
 128     32    Random padding
 ──────  ────  ─────
 Total   160
```

### 3.4 Tunnel Control Stream

```
ACCEPT true\n
<type> <listenAddr> <targetAddr>\n
...
END\n
```

- `ACCEPT true` or `ACCEPT false`: whether the peer accepts incoming tunnel connections.
  If `false`, the other side skips binding listeners for any tunnel requests from this peer.
- `<type>`: `L` (local forward) or `R` (remote forward).
- `<listenAddr>`: address on which the listener binds.
- `<targetAddr>`: address to dial for incoming connections.

### 3.5 Tunnel Data Stream

```
 Offset  Size  Field
 ──────  ────  ─────
 0       2     uint16 big-endian spec index
 2       N     raw TCP payload
```

---

## 4. Subcommand Flow Differences

### Pipe Mode (`qvole`)

| Aspect | Local | Remote |
|--------|-------|--------|
| Stream initiator | Opens stream when stdin is not a terminal (piped); otherwise accepts | Accepts stream |
| Data transfer | stdin → stream, stream → stdout | stdout ← stream, stdin ← stream |
| Exit on | Either stream closes | Either stream closes |

### Exec Mode (`qvole exec`)

| Aspect | `--cmd` side | Peer side (no `--cmd`) |
|--------|-------------|------------------------|
| Stream initiator | Always opens stream | Always accepts stream |
| Process | Direct command execution | Plain pipe (stdin/stdout) |
| Exit code | Propagated to peer | Exits with command's code |

### Tunnel Mode (`qvole tunnel`)

| Aspect | Side with `-L`/`-R` | Side without requests |
|--------|---------------------|-------------------|
| Control stream | Opens or accepts (race) | Opens or accepts (race) |
| `-L` listener | TCP listen, opens QUIC streams | Accepts QUIC streams, dials TCP |
| `-R` listener | Accepts QUIC streams, dials TCP | TCP listen, opens QUIC streams |

---

## 5. Relay Constraints

The relay imposes hard limits that peers rely on for brute-force resistance.
A protocol implementer must not exceed these:

| Constraint | Value | Purpose |
|---|---|---|
| Max message rate per client | 10 msg/s | Online brute-force protection |
| Client TTL (REG renewal) | 5 min | Stale client eviction |
| Room TTL when empty | 5 min | Garbage collection |
| Max clients per room | 2 (default, tunable) | Resource bounding |
| Max rooms | 10,000 | Relay capacity |
| Max rooms per IP | 100 | Single-host DoS mitigation |
| Max UDP datagram length | 1400 bytes | Safe payload ceiling; avoids fragmentation across Ethernet (1500 MTU minus IP+UDP headers) and accommodates tunnel overhead from IPv6, VPNs, and PPPoE without relying on path MTU discovery, which is unreliable for UDP |
| Room name max length | 64 chars | Validation |

Messages are fire-and-forget: there are no message IDs, no
acknowledgements, and no ordering guarantees. Peers retransmit continuously
to overcome packet loss (see §2).

---

## 6. Versioning

The qvole wire protocol uses the **MAJOR.MINOR** portion of the software version
to gate compatibility. Peers negotiate the protocol version via TLS ALPN; the relay
is unaware of versioning.

**ALPN string:** `"qvole-v<MAJOR>.<MINOR>"` (e.g. `"qvole-v0.1"`).

| Bump | Example | Meaning |
|------|---------|---------|
| Patch (`0.1.X`) | `0.1.3 → 0.1.4` | Wire-compatible: new subcommands, bugfixes, internal refactors, default tuning |
| Minor (`0.X.0`) | `0.1.0 → 0.2.0` | Wire-breaking: SPAKE2 layout change, relay message format, certificate algorithm, domain separation strings, or ALPN change |
| Major (`X.0.0`) | `0.1.0 → 1.0.0` | Protocol redesign that breaks ALPN compatibility with all prior versions

Peers that don't agree on `MAJOR.MINOR` fail during the TLS handshake with an ALPN
mismatch before any application data flows. Patch-level differences (`0.1.3` vs
`0.1.7`) produce the same ALPN string and interoperate.

The `ProtocolVersion` constant is hand-maintained in `internal/engine/transport.go`
and bumped intentionally alongside the corresponding wire-format changes.
The software version displayed by `qvole --version` is set independently via
build-time ldflags (git tags).
