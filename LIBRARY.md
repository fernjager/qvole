# qvole Go Library

> Encrypted peer-to-peer connections over QUIC, through NATs.
> Share a code, get a `net.Conn`.

```go
import "github.com/fernjager/qvole"
```

## Quick Start

### 1. Bidirectional pipe

```go
// Peer A: dial and write
conn, err := qvole.Dial(ctx,
    qvole.WithCode("my-secret"),
    qvole.WithRelay("relay.qvole.dev:9009"),
)
conn.Write([]byte("hello from A\n"))
var buf [1024]byte
n, _ := conn.Read(buf[:])
fmt.Print(string(buf[:n]))
conn.Close()

// Peer B: accept and respond
conn, err := qvole.Accept(ctx,
    qvole.WithCode("my-secret"),
    qvole.WithRelay("relay.qvole.dev:9009"),
)
var buf [1024]byte
n, _ := conn.Read(buf[:])
conn.Write([]byte("hello back from B\n"))
conn.Close()
```

### 2. Remote command execution

```go
// Side that runs the command
err := qvole.Exec(ctx,
    qvole.WithCode("my-secret"),
    qvole.WithRelay("relay.qvole.dev:9009"),
    qvole.WithCommand("uptime"),
    qvole.WithCmdMode(true),
)

// Side that sees the output (bridges to stdin/stdout)
err := qvole.Exec(ctx,
    qvole.WithCode("my-secret"),
    qvole.WithRelay("relay.qvole.dev:9009"),
    qvole.WithCmdMode(false),
)
```

### 3. Port forwarding (tunnel)

```go
// Peer with the service: expose port 8080
err := qvole.Tunnel(ctx,
    qvole.WithCode("my-secret"),
    qvole.WithRelay("relay.qvole.dev:9009"),
    qvole.WithRemoteTunnel("8080:localhost:80"),
)

// Peer consuming the service: connect with --allow-tunnel
err := qvole.Tunnel(ctx,
    qvole.WithCode("my-secret"),
    qvole.WithRelay("relay.qvole.dev:9009"),
    qvole.WithAllowTunnel(true),
)
// Now localhost:8080 reaches the peer's port 80
```

## Connection

### Dial / Accept

The simplest API: one side calls `Dial`, the other calls `Accept`. Both return a
`net.Conn` that supports `Read`, `Write`, `Close`, and deadline methods.

```go
func Dial(ctx context.Context, opts ...Option) (net.Conn, error)
func Accept(ctx context.Context, opts ...Option) (net.Conn, error)
```

`Dial` opens a QUIC stream after the handshake. `Accept` waits for the peer to
open one. Both internally handle SPAKE2 key exchange, UDP hole punching, and
QUIC connection setup.

### Connect

For users who need multiple streams (e.g. multiplexed protocols), `Connect`
returns the raw `*quic.Conn`:

```go
func Connect(ctx context.Context, opts ...Option) (*quic.Conn, bool, error)
```

The `bool` indicates whether this peer is the QUIC server (determined
lexicographically from the SPAKE2 public points).

### Exec

```go
func Exec(ctx context.Context, opts ...Option) error
```

When `WithCmdMode(true)`, opens a QUIC stream, runs the command locally, and
bridges its stdin/stdout over the stream. Exit code is propagated. When
`WithCmdMode(false)`, accepts a stream and bridges it to stdin/stdout.

### Tunnel

```go
func Tunnel(ctx context.Context, opts ...Option) error
```

Exchanges port-forwarding rules with the peer and sets up TCP listeners and
stream acceptors. Each TCP connection gets its own QUIC stream. Supports local
(`-L`) and remote (`-R`) forwarding via `WithLocalTunnel` and
`WithRemoteTunnel`.

## Options

Options are passed as variadic `Option` functions:

```go
conn, err := qvole.Dial(ctx,
    qvole.WithCode("my-secret"),
    qvole.WithRelay("relay.qvole.dev:9009"),
    qvole.WithPunchTimeout(5*time.Second),
)
```

### Required

| Option | Description |
|---|---|
| `WithCode(code string)` | Shared secret (8-256 chars) |
| `WithRelay(addr string)` | Relay server address (e.g. `relay.qvole.dev:9009`) |

### Connection

| Option | Default | Description |
|---|---|---|
| `WithPunchTimeout(d Duration)` | 10s | Max time for UDP hole punching |
| `WithExchangeDeadline(d Duration)` | 90s | Max time for SPAKE2 exchange |
| `WithKeepAlive(d Duration)` | 2s | QUIC keepalive interval |
| `WithIdleTimeout(d Duration)` | 2min | QUIC idle timeout |
| `WithHandshakeTimeout(d Duration)` | 30s | QUIC handshake timeout |
| `WithMaxStreams(n int)` | 100 | Max incoming bidirectional streams |
| `WithForwardMaxStreams(n int)` | 200 | Max incoming streams for tunnel forwarding |

### Exec

| Option | Description |
|---|---|
| `WithCommand(cmd string)` | Command to run (required with `WithCmdMode(true)`) |
| `WithCmdMode(b bool)` | `true` = this side runs the command; `false` = bridges to stdin/stdout |

### Tunnel

| Option | Description |
|---|---|
| `WithLocalTunnel(spec string)` | Local port forward, e.g. `"8080:localhost:80"` |
| `WithRemoteTunnel(spec string)` | Remote port forward, same format |
| `WithAllowTunnel(b bool)` | Accept tunnel requests from the peer |

Tunnel specs use the format `[addr:]port:host:port`. If the listen address is
omitted, it defaults to `127.0.0.1`. IPv6 addresses must be bracketed.

## Utilities

```go
func GenerateCode() (string, error)
```
Generates a human-readable code like `"0000-word-word-word"`.

```go
func Nameplate(code string) string
```
Derives a 4-character room identifier from a code.

```go
func ParseTunnelRequest(spec, typ string) (*TunnelRequest, error)
```
Parses a tunnel spec string.

```go
type TunnelRequest struct {
    Type       string // "L" or "R"
    ListenAddr string
    TargetAddr string
}
```

```go
var BufferPool *sync.Pool   // 32 KB reusable byte buffers
func PutBuffer(buf []byte)
```

## Stats

`StatsTracker` provides throughput tracking:

```go
import "github.com/fernjager/qvole/internal/engine"

st := engine.NewStatsTracker()
// Wrap writers to count bytes:
txW := st.TXWriter(conn)  // counts transmitted bytes
rxW := st.RXWriter(conn)  // counts received bytes
// Periodic logging:
st.Start(2 * time.Second)
// Final totals:
st.StopAndLog()
```

## Running your own relay

```go
import "github.com/fernjager/qvole/relay"

// Start a relay server
go relay.RunRelay(ctx, ":9009")

// Connect through it
conn, err := qvole.Dial(ctx,
    qvole.WithCode("my-secret"),
    qvole.WithRelay("localhost:9009"),
)
```

## Security

- All traffic encrypted via SPAKE2 PAKE + TLS 1.3 with certificate pinning.
- Relay is untrusted: sees only opaque hex blobs, cannot derive session keys.
- Forward secrecy for peer metadata (addresses, certificate fingerprints).
- See [PROTOCOL.md](./PROTOCOL.md) for the full wire protocol.
