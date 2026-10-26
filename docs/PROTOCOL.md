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
  │◄─ REGD <room> <cookie> ───────────│                                   │
  │── REG <room> <cookie> ───────────►│                                   │
  │◄─ REGD <room> OK <addr> ──────────│                                   │
  │                                   │◄── REG <room> ────────────────────│
  │                                   │─── REGD <room> <cookie> ─────────►│
  │                                   │◄── REG <room> <cookie> ───────────│
  │                                   │─── REGD <room> OK <addr> ────────►│
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
dictionary of 7762 words. Generated codes have ~52 bits of entropy.

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
  │◄── REGD ───│  Cookie challenge (32 hex chars)
  │─── REG ───►│  Echo cookie to prove return routability
  │◄── REGD ───│  Acknowledged with external address (OK <addr>)
  │            │
  │─── MSG ───►│  Send spake2/confirm payload
  │◄── MSGD ───│  Receive peer's messages
```

1. Peer sends `REG <room>\n`.
2. Relay validates the room name and replies `REGD <room> <cookie>\n`, where
   `<cookie>` is 32 hex characters (16 random bytes).
3. Peer echoes the cookie: `REG <room> <cookie>\n`. This proves the peer
   can receive datagrams at the claimed source address (source-IP spoofing
   would not receive the cookie).
4. Relay admits the peer and replies `REGD <room> OK <addr>\n`, where `<addr>`
   is the peer's external (UDP source) address as seen by the relay.
5. Messages are sent as `MSG <room> <phase> <hex>\n` and delivered as `MSGD <phase> <hex>\n`.
   Messages are forwarded to all other registered clients in the room; there is
   no message queuing; if no other client is registered, the message is dropped.
   Peers retransmit REG every 30 s and SPAKE2/confirm payloads every 2 s
   throughout the exchange; each received SPAKE2 point creates an
   independent candidate, and confirms are tried against all known
   candidates.

**Registration lifecycle:**
- Clients expire after 1 min without a REG message.
- Rooms expire when empty for 1 min.
- Pending (cookie-challenged) registrations expire after 5 s.
- A cleanup sweep runs every 1 min for rooms and every 5 s for rate-limit maps.
- Room capacity (10,000) uses stale-room eviction before rejecting new registrations.
- Per-IP room cap (10) prevents a single host from exhausting the relay.
- Hard client cap per room (20, default `maxClientsHard`) as an abuse guard.

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
first matching confirm or after the **90-second exchange deadline**; no phase
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

The HKDF salt is
`SHA-256(sortedPoints || marshal(N) || "qvole-spake2-fingerprints" || sorted(myFingerprint, peerFingerprint))`,
where N is the domain-separated custom generator. Binding both certificate
fingerprints (in sorted order, so both peers agree) prevents an active relay
from substituting the fingerprint field in the SPAKE2 payload.

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
 16      32    HMAC-SHA256(confirmKey, sortedPoints || marshal(N)
                 || "qvole-spake2-confirm-sender" || senderFingerprint
                 || "qvole-spake2-confirm-receiver" || receiverFingerprint
                 || nonce || "qvole-spake2-confirm")
 48      12    AES-256-GCM nonce
 60      52    Encrypted (null-padded) peer UDP address
 112     16    AES-256-GCM auth tag
 128     32    Random padding
 ──────  ────  ─────
 Total   160
```

The sender fills in its own fingerprint as `senderFingerprint` and the peer's
claimed fingerprint as `receiverFingerprint`; the verifier checks the opposite
direction (`sender = peer`, `receiver = self`). This binds both TLS pins to the
PAKE and rejects reflected payloads (identical fingerprints).

Sent as `MSG <room> confirm <hex>\n` and retransmitted every 2 s for each
received candidate point until a matching confirm is found from the peer.

The encrypted address uses AES-256-GCM with the sorted blind points as
Additional Authenticated Data (AAD), binding
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
- **ALPN:** `"qvole-v0.2"`.
- **Mutual auth:** Server requires client certificate; client sends its cert.
- **No uni-directional streams:** `MaxIncomingUniStreams = -1` (quic-go
  clamps negative values to 0; a literal `0` would select the default of 100).

**QUIC parameters:**

| Parameter | Default | Env Override |
|-----------|---------|-------------|
| MaxIncomingStreams | 100 | `QVOLE_MAX_STREAMS` |
| KeepAlivePeriod | 5 s | `QVOLE_KEEPALIVE_MS` |
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

See section 7 for the stream-announcement limit that applies when neither peer
sends a byte or closes its stream.

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

See section 7 before relying on a command that reads stdin before writing any
output.

#### Tunnel Mode (`qvole tunnel -L ... -R ...`)

