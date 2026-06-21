# Privacy & Terms: Public Relay

The default relay at `relay.qvole.dev:9009` handles peer discovery and SPAKE2
handshake relay only. It operates with zero persistent storage and minimal data
handling by design.

---

## Privacy

### Data the relay processes

| Data | Why it's seen | Retention |
|---|---|---|
| **Your IP address + UDP port** | Inherent to UDP networking. Needed to route packets back to you | In-memory only, auto-evicted 1 min after last activity |
| **Room name** (8-hex-char hash prefix of your connection code) | Registers you in a rendezvous room so your peer can find you | In-memory only, auto-evicted 1 min after last activity |
| **Message timing and sizes** | SPAKE2 handshake packets (REG, MSG, REGD, MSGD) transit the relay | Not retained. Relayed immediately, no message buffering |

### What the relay never sees

- **Your connection code.** The relay only sees a SHA-256 hash prefix (8 hex
  characters). The full code is never transmitted to the relay.
- **Message payloads.** SPAKE2 blinded points are computationally indistinguishable
  from random data. Confirm payloads are AES-256-GCM encrypted with keys the relay
  never possesses.
- **Your tunnel data.** After the SPAKE2 handshake completes (seconds), peers
  connect directly over QUIC/TLS 1.3. The relay is out of the data path entirely.

### Logging

The relay logs to stderr for operational purposes only. Logged events include:

- Client join events (`Client <IP:port> joined room <room>`)
- Rate-limit and drop warnings
- Periodic aggregate stats (room count, client count, messages relayed)

Logs are not persisted to disk and are not shared with third parties.

### Storage

The relay is **fully in-memory and stateless**:

- No database, no log files, no disk writes
- Room state and client entries expire automatically 1 minute after inactivity
- Rate-limiting counters are cleaned every 5 seconds
- Aggregate counters (REGs, MSGs, drops) accumulate in-memory for the lifetime of
  the relay process only

### No tracking

No analytics, no cookies, no telemetry, no device fingerprinting, no user
accounts. The qvole client binary contains zero tracking code of any kind.

### Purpose and lawful basis

The relay processes the minimal data necessary to provide the rendezvous
service. Without IP addresses and room names it cannot route packets between
peers. The processing is based on **legitimate interest**: operating the
service essential to peer discovery, which users explicitly opt into by
connecting to the relay.

The relay is **untrusted by design** in qvole's security model. It never holds
keys to decrypt traffic, and the protocol is engineered so that even a fully
compromised relay cannot impersonate peers or read tunnel data.

### Third parties

No data is shared with third parties. The relay is a single Go binary with exactly
two library dependencies: `quic-go/quic-go` (QUIC transport) and
`golang.org/x/crypto` (cryptography). Both are open-source standard libraries
with no external network calls.

### Your choices

You can avoid sending any data to the public relay by running your own:
`qvole relay --listen :9009`. The full source code is available under the MIT
license.

---

## Terms of Service

### Acceptable use

By connecting to the public relay, you agree not to:

- **Scan, probe, or attack the relay.** Do not attempt to bypass rate limits,
  exhaust room capacity, spoof traffic, or otherwise degrade the relay's
  operation for other users.
- **Use the relay for illegal activity.** Do not transmit, relay, or facilitate
  content that violates applicable law.
- **Reverse-engineer or exploit the relay service** beyond its intended use as a
  rendezvous point for qvole peers.

Violation of these terms may result in your IP address being temporarily or
permanently blocked at the relay operator's discretion.

### No guarantee of service

The public relay is provided **as-is, with no guarantees**:

- **No uptime SLA.** The relay may be unavailable at any time for any reason,
  including maintenance, outages, or discontinuation of the service.
- **No fitness for purpose.** The relay is not guaranteed to be suitable for any
  particular use case. Test your application's behavior when the relay is
  unreachable.
- **No data recovery.** The relay stores nothing persistently. If the process
  restarts, all in-memory state is lost. This is by design and not a bug.

### Suitability warnings

- **Not an anonymity tool.** The relay sees your IP address and the rooms you
  join. A relay operator can prove "peer X at IP A joined room R at time T."
  Encrypted payloads provide plausible deniability about what was communicated,
  but qvole does not hide your identity from the relay operator.
- **Not a VPN.** qvole provides encrypted, authenticated point-to-point tunnels
  between two peers. It does not route all your traffic, hide your IP from
  destination servers, or provide the broad privacy guarantees of a VPN service.
- **Not for critical infrastructure.** The relay is a single point of failure.
  Connections cannot be established without it. Do not rely on the public relay
  for life-safety, medical, financial, or other critical systems.

### Limitation of liability

To the fullest extent permitted by law, the relay operators are not liable for
any damages arising from the use or inability to use the relay service,
including but not limited to: data loss, service interruption, unauthorized
access, security breaches, or any consequence of the relay's observation of
connection metadata.

---

## Contact

For privacy inquiries or terms questions: [privacy@qvole.dev](mailto:privacy@qvole.dev)

---

*Last updated: June 2026*
