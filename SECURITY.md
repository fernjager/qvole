# Security

For the wire-level protocol description, payload layouts, and relay message
formats, see [PROTOCOL.md](./PROTOCOL.md).

For privacy policy and terms of service covering the public relay, see
[PRIVACY.md](./PRIVACY.md).

## Security Model

`qvole`'s security rests on a cryptographic handoff from a low-entropy shared code to a
strong 256-bit TLS 1.3 session, with the relay trusted only for rendezvous over UDP.

The design is motivated by the reality that even the most heavily audited software is not
immune to critical vulnerabilities. OpenSSH, for example, had an unauthenticated remote
code execution (`regreSSHion`, [CVE-2024-6387](https://www.cve.org/CVERecord?id=CVE-2024-6387)).
The xz backdoor ([CVE-2024-3094](https://www.cve.org/CVERecord?id=CVE-2024-3094)), a supply
chain attack, nearly shipped in major Linux distributions. Rather than rely on every exposed
service being patched, `qvole` binds services to localhost and leaves zero inbound ports,
removing the attack surface entirely.

| Layer | Mechanism | Protects Against | Limitation |
|---|---|---|---|
| **Code** | Random 52-bit `"NNNN-word-word-word"` | Online guessing, pre-shared secret distribution | 52 bits; relay rate-limiting needed |
| **SPAKE2 PAKE** | Password-Authenticated Key Exchange over P-256, dual generators M+N | MITM without the code; offline dictionary attacks on relay traffic | No forward secrecy for SPAKE2 keys themselves |
| **Confirm HMAC** | `HMAC-SHA256(confirmKey, sortedPoints ∥ Ngen ∥ peerFingerprint ∥ nonce ∥ context)` | Impersonation by a peer who only observed the blinded exchange | Bound to SPAKE2 session, not reused |
| **Address encryption** | `AES-256-GCM(encKey, aad=sortedPoints, addr)` | Relay learning peer UDP addresses | Address padding is fixed-length, not variable |
| **QUIC/TLS 1.3** | Ephemeral self-signed ECDSA P-256 certs + pinned SHA-256 fingerprints | MITM on the direct UDP path; eavesdropping on data | Certificates are self-signed; no CA binding |
| **Fingerprint binding** | Fingerprint exchanged inside SPAKE2 payload | Downgrade / cert swap attacks | Requires SPAKE2 integrity for binding |

```
                       ┌──────────────────────────┐
                       │       Code (52-bit)      │  shared secret
                       └────────────┬─────────────┘
                                    │
                       ┌────────────▼─────────────┐
                       │    SPAKE2 PAKE (P-256)   │  mutual auth + key derivation
                       │   → confirmKey + encKey  │
                       └────────────┬─────────────┘
                                    │
            ┌───────────────────────┼───────────────────────┐
            │                       │                       │
   ┌────────▼────────┐    ┌─────────▼─────────┐    ┌────────▼────────┐
   │  Confirm HMAC   │    │  AES-256-GCM enc  │    │ Cert fingerprint│
   │  (peer proof)   │    │  (peer addresses) │    │ (binding to TLS)│
   └────────┬────────┘    └─────────┬─────────┘    └────────┬────────┘
            │                       │                       │
            └───────────────────────┼───────────────────────┘
                                    │
                       ┌────────────▼─────────────┐
                       │   TLS 1.3 + QUIC (P2P)   │  data confidentiality +
                       │   pinned self-signed cert│  integrity + fwd secrecy
                       └──────────────────────────┘
```

### Key Property

SPAKE2-derived keys protect the certificate fingerprint exchange and encrypt peer
addresses. The certificate fingerprint authenticates the QUIC/TLS handshake. This
creates a cryptographic handoff from a low-entropy shared code to a strong 256-bit
TLS session, no PKI, no pre-shared keys on disk, no third-party trust beyond the
relay for UDP rendezvous.

The handoff from relay-mediated SPAKE2 to direct QUIC completes in seconds,
limiting the window for online brute-force or relay-traffic-recording attacks
against the low-entropy code to the duration of the SPAKE2 exchange. An attacker
who captures relay traffic after the handoff has no opportunity to recover the
code, and the SPAKE2 blinded points are indistinguishable from random.

**The relay is untrusted.** It can observe encrypted SPAKE2 points (indistinguishable
from random), control message delivery timing, and refuse service. It cannot derive
the session key, decrypt traffic, or impersonate a peer. The relay sees client IPs,
room names, and message timing; all encrypted SPAKE2 payloads remain opaque.

### Key Domain Separation

Every cryptographic context uses a unique domain-separated info string to
prevent cross-protocol attacks where a key, hash, or HMAC computed for one
purpose could be reused in another context:

| Context | Info String | Usage |
|---------|------------|-------|
| Password → scalar | `"qvole-spake2-pw:"` | Password-to-scalar derivation (PBKDF2 salt + domain prefix) |
| SPAKE2 generator M | `"qvole-spake2-M-v1"` | Custom generator for blinding |
| SPAKE2 generator N | `"qvole-spake2-N-v1"` | Second generator for domain separation in HKDF salt and confirm HMAC |
| Session key derivation | `"qvole-spake2-session"` | HKDF key material extraction |
| Confirm HMAC | `"qvole-spake2-confirm"` | HMAC authentication context (also includes peer cert fingerprint) |
| Room from user code | `"qvole-nameplate:"` | SHA-256 prefix for relay room |

### Entropy Budget

| Component | Entropy | Source |
|-----------|---------|--------|
| Generated code | ~52 bits | 10,000 × 7766³ |
| Ephemeral scalar r | 256 bits | `crypto/rand` |
| ECDSA P-256 key | 256 bits | `crypto/rand` |
| Confirm nonce | 128 bits | `crypto/rand` |
| GCM nonce (address) | 96 bits | `crypto/rand` |
| HKDF output | 512 bits (64 bytes) | HKDF-SHA256 |

---

## Threat Model

**Attacker capabilities:**
- Full control of the relay (packet inspection, drop, reorder, inject, delay)
- Passive eavesdropping on the direct UDP path
- Active MITM on the direct path (if no NAT firewall)
- Network-level traffic analysis and timing correlation
- May run their own `qvole` client in the same room
- May record all relay and P2P traffic indefinitely
- Does NOT know the shared code (brute-force only)
- Does NOT have access to the peer's private key memory
- Does NOT have a cryptographic break of P-256, SHA-256, AES-256-GCM, HKDF, HMAC, or TLS 1.3

### Attacker Tiers

| Tier | Capability | Examples |
|---|---|---|
| **Passive Observer** | Watches relay traffic; cannot inject or drop | Network tap between peer and relay |
| **Active Relay Operator** | Drops, delays, reorders, injects `MSGD`; controls the relay binary | Malicious relay deployment |
| **Active P2P MITM** | Intercepts and modifies direct UDP between peers | On-path between NATs (rare) |
| **Post-Compromise** | Later learns the code after recording all traffic | Code leaked weeks after a session |

The relay is the primary attack surface because it sees all pre-QUIC traffic. The
direct path is protected by TLS 1.3 with pinned fingerprints, which requires the
code to subvert.

### Trust Boundaries

```
Peer A ──(SPAKE2 over relay)──► Relay (untrusted) ◄──(SPAKE2 over relay)── Peer B
   │                                                                         │
   └───────────(QUIC/TLS 1.3, pinned certs ──────────────────────────────────┘
                     - relay sees nothing -
```

---

## Protocol Security Analysis

### 1. Code-Based Attacks

#### Online Brute-Force

The code has ~52 bits of entropy. An attacker who knows the relay address can send
`REG` messages with guessed codes and observe whether a second peer is registered in
the same room (by detecting a `MSGD` reply with a valid-looking SPAKE2 point).

**Mitigation:** The relay rate-limits per client to 10 msg/s. At that rate,
exhaustively searching 2⁵² codes would take ~10¹³ years. Rate limits are enforced
in the relay's hot path and cannot be bypassed by a single UDP source.

**Residual risk:** The default 2-clients-per-room limit (tunable via
`QVOLE_RELAY_MAX_CLIENTS`) caps the room to two peers, so a distributed attack
cannot exceed a combined 20 msg/s toward a single target room (2 clients ×
10 msg/s). An attacker who knows the 4-digit prefix (e.g. from overhearing it)
still faces the remaining 40-bit word space, which takes ~1.7 million years to
exhaust at 20 msg/s. The 100-rooms-per-IP limit further restricts the number of
rooms an attacker can simultaneously probe from a single source.

#### Pre-image Attack on Code Generation

If the attacker steals the code in transit or via side channel, all security is
lost; the code is the sole shared secret.

**Mitigation:** Out of band. The `GenerateCode` function uses `crypto/rand` with
no seeding weaknesses. Codes must be distributed through a trusted channel (e.g.
encrypted messaging, QR code in person, voice call).

#### Room Collision from Code Prefix Overlap

Two different codes may map to the same relay room: generated codes use the
4-digit prefix directly; user-provided codes use `SHA-256("qvole-nameplate:" + code)[:4]`
as an 8-hex-char room name. With ~2¹⁶ possible rooms from the hex derivation,
room collisions occur with probability ~1/65536 per code pair.

**Mitigation:** Room collision does not compromise security. Peers with different
codes compute different `passwordScalar` values, producing different SPAKE2 keys.
The confirmation HMAC fails, and peers disconnect. The collision only allows a
relay to observe cross-room traffic patterns; conversely, the traffic mixing
makes it impossible for a relay observer to attribute individual MSGD messages
to a specific peer pair, providing cover traffic at no cost.

---

### 2. Relay-Level Attacks

#### Relay Observes Traffic Patterns

Even though SPAKE2 blinded points are computationally indistinguishable from random,
an observer can see:
- `REG` / `REGD` messages → two peers joined the same room
- `MSG` / `MSGD` messages → timing, label (`spake2` vs `confirm`), and hex payload size
- Subsequent silence → when protocol phases complete

**Mitigation:**
- Both sides can be sources of the first spake2 point, so an observer cannot
  determine which peer initiated the exchange.

**Residual risk:** The protocol's entry point (the initial `REG` datagram) is
detectable since it has a fixed ASCII prefix. A relay observer can identify
the start of an exchange but learns nothing about the code or the peer's
identity.

A global passive adversary correlating packet timing across the relay and the
direct UDP path could still determine which two peers connected.

#### Relay Denial of Service

The relay can drop, delay, or reorder messages between peers.

**Mitigation:** Both SPAKE2 points and confirms are retransmitted every 2 s
continuously, independent of state. Each received point initiates its own
confirm retry cycle, and incoming confirms are tried against all known
candidate points. If no confirm matches within the deadline (5 min exchange
deadline), the peer exits. The protocol makes no guarantees about DoS
resilience; the relay is a single point of failure for rendezvous.

**Mitigations in the relay implementation:**
- Non-blocking packet dispatch prevents the reader goroutine from stalling under load;
  packets are dropped when worker queues fill.
- Per-IP room cap (100) prevents a single host from exhausting all relay rooms.
- Stale-room eviction before rejecting at capacity ensures active rooms can always
  be created even when the relay is near its 10,000-room limit.
- REG rate limiter is sharded across 16 mutexes, eliminating the single global
  bottleneck.
- Message queuing has been removed entirely; the relay never buffers messages
  between clients, eliminating replay amplification and memory pressure vectors.

#### Relay Injects Fake Messages

The relay could forge a `MSGD` to a peer, pretending to be the other peer.

**Mitigation:** The SPAKE2 confirmation HMAC proves knowledge of the confirm key,
which itself requires knowledge of the password scalar. Without the code, the relay
cannot compute a valid HMAC that `subtle.ConstantTimeCompare` would accept. The
relay also cannot decrypt the encrypted address payload (AES-256-GCM under the
encryption key, with sorted blinding points as AAD). Client-side caps (50 candidates,
200 buffered confirms) bound the memory and CPU cost of injected messages.

#### Relay Performs a Room Collision Attack

The relay could deliberately route multiple distinct rooms to the same internal
room, causing unintended handshakes between peers with different codes.

**Mitigation:** The room name is derived from the code prefix and validated by the
relay (`relay/room.go`). An attacker in the wrong room will compute a different
`passwordScalar` and the SPAKE2 handshake will produce a different session key;
the confirmation HMAC will fail and the peers disconnect. The code mismatch is
detected at the cryptographic layer, not the relay layer.

#### UDP Source Spoofing in Relay Responses

On a shared L2 network, an attacker could spoof the relay's source address and
inject hex payloads into the SPAKE2 exchange. The client previously filtered
messages by relay IP:port, which is trivially bypassed by source spoofing on
shared networks (cloud VPS neighbors, Docker bridge networks, shared Wi-Fi).

**Mitigation:** The exchange phase now uses a **connected UDP socket**
(`net.DialUDP`) to the relay. The kernel filters all incoming packets at the
socket level: only datagrams from the connected peer (the relay's IP:port)
are delivered to the application. Spoofed packets from other sources are
silently discarded by the kernel before reaching `ReadFromUDP`. After the
exchange completes, the connected socket is closed and a wildcard socket is
re-bound on the same local port for hole punching and QUIC.

**Residual risk:** The connected-socket defense is kernel-level and does not
rely on application-layer IP comparison. An attacker who can compromise the
kernel or the relay itself can still inject messages, but the SPAKE2 and
confirm HMACs prevent impersonation without the code. Client-side caps (50
candidates, 200 buffered confirms) continue to bound resource usage.

---

### 3. SPAKE2 Cryptographic Attacks

#### Offline Dictionary Attack

An attacker who captures the SPAKE2 blinded points (from relay traffic or direct
path) can try to guess the code offline: for each candidate code, compute
`M·passwordScalar`, subtract from the blinded point, and check if the result is a
valid public key.

**Mitigation:** SPAKE2 is PAKE-secure: without the password, the blinded points
are indistinguishable from random group elements. No offline verification oracle
exists. The attacker would need to solve the Computational Diffie-Hellman problem
to verify a guess. This property holds even though P-256 is a standard curve (not
a symmetric-primitive-based PAKE like OPAQUE).

#### Small-Subgroup Attack

If the peer's blinded point is not on P-256, computing the shared secret may leak
information about `passwordScalar` or the private blinding scalar.

**Mitigation:** The received point is validated with `IsOnCurve()` before any
computation (`spake2/spake2.go`). P-256 is a prime-order curve with no cofactor;
successful on-curve validation guarantees the point is in the correct subgroup.

#### Reflection Attack

An attacker who observes one side's blinded point could replay it to the same peer,
making them derive a shared secret with themselves.

**Mitigation:** The role is determined by comparing the M-blinded points with
`bytes.Compare(myPointM, peerPointM) > 0`. If both points are identical
(reflection), neither peer would be the server and neither would be the client;
the QUIC `Listen`/`Dial` pair would deadlock. Additionally, the confirmation HMAC
includes both points sorted, peer fingerprint, N generator, nonce, and context,
so a replayed point produces a different HMAC input.

#### Shared Secret Padding Attack

`big.Int.Bytes()` strips leading zeros from the shared X-coordinate. Without
zero-padding to 32 bytes before HKDF extraction, both sides would derive different
key material whenever the shared coordinate has a leading zero byte (~0.4% of
connections).

**Mitigation:** The shared coordinate is zero-padded to exactly 32 bytes before
HKDF (`spake2/spake2.go`). Both sides perform identical padding.

#### Zero Password Scalar

If `SHA-256("qvole-spake2-pw:" + code) mod N` produces zero, the blinded point
would be `G·r` with no password contribution, breaking SPAKE2's security.

**Mitigation:** The password scalar is clamped to 1 if the hash produces zero
(`spake2/spake2.go`).

---

### 4. Confirmation & Address Encryption Attacks

#### Replay of Confirm Message

An attacker who captures a valid confirm message could replay it in a later session
to appear authenticated.

**Mitigation:** The confirm HMAC is bound to the specific session's `confirmKey`
(derived from both blinding scalars). Replaying the same bytes in a new session
produces a different expected HMAC because the confirm key is different. The HMAC
is verified with `subtle.ConstantTimeCompare` to prevent timing side channels.

#### AES-256-GCM Nonce Reuse

If the SPAKE2 handshake produced the same encryption key twice (extremely
improbable: 2²⁵⁶ space, fresh blinding scalars per connection) and the GCM nonce
collided, it would break confidentiality of the encrypted address.

**Mitigation:** Each connection generates fresh ephemeral blinding scalars via
`crypto/rand`. The probability of key collision is negligible (~2⁻¹²⁸ by birthday
bound). The GCM nonce is also generated via `crypto/rand` (12 bytes per call),
making nonce reuse astronomically unlikely.

#### Chosen-Ciphertext Attack on Encrypted Address

An attacker could inject a modified ciphertext to the peer, who would decrypt and
attempt to hole-punch an attacker-chosen address.

**Mitigation:** AES-256-GCM provides authenticated encryption. Any modification to
the ciphertext (bit flips, truncation, extension) causes a decryption failure. The
peer does not proceed with an unauthenticated address.

#### Fixed-Length Confirm Payload

The confirm payload has a fixed 160-byte size regardless of peer address type
(IPv4 or IPv6). See **Design Tradeoffs: Fixed Confirm Payload Size** below for
details and rationale.

---

### 5. Hole Punching Attacks

The hole punching mechanism is described in [PROTOCOL.md](./PROTOCOL.md) §2 Phase 3.
This section analyses the security properties.

#### Third-Party Denial of Service

An attacker who learns the peer's UDP address (e.g. by compromising the relay or
through traffic analysis) could flood the peer's port with garbage packets.

**Mitigation:** The hole-punch listener accepts any packet from the expected peer
address as success, but the QUIC connection that follows requires a valid TLS 1.3
handshake with the pinned fingerprint. Unsolicited data cannot establish a QUIC
session; `quic.Dial` will fail if the peer's TLS fingerprint does not match the
one obtained from SPAKE2. DoS amplification is limited by UDP packet size.

#### Hole Punch Payload Unauthenticated

Hole punch packets contain random bytes from `crypto/rand` and are not
cryptographically bound to the QUIC session. An attacker who can spoof the peer's
UDP address can trigger the success channel.

**Mitigation:** Triggering the hole punch success channel only causes the QUIC
handshake to begin early. The attacker still cannot complete the TLS 1.3 handshake
without the correct pinned certificate fingerprint, which requires knowledge of the
code to obtain from the SPAKE2 exchange.

#### IP-Only Port Matching

`listenForPunch` matches incoming packets by IP only, not IP + port. This is
necessary for symmetric NAT: the peer may send punch packets from a different
source port than the one reported by the relay, since symmetric NAT assigns a
new mapping per destination. On success, the observed address replaces the
relay-reported address for the QUIC handshake.

**Attack surface:** On a shared L2 network (coffee shop Wi-Fi, cloud VLAN,
Docker bridge), any host can spoof the peer's source IP with an arbitrary
port and win the hole punch race. `listenForPunch` exits on the first
matching packet and never re-checks, so a single spoofed datagram within the
10 s hole punch window locks out the legitimate peer. The QUIC handshake then
targets the attacker's chosen port, which either reaches the wrong service on
the peer's host or goes nowhere, causing the connection to time out.

**Mitigation:** TLS fingerprint pinning prevents the attacker from completing
the QUIC handshake even if they win the race, so data confidentiality and
integrity are preserved. The impact is limited to denial of service: the
legitimate peer cannot establish a connection during the compromised session.
Reconnecting with a new code resets the handshake. On the open internet, UDP
source-IP spoofing is unreliable (filtered by most ISPs and cloud providers),
limiting this vector to shared-network scenarios.

#### NAT Rebinding

If a NAT rebinds the peer's external mapping between hole punching and QUIC, the
direct connection fails.

**Mitigation:** QUIC uses connection IDs that survive address changes. `quic-go`'s
`KeepAlivePeriod` (2 s) maintains NAT state. If the address changes, QUIC can
re-establish on the same connection ID. Hole punching retries with ramped
intervals (50 ms → 100 ms → 200 ms) and payload sizes (5 B → 50 B → 100 B → 200 B)
to increase the chance of opening a pin hole. On success or timeout, a helper goroutine
sends keep-alive packets (`0x01`) to the peer every 50 ms for 3 s to hold the NAT
pin-hole open while the QUIC handshake starts. If hole punching fails (10 s timeout by default),
QUIC attempts the connection anyway; the OS may already have opened a path from
relay traffic.

---

### 6. QUIC / TLS Attacks

The QUIC connection setup and TLS configuration is described in
[PROTOCOL.md](./PROTOCOL.md) §2 Phase 4. This section analyses the security properties.

#### MITM on Direct Path

An active attacker between the peers could attempt a TLS MITM by presenting their
own certificate.

**Mitigation:** The `VerifyPeerCertificate` callback compares `sha256(peerDER)`
against the fingerprint obtained from the SPAKE2 exchange. The attacker does not
know the code and cannot compute the correct fingerprint. `InsecureSkipVerify: true`
disables CA-chain validation but does not weaken this; the custom verification is
strictly stronger (it pins to an exact key, not a CA hierarchy).

> **Implementation note:** Both server and client TLS configs share
> `InsecureSkipVerify: true` via `baseTLSConfig`. Without this, quic-go's server
> would verify the peer's self-signed certificate against the system CA pool and
> reject the handshake *before* the `VerifyPeerCertificate` callback fires,
> breaking all server-role connections.

#### TLS 1.3 Forward Secrecy (Data Traffic)

TLS 1.3 uses ephemeral (EC)DHE key exchange. The TLS session keys are derived
from ephemeral key material exchanged during the handshake, not from the
certificate keys. This has an important consequence for post-compromise analysis:

- **If the code is compromised after a session:** An attacker who recorded relay
  traffic can re-derive the SPAKE2 session keys and decrypt the confirm payload.
  This reveals peer UDP addresses and certificate fingerprints, but **not** the
  QUIC/TLS session keys or data traffic. The TLS 1.3 ephemeral key exchange is
  independent of both the SPAKE2 keys and the certificate keys.
- **If the code is compromised before a session:** The attacker can mount an active
  MITM on future sessions by completing SPAKE2, generating their own certificate,
  and presenting their own fingerprint, though this requires both relay access
  (to intercept SPAKE2) and P2P access (to intercept TLS).

#### Downgrade Attack

An attacker could try to force TLS 1.2 or a weak cipher suite.

**Mitigation:** `quic-go` uses `tls.Config` with `MinVersion: tls.VersionTLS13`
(implicitly via quic-go's defaults). TLS 1.3 removes all backward-compatible
downgrade mechanisms and cipher-suite negotiation is minimal. No downgrade below
TLS 1.3 is possible.

#### Certificate Spoofing via Compromised RNG

If `crypto/rand` produces a predictable ECDSA key, an attacker could generate the
same certificate.

**Mitigation:** The ECDSA key is generated per-connection via `crypto/rand`. If the
RNG is compromised, the TLS layer fails. This is a general systems security concern,
not specific to `qvole`. The fingerprints would still need to match the SPAKE2
exchange; the attacker would also need the code.

#### Cross-Protocol Connection Prevention

The QUIC listener uses ALPN `"qvole-v" + ProtocolVersion` (currently `"qvole-v0.1"`).
A `quic-go` client or server with a mismatched ALPN is rejected before the TLS
handshake begins. This prevents accidental or malicious cross-protocol QUIC
connections.

#### No SAN (Subject Alternative Name)

The self-signed certificates omit Subject Alternative Names, which some TLS
implementations require.

**Rationale:** Since `InsecureSkipVerify: true` disables standard verification,
SAN is not checked. The security relies entirely on `VerifyPeerCertificate`'s
fingerprint comparison. Adding a SAN would have no security benefit.

---

### 7. Tunnel Subcommand Attacks

The tunnel protocol is described in [PROTOCOL.md](./PROTOCOL.md) §2 Phase 5 (Tunnel
Mode) and §3.4-3.5. This section analyses the security properties.

#### TCP Connection Hijack via Spec-Index Spoofing

If an attacker opens a QUIC stream to the forward listener, they could send a
crafted spec index to divert traffic to an unintended target.

**Mitigation:** Only one peer opens QUIC streams (the side that receives TCP
connections). The control stream is sent once, authenticated by the QUIC
connection (which is pinned to the SPAKE2 session). An external attacker cannot
open a QUIC stream without passing the fingerprint check. Spec indices are
validated against known spec counts; out-of-range indices silently close the
stream.

#### Local Listener Exposure

The `-L` listener binds to `0.0.0.0` or a user-specified address. On a multi-tenant
machine, other local processes could connect to the forward listener and tunnel
through.

**Mitigation:** The user controls the bind address (`-L 127.0.0.1:8080:host:port`
restricts to loopback). The forwarded connection is cryptographically protected to
the peer, but the local TCP connection receives no additional authentication; this
is equivalent to SSH `-L` semantics. The user is responsible for bind-address
restrictions.

#### Remote Forward Target Confusion

With `-R`, the peer specifies the target address. A malicious peer could request
forwarding to an internal service the listener did not intend to expose.

**Mitigation:** Same as SSH `-R`; the side running the `-R` flag controls which
target address the peer forwards to. The peer's config is sent over the encrypted
QUIC control stream, so an external attacker cannot modify it. The receiving side
should only use `-R` with trusted peers. The `--allow-tunnel` flag (default off)
must be explicitly passed to accept incoming tunnel connections.

#### Control Stream Exchange

The peer with tunnel requests opens the control stream, writes its requests, then
reads the peer's. The peer without requests accepts, reads first, then writes
its own requests back on the same stream.

**Mitigation:** Both directions are on the same QUIC connection, authenticated
by the pinned certificate. An attacker who cannot complete the QUIC handshake
cannot participate in the control stream exchange. Spec counts are capped at 100
per side.

#### Tunnel Stream Resource Exhaustion

A malicious peer could open QUIC streams and never write the 2-byte spec-index
header, consuming goroutines and guard slots until the forward stream limit
(200 by default) is exhausted, blocking all subsequent tunnel connections.

**Mitigation:** Accepted tunnel streams have a read deadline of 15 s before the
header read (`HandleTunnelStream`). If the peer does not send the header within
that window, the stream is closed and the guard slot is released. The control
stream (config exchange) has a 30 s read deadline after `AcceptStream`. These
timeouts apply only to protocol-driven reads (machine-to-machine), not to
interactive data streams (pipe, exec) or tunnel data copy, where human-paced
or arbitrary TCP traffic may pause indefinitely.

---

### 8. Side-Channel Attacks

#### Timing Side Channel on HMAC Verification

If the HMAC comparison were not constant-time, an attacker could iteratively
refine a guessed HMAC by measuring response timing.

**Mitigation:** `crypto/subtle.ConstantTimeCompare` is used in `spake2/spake2.go` for the
confirm HMAC verification and in `internal/engine/transport.go` for the certificate fingerprint
comparison. The comparison runs in time proportional to the hash length regardless
of the match position.

#### Timing Side Channel on IsOnCurve

If the point validation (`IsOnCurve`) had data-dependent timing, it could leak
bits of the received point.

**Mitigation:** The Go `crypto/elliptic` implementation's `IsOnCurve` uses
constant-time arithmetic for P-256 (as of Go 1.24). The `ScalarMult` used
against custom generators M and N in SPAKE2 (`generatorScalar`, `ComputeShared`)
is not fully constant-time; however, these operations use ephemeral single-use
scalars, limiting the risk to local cache-timing attacks.

#### Lexicographic Role Comparison

`bytes.Compare(myPointM, peerPointM) > 0` is not constant-time, but it operates on
public data (the blinded points have already been transmitted over the relay).
No secret is leaked.

#### Buffer Zeroing and Compiler Optimization

`internal/engine/pool.go` zeros buffers on return to the `sync.Pool`. A sufficiently aggressive
compiler could elide this dead store since the buffer is never read after zeroing.

**Mitigation:** The `ZeroBytes` function in `spake2/spake2.go` is marked
`//go:noinline`, preventing the compiler from inlining it and optimizing away the
writes. Additionally, `sync.Pool.Put` retains a reference to the buffer through an
interface conversion, making it impossible for the compiler to prove the store is
dead. This is defense-in-depth; data within the same process is already accessible
to any goroutine.

---

### 9. Entropy and Randomness

#### Insufficient Entropy at Startup

If `crypto/rand` blocks (e.g. on a headless server with no entropy source), the
ECDSA key generation and SPAKE2 blinding scalar generation will hang.

**Mitigation:** Go's `crypto/rand` reads from `/dev/urandom` on Linux/macOS, which
never blocks. On Linux, it is backed by `getrandom(2)` with `GRND_NONBLOCK` in
Go's runtime. Entropy depletion is not a practical concern on modern kernels.

#### Code Generation Rejection Sampling

`randInt` in `internal/util/code.go` uses rejection sampling to eliminate modulo bias when
selecting from the word list (7766 words) and digit range (0-9999). A uniformly
random uint16 is drawn and rejected if it falls outside the largest multiple of
the range that fits in 65536. For 7766, the rejection probability is ~5.0%,
potentially requiring multiple `crypto/rand` reads.

---

## Design Tradeoffs

### SPAKE2 vs OPAQUE / Other PAKEs

| Property | SPAKE2 | OPAQUE | CPace | SRP |
|---|---|---|---|---|
| Requires server-side password storage | No | Yes (OPRF key) | No | Yes (verifier) |
| Round trips (after setup) | 1 | 2 | 1 | 1 |
| Standard library implementation | Yes (Go `crypto/elliptic`) | No | No | No |
| Forward secrecy for session keys | No (PAKE-level) | Yes | No | No |
| Quantum-resistant variant exists | No | Yes (draft) | No | No |

**Rationale for SPAKE2:** `qvole` has no persistent server; both peers share the
code ephemerally. OPAQUE requires the server to hold a salted OPRF key, requiring
persistent state and key management. SPAKE2 works with a pure shared secret and
uses only operations available in Go's `crypto/elliptic`. The lack of forward
secrecy at the PAKE level is mitigated by TLS 1.3 for data traffic (see
Limitations section).

### P-256 vs X25519 / Ed25519

`qvole` uses NIST P-256 everywhere: SPAKE2, ECDSA certificates, and TLS 1.3 key
exchange.

| Property | NIST P-256 | X25519 |
|---|---|---|
| SPAKE2 point operations (`Add`, `ScalarMult` with custom generator) | Available in `crypto/elliptic` | Not available in stdlib (only `crypto/ecdh` ECDH) |
| Constant-time in Go stdlib | Yes (as of Go 1.18+) | Yes (via `x/crypto`) |
| No cofactor (prime order) | Yes | No (cofactor 8 requires clamping) |
| CA/Browser TLS support | Required | Optional (TLS 1.3) |

**Rationale:** SPAKE2 requires `curve.Add` (point addition), `curve.ScalarMult`
(arbitrary scalar × point), and `curve.IsOnCurve` (point validation). Go's
`crypto/ecdh` deliberately exposes only `PrivateKey.ECDH(PublicKey)` and forbids
raw point arithmetic. The `crypto/elliptic` package is the only path in the
standard library to these operations. P-256's prime order eliminates cofactor
concerns; validated points are always in the correct subgroup without clamping.

### `crypto/elliptic` (Deprecated) vs `filippo.io/nistec`

The `crypto/elliptic` package is deprecated since Go 1.24. SPAKE2 requires
`curve.Add`, `curve.ScalarMult`, `curve.IsOnCurve`, operations `crypto/ecdh`
does not expose.

`elliptic.P256()` internally wraps the same constant-time NIST-P256 assembly
backend used by `crypto/ecdh`, so the implementation benefits from optimized,
constant-time scalar multiplication. The remaining `big.Int` surface is a known
limitation.

**Migration path:** `filippo.io/nistec` (the public version of Go's internal
`crypto/internal/nistec`) would eliminate `big.Int` entirely. This is a
contemplated future change but adds an external dependency.

### Self-Signed Certificates vs PKI

`qvole` generates ephemeral self-signed ECDSA P-256 certificates per connection
and pins SHA-256 fingerprints (exchanged inside the SPAKE2 payload). No CA
hierarchy exists.

| Approach | Trust Anchor | Revocation | Setup Friction |
|---|---|---|---|
| Self-signed + pinning | SPAKE2 exchange | New code per session | Zero |
| Public CA (Let's Encrypt) | CA hierarchy + DNS | OCSP/CRL | Requires domain, DNS, renewal |
| Web of Trust (PGP-style) | Out-of-band key exchange | Key revocation | Manual key distribution |

**Rationale:** The code acts as the root of trust; exchanging anything else
would require an additional out-of-band step. Self-signed certificates with
fingerprint pinning avoid certificate expiration concerns (the 1-year validity
is irrelevant since fingerprints are checked, not expiry) and PKI infrastructure.

### Relay-Based Rendezvous vs STUN/TURN

| Approach | Infrastructure | Works behind symmetric NAT | Relay in data path |
|---|---|---|---|
| `qvole` relay | One UDP server | Yes (hole punch + relay traffic path) | No |
| STUN (standalone) | STUN server | No (requires full-cone or restricted-cone NAT) | No |
| TURN | TURN server | Yes (relay all traffic) | Yes |
| UPnP/NAT-PMP | None | Config-dependent | N/A |

**Rationale:** A pure STUN approach would fail on symmetric NATs. TURN
eliminates the direct P2P benefit and places the relay in the data path. `qvole`'s
relay is lightweight (UDP, in-memory state, no data forwarding beyond handshake)
and the hole-punching phase with ramped payload sizes maximizes the chance of
direct connectivity. If hole punching fails, QUIC still attempts the connection.

### Raw UDP vs QUIC for Relay Transport

The relay uses plaintext UDP rather than QUIC. The decision is pragmatic given the
relay's role as a lightweight, untrusted rendezvous:

| Property | Raw UDP (current) | QUIC |
|---|---|---|
| Per-connection state | `addr + rate limiter` | Congestion control, stream state, TLS session |
| Connection setup | None (REG packet is first message) | TLS 1.3 handshake (1,200–3,000 B) |
| Per-message overhead | 0 B (just payload) | 1–20 B (short-header datagram or stream frame) |
| Typical exchange on wire | ~2,800 B across 12 messages | Handshake alone exceeds the entire exchange |
| Relay memory per client | ~tens of bytes | ~hundreds of KB (QUIC + TLS state) |
| Maximum relay concurrency | 500k clients (50/room at configurable limits × 10k rooms) feasible | Impractical without massive memory |

**Why the overhead matters:** The relay exchange is just 6 application messages
(2 SPAKE2 + 2 confirm + 2 REG/REGD). Adding a QUIC handshake to each rendezvous
would multiply the bytes on the wire for no security gain against the relay itself.
The relay terminates QUIC and would still see all plaintext; SPAKE2 points are
already indistinguishable from random, and confirm payloads are AES-256-GCM
encrypted end-to-end with keys the relay never possesses.

**Privacy:** QUIC would hide REG/REGD/SPAKE2/confirm messages from passive network
observers (ISP, Wi-Fi snoopers), but not from the relay operator. The relay, as the
QUIC endpoint, decrypts everything. The current threat model already assumes the
relay is untrusted and can observe all traffic through it. Protecting against
passive network observers would require QUIC, but at the cost of massive per-client
state on the relay. For most deployments, network-level observers see only
random-looking payloads anyway (SPAKE2 points are computationally indistinguishable
from random, hex-encoded confirm payloads are opaque). A full privacy layer against
both the relay and network observers would require onion routing or proxy chaining,
not merely a transport upgrade.

**Reliability:** QUIC provides loss detection and retransmission. The SPAKE2
exchange already handles this at the application layer: points and confirms are
resent every 2 seconds, and REGs every 60 seconds. QUIC's reliability would be
redundant and potentially counterproductive because automatic retransmission of stale
messages would waste bandwidth when the application-level resend already covers
late-joining peers.

### Noise Protocol vs Custom Handshake

`qvole` uses SPAKE2 over the relay for authentication, then QUIC/TLS 1.3 for data.
Why not use the Noise Protocol Framework (e.g. `Noise_IK` or `Noise_NK`)?

- **Noise lacks PAKE primitives natively.** Noise's `psk` patterns use a
  pre-shared symmetric key, not a low-entropy password. Incorporating a PAKE
  into Noise requires a custom extension.
- **QUIC already provides TLS 1.3 as a transport.** Building a Noise-based
  transport would require reimplementing reliable stream multiplexing, flow
  control, and congestion control, all of which QUIC provides.
- **SPAKE2 + QUIC composes cleanly.** SPAKE2 handles mutual authentication from
  the shared code; QUIC handles the secure channel.

### Lexicographic Role Determination

Both peers independently compute `isServer = bytes.Compare(myPointM, peerPointM) > 0`.
The 65-byte uncompressed P-256 M-blinded points are compared lexicographically,
guaranteeing opposite results for non-equal points. This avoids an extra
coin-toss message or reliance on the relay to assign roles.

**Tradeoff:** The `bytes.Compare` is not constant-time, but the points
are public (already transmitted over the relay). The deterministic role assignment
means an attacker who knows the code can predict which side will be server/client
by choosing a blinding scalar that forces a specific ordering, but this provides
no advantage; both roles are equally authenticated.

### Fixed Confirm Payload Size

The confirm payload is always 160 bytes: 16 (nonce) + 32 (HMAC) + 12 (GCM nonce) +
52 (max address size, padded) + 16 (GCM tag) + 32 (random padding). The address
is null-padded to 52 bytes before encryption and trimmed after decryption. The
remaining bytes are filled with random padding, ensuring a fixed-size output that
prevents the relay from inferring address type or length from message size.

**Tradeoff:** Always sending 160 bytes wastes ~108 bytes per confirm message
(typical IPv4 address with port is ~20 chars, padded to 52 + 32 random). For a
single exchange, this is negligible. The random padding adds true uncertainty
(not just fixed-size) to the confirm payload, making traffic analysis harder
than a bare fixed-size layout.

### Single Code vs Multi-Factor

`qvole` uses a single shared code for authentication. There is no user identity,
no key file, and no second factor.

**Rationale:** The goal is zero-config setup. Adding persistent identity or
multi-factor auth would require state management and out-of-band verification,
defeating the "share a code, get a pipe" simplicity. For environments needing
stronger authentication, the code can be generated with higher entropy and
exchanged through a pre-established secure channel.

---

## Limitations

### No Forward Secrecy for Code

If the code is compromised after a session, and the attacker recorded relay
traffic:

- **Recoverable:** The SPAKE2 session keys can be re-derived from the recorded
  blinded points + code. This reveals:
  - Peer UDP addresses (decrypted from confirm payload)
  - Certificate fingerprints (extracted from spake2 payload)
  - Which code was used for a particular connection
- **Not recoverable:** The QUIC/TLS 1.3 data traffic. TLS 1.3 uses ephemeral
  (EC)DHE key exchange; the TLS session keys are independent of both the SPAKE2
  keys and the certificate private keys. The certificate fingerprints alone
  cannot decrypt TLS 1.3 traffic.

**Implication:** The code should be treated as ephemeral. Past P2P data is
protected by TLS 1.3 forward secrecy, but connection metadata (who connected
to whom, when) is not.

### No Peer Identity Persistence

Each session generates new ephemeral certificates. There is no way to recognize
a peer across sessions; each connection is independent. If a peer reconnects
with the same code, the other side cannot verify it's the same entity.

### Relay is Single Point of Failure

The relay is required for rendezvous. If it is unavailable, peers cannot discover
each other's UDP addresses or exchange SPAKE2 points. The relay is also a single
point of observation; all pre-QUIC metadata is visible to the relay operator.

### No Rekeying During Long Sessions

QUIC supports connection migration and key updates via TLS 1.3's `KeyUpdate`,
but `quic-go` does not currently expose an API to trigger rekeying. Long-running
sessions (hours/days) use the same TLS traffic keys. A future `quic-go` version
may add `Conn.KeyUpdate()` support.

### No Post-Quantum Resistance

All cryptographic primitives (P-256, ECDSA, SHA-256, AES-256-GCM, HKDF, HMAC)
are vulnerable to a large-scale quantum computer via Shor's algorithm (P-256,
ECDSA) or Grover's algorithm (SHA-256, AES-256 reducing 256-bit to 128-bit
effective security).

**Migitation path (future):** A post-quantum variant would require:
- A post-quantum PAKE (e.g., OPAQUE with Kyber, or a lattice-based PAKE)
- Post-quantum TLS 1.3 cipher suites (hybrid ECDH + Kyber)
- This is not implemented and would break compatibility with the current relay
  and peer protocol.

### Traffic Analysis Vulnerability

The relay sees message timing, sizes, and labels. A relay that also observes
direct UDP traffic (e.g., by being on the same network as the peers) can
correlate the two streams. `qvole` provides confidentiality and integrity, but
not anonymity or unlinkability.

### Plausible Deniability vs Anonymity

`qvole` provides **plausible deniability** but not **anonymity**.

| Property | What it means | Status |
|---|---|---|
| **Anonymity** | An observer cannot determine which peers communicated. | ❌ Not provided. The relay sees both peers' real IPs, room names, message timing, and protocol phases. A network observer watching both the relay path and the direct P2P path can correlate the two flows. |
| **Plausible deniability** | A peer can deny that any particular communication occurred beyond random-looking UDP packets. | ✅ Inherent. SPAKE2 blinded points are computationally indistinguishable from random. Room names are hex-encoded hashes. There is no persistent identity, no signed messages, and no session artifacts that prove *what* was communicated or *that* a specific conversation took place. |

Concretely: a relay operator can prove "peer X at IP A sent a datagram to
room R at time T", but cannot prove what the datagram contained (it is
encrypted and indistinguishable from random) or that a meaningful exchange
occurred. Any captured ciphertext could equally be random noise. This is
similar to how SSH provides a secure channel but a traffic log does not prove
what was typed; with the difference that SSH headers are structured, while
SPAKE2 payloads are not.

The distinction matters: **do not use `qvole` where peer identity must be
hidden from the relay operator.** Use it where peers want a confidential
channel and the ability to deny the contents of any captured session.

### No Multi-Party Support

The protocol supports exactly two peers per connection. Multi-party
conferencing would require either pairwise connections (N² SPAKE2 exchanges)
or a group PAKE (not implemented).

### Concurrent Handshake Collisions in Same Room

SPAKE2 blinded points carry no peer identifier beyond their content. If three
or more peers register in the same room simultaneously, their spake2 and
confirm messages are broadcast to all members. The stateless exchange handles
this gracefully: each received point becomes a separate candidate with its own
confirm retry cycle, and confirms are tried against all candidates (capped at
50 candidates and 200 buffered confirms to bound resource usage). Only the
peer sharing the correct code will produce a valid confirm HMAC.

**Workaround:** Use unique codes per pair. This is not a security issue
(incorrect session keys result in HMAC failure) but is a
usability limitation.

### Code Entropy Ceiling

The generated code has ~52 bits of entropy (10,000 × 7766³). User-provided
codes can be up to 256 characters but the entropy is only as strong as the
chosen code. 52 bits is sufficient against online brute-force with relay rate
limiting, but may be marginal against a well-resourced attacker with access
to the relay's traffic logs.

**PBKDF2 ceiling:** `PasswordToScalar` derives the scalar via PBKDF2-HMAC-SHA256
(in `spake2/spake2.go`). The 256-bit hash output is reduced modulo the curve order.
Even a 256-character random code is therefore capped at 256 bits of effective
security, the same as a 32-byte random string. Beyond 32 bytes of
cryptographic randomness, additional input length does not strengthen the
derived scalar. The 256-character maximum accommodates passphrases, which
need more characters to achieve the same entropy as random bytes.

**Mitigating factor:** The code is only exposed on the wire for the duration
of the SPAKE2 exchange (seconds). After the handoff to direct QUIC, the relay
sees no further code-derived material. An attacker must capture traffic during
the same seconds-long window to later benefit from code compromise; stale
recordings after the handoff contain no code-bound ciphertext.

### PBKDF2 Iteration Count

`PasswordToScalar` (`spake2/spake2.go`) maps the shared code
to a curve scalar using PBKDF2-HMAC-SHA256 with **600,000 iterations** by
default (tunable via `QVOLE_KDF_ITERATIONS`). Each guess costs 600,000
HMAC-SHA256 computations (~1,200,000 SHA-256 operations).

#### Time to Break: Auto-Generated Codes (~52-bit)

The effective work factor for auto-generated codes is
2^52 × 600,000 ≈ 2^71.4 total SHA-256 operations (~71 bits of effort).

Assuming ~10^6 PBKDF2-10k-iter guesses/s on a single RTX 4090, the rate at
600,000 iterations drops to ~16,700 guesses/s:

| Resources | PBKDF2 guesses/s (600k) | Time to crack (avg) |
|---|---|---|
| 1× RTX 4090 | ~16,700 | ~4,280 years |
| 10× RTX 4090 | ~167,000 | ~428 years |
| 100× RTX 4090 | ~1,670,000 | ~42.8 years |
| 1000× RTX 4090 | ~16,700,000 | ~4.28 years |

Even at the 1000-GPU scale (~4.3 years), the attacker recovers only
**connection metadata**: peer UDP addresses and certificate fingerprints.
TLS 1.3 ephemeral (EC)DHE key exchange is independent of both the SPAKE2
keys and the certificate keys; user data remains unrecoverable.

#### Why 600,000 Rounds

The SPAKE2 exchange completes in seconds (typically 5-30s, max 5 min
deadline). After the handoff to direct QUIC, code-derived keys are never
used again. The PBKDF2 amplification (52 bits → ~71 bits of effort) is
proportionate to the threat for three reasons:

1. **Bounded exposure window.** The attacker must capture relay traffic
   during the seconds-long SPAKE2 exchange. After the QUIC handoff, no
   code-derived material appears on the wire. Stale recordings of the
   direct QUIC path contain no code-bound ciphertext.
2. **Limited reward.** A successful offline crack reveals peer addresses
   and certificate fingerprints: metadata about who connected, not what
   they sent. TLS 1.3 forward secrecy ensures data traffic is never
   recoverable, even if the code and SPAKE2 session keys are fully
   compromised after the session.
3. **Manageable latency.** 600,000 PBKDF2-SHA256 iterations take ~300ms-1.2s
   on a modern CPU. The per-side compute cost is a small fraction of the
   total exchange time dominated by relay round-trips and hole punching.
   On a 400MHz embedded CPU the derivation may take 2-10s, still well
   within the 5-minute exchange deadline.

#### Time to Break: User-Chosen Codes

The iteration count raises the bar substantially for weak user-chosen codes.
An 8-character alphanumeric code (~48 bits at best) takes ~2.1 million years
on a single RTX 4090 (at ~16,700 guesses/s), or ~640 years on 100 GPUs.
Users should prefer auto-generated codes or long passphrases.

#### Why Not Argon2id

A memory-hard KDF would provide stronger protection against GPU/ASIC
acceleration. PBKDF2 was chosen because it is available in the standard
library (`golang.org/x/crypto/pbkdf2`), requires no additional
dependencies, and its CPU-bound iteration cost applies equally to both
peers during connection setup. A future upgrade to Argon2id is
contemplated for environments where user-chosen codes are the primary
use case.

### No Handshake Padding Beyond Confirm

The spake2 message (162 bytes: two 65-byte blinded points M+N + 32-byte
fingerprint) has a fixed size but is distinguishable from the confirm message
(160 bytes). A relay can deduce protocol phase from message size.

### Buffer Pool Isolation

The `sync.Pool` 32 KB buffers are zeroed on return, but this is a single-process
concern; any code running in the same process can already access the data.
The zeroing is defense-in-depth against accidental leaks within `io.CopyBuffer`,
not a cross-process security boundary.

### Connection-Level Goroutines

`closeOnCancel` (`internal/engine/transport.go`) spawns one goroutine per
connection that blocks on context cancellation. The goroutine exists for the
connection's lifetime, at most one per session in normal use. Not exploitable;
documented for completeness.

### stdout Not Closed

In pipe mode, `StartStdinPipe` intentionally does not close `os.Stdout` after
the copy completes. A peer writing past the logical end of the stream would
produce output on the terminal after the `qvole` process exits, but only if the
OS buffer is still accepting writes and the terminal is still attached. This is
a cosmetic limitation, not a security concern.

---

## Residual Risks Summary

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Code guessed online (via rate-limited relay) | Negligible | Full compromise | Rate limits, high entropy |
| Offline dictionary attack on recorded relay traffic | Negligible (52-bit auto) / Medium (user-chosen) | SPAKE2 session key recovery; metadata exposure | PBKDF2 with 600,000 iterations (~71-bit effective effort); auto codes infeasible, user codes hardened |
| Code compromised post-session + recorded relay traffic | Low | Metadata recovery (addresses, fingerprints); no data decryption | TLS 1.3 forward secrecy for data |
| RNG compromise → key recovery | Low (OS-level) | Full compromise | Defense in depth (OS, Go runtime) |
| Traffic analysis identifies peers | Moderate | Loss of anonymity | Fixed message sizes |
| Relay DoS disrupts rendezvous | High | Denial of service (no confidentiality loss) | Retry with different relay (out of band) |
| Hole punch spoofing on shared L2 network | Low-Medium | Denial of service (IP-only port matching, first-packet-wins race); no data compromise | TLS fingerprint pinning; reconnect with new code |
| Forward listener binds to 0.0.0.0 unintentionally | User-dependent | Local privilege escalation | Bind to 127.0.0.1 by default for `-L port:host:port` syntax |
| Cryptographic break of P-256, SHA-256, or TLS 1.3 | Negligible | Full compromise | Standard model; common to all TLS-based systems |
| Post-quantum adversary | Futuristic | Full compromise | No quantum-resistant primitives; code is not PQ-secure |

---

## Implementation Assurance

### Cryptographic Primitives

All cryptographic operations use Go's `crypto/*` standard library or
`golang.org/x/crypto`:

- SPAKE2: `crypto/elliptic` (P-256), `crypto/sha256`, `crypto/hmac`,
  `crypto/subtle`, `crypto/aes`, `crypto/cipher`, `crypto/rand`
- PBKDF2: `golang.org/x/crypto/pbkdf2` (600,000 iterations tunable via `QVOLE_KDF_ITERATIONS`)
- QUIC/TLS: `crypto/tls`, `crypto/ecdsa`, `crypto/x509`
- HKDF: `golang.org/x/crypto/hkdf`
- GCM: standard AES-256-GCM via `crypto/aes` + `crypto/cipher`

No custom cryptographic implementations beyond:
- `HashToCurve` (hash-and-increment with SHA-256) in `spake2/spake2.go`
- Code generation (`randInt` with rejection sampling) in `internal/util/code.go`

### Constant-Time Primitives

- `subtle.ConstantTimeCompare` for confirm HMAC (`spake2/spake2.go`)
- `subtle.ConstantTimeCompare` for certificate fingerprint (`internal/engine/transport.go`)
- `elliptic.P256().IsOnCurve`, constant-time via internal assembly

### Fuzz Tests

The following fuzz tests target parsers and cryptographic operations:

| Test | Target | File |
|------|--------|------|
| `FuzzNameplate` | Code → room name derivation | `cmd/qvole/fuzz_test.go` |
| `FuzzSplitTunnelRequest` | `-L`/`-R` request parsing | `cmd/qvole/fuzz_test.go` |
| `FuzzPasswordToScalar` | Password → scalar conversion | `cmd/qvole/fuzz_test.go` |
| `FuzzEncryptDecrypt` | Metadata encrypt/decrypt round-trip | `cmd/qvole/fuzz_test.go` |
| `FuzzGenerateCode` | Code generation and validation | `cmd/qvole/fuzz_test.go` |
| `FuzzHandlePacket` | Relay packet handler with arbitrary input | `cmd/qvole/fuzz_test.go` |
| `FuzzDecryptMetadata` | Metadata decryption with malformed ciphertext | `cmd/qvole/fuzz_test.go` |
| `FuzzComputeShared` | SPAKE2 shared secret with invalid points | `cmd/qvole/fuzz_test.go` |
| `FuzzVerifyConfirm` | Confirm HMAC verification with corrupt payloads | `cmd/qvole/fuzz_test.go` |

### Integration Tests

`tests/integration_test.go` exercises end-to-end scenarios (pipe, exec, tunnel)
with a real relay and multiple peers.

### Relay Crash Resistance

The relay processes all input through `HandlePacket` with bounded buffers
and timeouts. The worker pool (4 workers, 256-packet channel) prevents
CPU exhaustion from many concurrent clients. Write deadlines (500 ms) prevent
slow peers from blocking the relay.

### Security-Relevant Constants

| Constant | File | Value | Purpose |
|---|---|---|---|
| `minCodeLen` | `cmd/qvole/main.go` | 8 | Minimum code length |
| `maxCodeLen` | `cmd/qvole/main.go` | 256 | Maximum code length |
| `ExchangeDeadline` | `internal/engine/connect.go` | 5 min | Handshake timeout (overridable via `QVOLE_EXCHANGE_DEADLINE_MS`) |
| `RegInterval` | `internal/engine/connect.go` | 60 s | REG re-send interval; late-joining peer may wait up to this long to be discovered |
| `ConfirmPayloadSize` | `internal/engine/connect.go` | 160 | Fixed confirm size (incl. 32 B random padding) |
| `maxMsgRate` | `relay/room.go` | 10/s | Rate limit |
| `maxRooms` | `relay/room.go` | 10000 | Room cap |
| `maxRoomsPerIP` | `relay/room.go` | 100 | Per-IP room cap |
| `maxClientsPerRoom` | `relay/room.go` | 2 | Room membership cap (default; tunable) |
| `maxDatagramLen` | `relay/room.go` | 1400 | Raw UDP datagram limit |
| `MaxIncomingUniStreams` | `internal/engine/transport.go` | 0 | No uni streams |
| `maxTunnelRequests` | `internal/app/tunnel_request.go` | 100 | Spec count limit |
| `streamHeaderTimeout` | `internal/app/tunnel_request.go` | 15 s | Tunnel stream header read deadline |
| `streamConfigTimeout` | `internal/app/tunnel_request.go` | 30 s | Control stream config read deadline |
| `ForwardMaxStreams` | `internal/engine/transport.go` | 200 | Max concurrent tunnel streams (overridable via `QVOLE_FORWARD_MAX_STREAMS`) |
| `InsecureSkipVerify` | `internal/engine/transport.go` | true | Self-signed cert bypass |

---

## Vulnerability Reporting

`qvole` is a research/utility tool. Security issues can be reported to security @ qvole.dev
Given the protocol's design (relay is untrusted, cryptographic
handoff to TLS 1.3), the most impactful vulnerabilities would be in:

1. SPAKE2 implementation errors (session key derivation, point validation)
2. Certificate fingerprint verification bypass
3. TLS 1.3 configuration errors that permit downgrade
4. Side channels in constant-time comparisons

For critical vulnerabilities, consider the implications: SPAKE2 or fingerprint
verification flaws could allow an attacker with relay access to impersonate a
peer without the code. TLS configuration errors could allow MITM on the direct
path.