```
├─ Control Stream ────────────────────────────────────────────────┤
│                                                                 │
│  client ─── opens stream, sends requests ──► reads peer requests│
│  server ─── accepts stream, reads requests ◄── sends requests   │
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

The control stream is opened immediately after QUIC connects. The client side
opens the stream and the server side accepts it. Each side writes its accept
status and requests + `END` on the stream, then reads the peer's from the same
stream. Reads and writes are bounded by a 30 s deadline so a stalled peer
cannot hold the exchange open indefinitely.

Both sides order the combined request list identically: the client's requests
come first, then the server's. Each side tracks the index range of its own
requests within that canonical ordering (the client's start at 0, the server's
after the client's requests), so the 2-byte spec index on data streams maps to
the same request on both peers.

**Control stream format:**
```
ACCEPT true
L <listenAddr> <targetAddr>
R <listenAddr> <targetAddr>
END
```

Each peer declares whether it is willing to accept incoming tunnel connections
(`ACCEPT true` or `ACCEPT false`) before listing its requests. `ACCEPT true`
means the peer has a tunnel allowlist configured (`-aL`/`-aF`, or the `-a`
allow-all, which is not safe); individual requests are still filtered against
that allowlist
on the receiving side.

`ACCEPT` is a coarse, boolean signal: it says "I will honor *some* peer
requests", not which ones. Its exact effects:

- A peer that declares `ACCEPT false` will not dial any peer-initiated streams,
  so the other side skips binding **its own `-L` listeners** (and logs a warning
  telling its operator which `-aL`/`-aF` flag the peer must run).
- Peer `-R` requests are gated by the *receiving* side's own `-aL` allowlist
  entries, not by the peer's `ACCEPT`: a request whose `<listenAddr>` matches no
  `-aL` entry is ignored with a warning, even when the peer declared
  `ACCEPT true`.
- Peer `-L` requests are gated by the receiving side's own `-aF` allowlist
  entries: a stream whose `<targetAddr>` matches no `-aF` entry is rejected with
  a warning, even when the peer declared `ACCEPT true`.

Because `ACCEPT` is per-side rather than per-request, a peer with a allowlist
that does not cover a particular request may still cause the requesting side to
bind a listener that will reject every connection (with the rejection visible
only in the allowlisting side's logs).

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
| Client → Relay | `REG <room>\n` | Register in room (step 1 of cookie handshake) |
| Relay → Client | `REGD <room> <cookie>\n` | Cookie challenge (32 hex chars, 16 random bytes) |
| Client → Relay | `REG <room> <cookie>\n` | Echo cookie to prove return routability |
| Relay → Client | `REGD <room> OK <addr>\n` | Registration confirmed with external address |
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
 16      32    HMAC-SHA256(confirmKey, sortedPoints || marshal(N)
                 || "qvole-spake2-confirm-sender" || senderFingerprint
                 || "qvole-spake2-confirm-receiver" || receiverFingerprint
                 || nonce || "qvole-spake2-confirm")
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

- `ACCEPT true` or `ACCEPT false`: whether the peer is willing to accept tunnel
  requests (has an `-aL`/`-aF` allowlist or the `-a` allow-all flag). If `false`,
  the other side skips binding its own `-L` listeners (the declaring peer will
  not honor them) and logs a warning. If `true`, the receiving side still
  filters each request against its own allowlist: peer `-R` requests need a
  matching `-aL` listen entry, peer `-L` requests a matching `-aF` forward entry
  (see the control stream description above).
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
| Exit on | Either stream closes (5 s half-close grace for the other direction) | Either stream closes (5 s half-close grace for the other direction) |

### Exec Mode (`qvole exec`)

| Aspect | `--cmd` side | Peer side (no `--cmd`) |
|--------|-------------|------------------------|
| Stream initiator | Always opens stream | Always accepts stream |
| Process | Direct command execution | Plain pipe (stdin/stdout) |
| Exit code | Propagated to peer | Exits with command's code |

### Tunnel Mode (`qvole tunnel`)

| Aspect | Side with `-L`/`-R` | Side without requests |
|--------|---------------------|-------------------|
| Control stream | Opened by client, accepted by server | Opened by client, accepted by server |
| `-L` listener | TCP listen, opens QUIC streams | Accepts QUIC streams, dials TCP |
| `-R` listener | Accepts QUIC streams, dials TCP | TCP listen, opens QUIC streams |

---

## 5. Relay Constraints

The relay imposes hard limits that peers rely on for brute-force resistance.
A protocol implementer must not exceed these:

| Constraint | Value | Purpose |
|---|---|---|
| Max message rate per client | 10 msg/s | Online brute-force protection |
| Client TTL (REG renewal) | 1 min | Stale client eviction |
| Room TTL when empty | 1 min | Garbage collection |
| Hard client cap per room | 20 (default) | Abuse guard |
| Max rooms | 10,000 | Relay capacity |
| Max rooms per IP | 10 | Single-host DoS mitigation |
| Max UDP datagram length | 1400 bytes | Safe payload ceiling; avoids fragmentation across Ethernet (1500 MTU minus IP+UDP headers) and accommodates tunnel overhead from IPv6, VPNs, and PPPoE without relying on path MTU discovery, which is unreliable for UDP |
| Room name max length | 64 chars | Validation |

Messages are fire-and-forget: there are no message IDs, no
acknowledgements, and no ordering guarantees. Peers retransmit continuously
to overcome packet loss (see §2).

---

## 6. Versioning

Wire compatibility is gated **only** by the ALPN string, which is derived from
the protocol version, not the software version. Peers negotiate the protocol
version via TLS ALPN; the relay is unaware of versioning.

**ALPN string:** `"qvole-v<MAJOR>.<MINOR>"` (e.g. `"qvole-v0.2"`).

The `ProtocolVersion` constant (`internal/engine/transport.go`) is
hand-maintained and bumped **only** when the wire format changes: SPAKE2 layout,
relay message format, certificate algorithm, domain separation strings, or the
ALPN string itself. Peers that don't agree on `MAJOR.MINOR` fail during the TLS
handshake with an ALPN mismatch before any application data flows.

| Protocol bump | Example | Meaning |
|---------------|---------|----------|
| Patch | `0.1 → 0.1.1` | Wire-compatible: new subcommands, bugfixes, internal refactors, default tuning, reinterpreted option semantics |
| Minor | `0.1 → 0.2` | Wire-breaking: SPAKE2 layout change, relay message format, certificate algorithm, domain separation strings, or ALPN change |
| Major | `0.1 → 1.0` | Protocol redesign that breaks ALPN compatibility with all prior versions |

Software versioning is independent of wire compatibility: any software release
that leaves `ProtocolVersion` (and therefore the ALPN string) unchanged remains
wire-compatible with all prior software releases using the same protocol
version. For example, software `0.1.4 → 0.2.0` kept the wire protocol at `0.1`
(ALPN `"qvole-v0.1"`) while reinterpreting the tunnel-config `ACCEPT` semantics
and changing who opens the control stream (the client, regardless of request
count). Connections therefore still establish between `0.2.0` and `0.1.x`
peers, but tunnel sessions assume both sides understand the `0.2.0` exchange
model; pipe and exec modes remain interoperable with `0.1.x`.

The software version displayed by `qvole --version` is the `version` variable in
`cmd/qvole/main.go` and is set independently of `ProtocolVersion`.

---

## 7. Known Limitations

### 7.1 Locally opened streams are announced by the first byte or FIN

QUIC has no "open stream" frame. A stream becomes visible to the peer only when
a frame naming its stream ID arrives, which in practice is a STREAM frame.
quic-go's `OpenStreamSync` allocates a stream ID locally and returns a handle
without sending anything, so a locally opened stream is invisible until either
data is written or the send side is closed:

| Opener does | Peer's `AcceptStream` |
|---|---|
| `OpenStreamSync`, no write | blocks |
| `OpenStreamSync`, zero-byte `Write` | blocks |
| `OpenStreamSync`, `Close` (FIN) | returns |

A zero-byte write does not announce the stream. This is quic-go behavior, not a
QUIC requirement; other stacks may differ.

Tunnel data streams are unaffected because the initiator always writes the
2-byte spec index immediately after opening (section 3.5), and the control
stream writes its config on open. `pipe` and `exec` carry raw unframed
payloads, so the initiator stays silent until stdin or the command produces its
first byte.

### 7.2 Consequences for pipe and exec

- **Exec:** the `--cmd` side opens the stream and runs the command with its
  stdout bound to the stream. A command that blocks on stdin before producing
  any output (`cat`, a bare `read`, an interactive shell with no prompt) is
  never announced, so the peer never returns from `AcceptStream` and never sends
  the stdin that would unblock the command. This is a mutual deadlock, not a
  delay. With the default 5 s keepalive and 2 minute idle timeout, the
  connection does not time out, so the session hangs until interrupted or the
  connection is lost.
- **Pipe:** the peer whose stdin is not a terminal opens a stream. If that stdin
  stays open and silent (a FIFO with no writer, a `tail -f` with no new output)
  and the other peer has nothing to send either, both peers block in
  `AcceptStream`. `pipe < /dev/null` is fine because EOF closes the send side
  immediately, and a file with data is fine because the first byte announces
  the stream.

### 7.3 Workaround and fix

Ensure at least one peer sends a byte or closes its send direction. For exec,
use a command that writes before it reads (most do); avoid `cat` and a bare
`read`. No client flag changes this behavior.

A real fix writes a small application-level header immediately after
`OpenStreamSync`, as the tunnel data stream already does, and strips it on read.
Because pipe and exec are currently wire-compatible with `0.1.x` peers over
ALPN `qvole-v0.2` (section 6), adding a header is wire-breaking and requires
either a protocol version bump or an explicit capability negotiated during the
handshake.
