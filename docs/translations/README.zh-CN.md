<p align="center"><img src="../../assets/logo.svg" width="96" alt="qvole"></p>

<h1 align="center">qvole</h1>
<p align="center">
  <a href="https://go.dev"><img src="https://img.shields.io/badge/go-1.27-blue" alt="Go 版本"></a>
  <a href="https://github.com/fernjager/qvole/actions/workflows/ci.yml"><img src="https://github.com/fernjager/qvole/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="../../LICENSE"><img src="https://img.shields.io/badge/license-MIT-green" alt="许可证"></a>
</p>
<p align="center"><em>基于 QUIC 的快速隧道，打洞穿越 NAT。</em></p>

<p align="center">
  <a href="../../README.md">English</a> &nbsp;|&nbsp;
  <a href="./README.zh-CN.md">简体中文</a> &nbsp;|&nbsp;
  <a href="./README.zh-TW.md">繁體中文</a> &nbsp;|&nbsp;
  <a href="./README.es.md">Español</a> &nbsp;|&nbsp;
  <a href="./README.ru.md">Русский</a>
</p>

两台机器，都位于 NAT 之后。与你的对端分享一个短代码，你们之间就拥有了一条加密管道。

端口转发、文件传输、运行远程命令与管道。一个二进制文件，无需配置文件，无需任何设置。

<p align="center"><img src="../../assets/demo.gif" alt="qvole 演示"></p>

---

## 快速入门示例

更多灵感请参见下文中的[示例](#管道)！

```bash
# 发送文件
alice$ qvole pipe < in                         # 打印连接代码，等待 bob 连接
bob$   QVOLE_CODE=CODE qvole pipe > out        # bob 连接，从 alice 接收文件

# 发送目录
alice$ tar czf - dir/ | qvole pipe             # alice 将 dir/ 的 tar 流发送给 bob
bob$   QVOLE_CODE=CODE qvole pipe | tar xzf -  # bob 接收并解压 dir/

# 端口转发
# 双方各自授权对方可访问的目标（-aF）；访问方自行选择方便的本机端口（-L）。
alice$ qvole tunnel \
    -L 8080:localhost:80 \
    -aF localhost:22
bob$   QVOLE_CODE=CODE qvole tunnel \
    -aF localhost:80 \
    -L 2222:localhost:22
alice$ curl localhost:8080    # 访问 bob 的 :80
bob$   ssh -p 2222 localhost  # 访问 alice 的 sshd

# 远程命令（alice 运行命令，bob 连接）
alice$ qvole exec --cmd "uptime"                    # 在 alice 上运行，bob 查看输出
alice$ qvole exec --cmd "script -q bash /dev/null"  # alice 为 bob 提供远程 shell
bob$   QVOLE_CODE=CODE qvole exec

# 运行私有中继服务器（UDP）
relay$ qvole relay --listen :9009
```

---

## 安装

**curl 一行命令：**

```bash
curl -fsSL https://get.qvole.dev | sh
```

**从源码安装：**

```bash
git clone https://github.com/fernjager/qvole && cd qvole && make && sudo make install
```

（需要 Go 1.27 或更高版本）

---

## 问题

我在各处运行服务：家庭实验室、VPS、帆船上装有蜂窝 SIM 卡的旅行路由器。
常规方案（VPN、SSH 反向隧道、端口转发）都需要证书、密钥分发或可达的端口。
每一个开放端口都是潜在的零日漏洞，而一台运气好才打补丁的个人服务器根本跟不上节奏。

我更愿意把服务绑定到 localhost，不开放任何入站端口，通过共享代码按需访问它们。
这同时也解决了三个更棘手的问题：发现（双方对端都在 NAT 之后）、认证（无需 PKI，
只需要一个代码）和传输（TCP 里套 TCP 本身就是一种残缺的传输方式）。

## 解决方案

`qvole` 是一个单一二进制文件，可在两台机器之间创建加密的直连 P2P 隧道。
你们分享一个代码，几秒钟内即可获得一条 QUIC 连接，支持 stdin/stdout 管道、
TCP 端口转发或远程命令执行。无需账号，无需配置文件，一个极简的公共中继仅负责
会合（rendezvous）。它永远不会解密你的流量。

受 [magic-wormhole](https://github.com/magic-wormhole/magic-wormhole) 启发，
qvole 改以 QUIC 作为传输层，通过组合三个广为人知的基础原语来实现：

### SPAKE2：口令认证密钥交换

每个对端发送一个盲化的椭圆曲线点。双方据此推导出一个共享密钥，除他们之外任何人都
无法计算，中继也不例外。代码*就是*凭证，握手之后它的使命便告完成。

### QUIC：基于 UDP 的流多路复用

单个 UDP socket 即可支持多条相互独立的字节流，无队头阻塞，内置 TLS 1.3，
每条流拥有独立的流量控制。每条连接始终经过加密和认证。

### UDP 打洞：NAT 穿越

位于不同 NAT 之后的两台对端同时向对方的外部地址发送数据包。每个出站数据包都会
打开一个针孔，让对端的数据包得以穿过。这与视频通话和电话会议使用的 NAT 穿越技术
相同，适用于多种常见的 NAT 配置。

### 密码学交接

这些概念都不是新事物，关键在于它们如何为这一特定应用组合在一起。

关键之处在于：TLS 证书指纹与 SPAKE2 盲化点一同交换，然后通过由 PAKE 派生的
确认 HMAC 绑定到共享密钥，从而认证 TLS 会话。

这实现了从低熵代码到强 256 位 TLS 会话的密码学交接，该会话具有前向保密性，
并由上述指纹认证，整个过程只需几秒钟：

```
共享代码（52 位）
       │
       ▼
  SPAKE2 PAKE ──► 会话密钥 + 对端证书指纹
       │
       ▼
  打洞        ──► 直连 UDP 路径
       │
       ▼
  QUIC/TLS 1.3 ──► 固定指纹验证
       │
       ▼
  流          ──► 数据（pipe、exec、tunnel）
```

握手完成后，中继便什么也看不到了。它无法推导密钥、解密流量，也无法冒充对端。

即使有人事后暴力破解或获知了代码，并录下了全部中继流量，他们最多也只能恢复
证书指纹，而无法得到保护实际数据的 TLS/QUIC 会话密钥。

**名字由此而来：田鼠（vole）在障碍物下打洞，而 `qvole` 在 NAT 之下掘进 QUIC 隧道。**

## 设计理念

`qvole` 刻意保持极简。它只做一件事：在两个对端之间建立临时的加密隧道。
诸如 SOCKS 或 HTTP 代理、文件传输命令等功能被有意排除在外，因为与 `qvole`
组合使用时，现有工具已经能胜任这些角色。

`qvole` 不会成为你的 SOCKS 代理，SSH 在这方面做得很好。但 `qvole` 会给你
一条可以用来跑 SSH 的隧道。

坚持 stdin/stdout 流与 TCP 端口转发，`qvole` 能与你的系统中的其他工具自然地组合。

**这正是 Unix 哲学：每个工具只做一件事，再用管道把它们连接起来。**

`qvole` 有自己的坚持，也承认它有些小众。它挠的是我自己的痒处：我对开放端口很偏执，
需要一种按需访问机器而又不暴露它们的方式。如果这个工具碰巧对其他人也有用，那就太好了！

### 协议

**[完整协议规范 → PROTOCOL.md](../../docs/PROTOCOL.md)**

握手在几秒内完成：先通过 UDP 中继上的 SPAKE2 PAKE 进行对端发现与认证，
再通过 UDP 打洞实现直连 NAT 穿越，最后建立一条使用固定自签名证书的 QUIC 连接。

关于连接生命周期的逐步说明以及线上传输数据报的结构，请参阅上述文档。

### 安全

**[完整安全文档 → SECURITY.md](../../SECURITY.md)**

`qvole` 的安全模型是一次密码学交接：SPAKE2 PAKE 从一个共享代码出发认证两个对端，
随后带固定证书指纹的 TLS 1.3 保护直连的 QUIC 数据通路。中继被假定为不可信：
它无法推导会话密钥、解密流量或冒充对端。

详细的分解说明请参阅上述文档，其中涵盖威胁模型、协议安全分析、设计权衡与局限性。

### 局限性与权衡

#### 网络连通性
* **网络支持不一致**：我在家庭宽带、机场 Wi-Fi 和移动热点上测试过 `qvole`。能否连接取决于网络环境。大多数情况下可以工作，少数情况下不行。没有备用方案，因此你需要另一种连接方式。
* **无数据中继备用方案**：我们不实现 TURN 中继，以保持中继服务器的简单，并确保它永远不会接触用户数据。这意味着当双方对端都位于阻止 UDP 打洞的限制性对称 NAT 之后时，连接将失败；没有数据中继的备用通路。
* **仅限 UDP**：协议完全依赖 UDP，没有 TCP 备用方案。阻止所有出站 UDP 流量的高度限制性环境将导致无法建立连接。

#### 运维约束
* **中继是单点故障**：没有中继，对端之间无法相互发现或交换握手材料。如果中继不可用，就无法建立任何连接。
* **仅支持两个对端**：协议每条连接恰好支持两个对端。不支持多方会议。

#### 性能与可扩展性
* **无状态中继设计**：中继不实现消息队列或持久化存储。这使它保持极低的开销，并能抵御状态耗尽攻击，但意味着如果客户端在握手期间暂时不可达，消息可能会丢失。
* **限流**：我们实现了严格的限流（例如每个客户端每秒 10 条消息），以防止中继被滥用。这保证了社区的稳定性，但可能会限制极高频率的信令。

---

## Go 库

在自己的 Go 程序中使用 `qvole`：

```go
import "github.com/fernjager/qvole"
```

```go
// 简单的点对点管道
conn, _ := qvole.Dial(ctx,
    qvole.WithCode("my-secret"),
    qvole.WithRelay("relay.qvole.dev:9009"),
)
conn.Write([]byte("hello"))
conn.Close()

// 远程命令执行
qvole.Exec(ctx,
    qvole.WithCode("secret"),
    qvole.WithRelay("relay.qvole.dev:9009"),
    qvole.WithCommand("uptime"), qvole.WithCmdMode(true),
)

// 端口转发：Alice 在本机监听 8080，并转发到 Bob 的 :80。
// Bob 必须用 qvole.WithAllowForward("localhost:80") 将该目标加入白名单。
qvole.Tunnel(ctx,
    qvole.WithCode("secret"),
    qvole.WithRelay("relay.qvole.dev:9009"),
    qvole.WithLocalTunnel("8080:localhost:80"),
)

// Bob（对端一侧）：将 Alice 可以通过本机访问的目标加入白名单。
qvole.Tunnel(ctx,
    qvole.WithCode("secret"),
    qvole.WithRelay("relay.qvole.dev:9009"),
    qvole.WithAllowForward("localhost:80"),
)

// 运行你自己的中继
go relay.RunRelay(ctx, ":9009")
conn, _ = qvole.Dial(ctx,
    qvole.WithCode("secret"), qvole.WithRelay("localhost:9009"),
)
```

完整的 API 参考、选项参考、统计跟踪以及更多示例，请参阅 **[LIBRARY.md](../../docs/LIBRARY.md)**。

## 子命令

```
用法：
  qvole pipe      [--code CODE] [--stats]                            # 管道模式
  qvole tunnel    -L request [-L ...] [-R ...] [--code CODE]         # 端口转发
  qvole exec      --cmd cmd [--code CODE]                            # 远程命令
  qvole relay     [--listen :9009]                                   # 中继服务器

通用命令标志（pipe / exec / tunnel）：
  --code CODE        连接代码（或 $QVOLE_CODE）
  --relay addr       中继地址（host:port）
  --debug            将详细的调试日志输出到 stderr
```

### 管道

基于最多两条 QUIC 流的 stdin/stdout 管道（stdin 为终端时一条，stdin 被管道输入时
两条）。双向通信。未提供代码时，会自动生成一个并打印到 `stderr`。

| 标志 | 描述 |
|------|-------------|
| `--relay addr` | 中继地址（host:port） |
| `--stats` | 将传输统计信息记录到 stderr |
| `--debug` | 将详细的调试日志输出到 stderr |

**示例：**

握手完成后，`qvole` 会开启一条直连 P2P 数据传输，没有中继瓶颈。每条连接都是
一个 QUIC 会话，因此你不仅可以通过它发送文件，还可以在它之上传输管道数据。

```bash
# 监控吞吐量：使用内置的 --stats（无需 pv）
alice$ qvole pipe --stats < largefile.dat
bob$   QVOLE_CODE=CODE qvole pipe --stats > received.dat

# --stats 输出到 stderr 的示例：
# ↑ 5.20 MB (2.60 MB/s)  ↓ 10.40 MB (5.20 MB/s)
# ↑ 10.40 MB (2.60 MB/s)  ↓ 20.80 MB (5.20 MB/s)
# ...
# 总计：↑ 50.00 MB  ↓ 100.00 MB

# 磁盘克隆：通过 P2P 隧道克隆远程磁盘
alice$ sudo dd if=/dev/sda | qvole pipe
bob$   QVOLE_CODE=CODE qvole pipe | sudo dd of=/dev/sdb

# 数据库备份：通过隧道传输转储文件
alice$ pg_dump mydb | gzip | qvole pipe
bob$   QVOLE_CODE=CODE qvole pipe | gunzip | psql mydb

# 摄像头/媒体流：从采集设备进行流式传输
alice$ ffmpeg -i /dev/video0 -f mpegts - | qvole pipe
bob$   QVOLE_CODE=CODE qvole pipe | ffplay -

# 远程日志跟踪：跟踪远程系统的日志
alice$ journalctl -f | qvole pipe
bob$   QVOLE_CODE=CODE qvole pipe

# Gzip 压缩：在传输过程中压缩数据
alice$ tar czf - dir/ | qvole pipe
bob$   QVOLE_CODE=CODE qvole pipe | tar xzf -

# 远程抓包：远程捕获网络流量
alice$ tcpdump -w - -i eth0 | qvole pipe
bob$   QVOLE_CODE=CODE qvole pipe > capture.pcap

# 剪贴板同步：在机器之间共享剪贴内容
alice$ qvole pipe | pbcopy
bob$   pbpaste | QVOLE_CODE=CODE qvole pipe

# 简单 P2P 聊天：直接的双向文本通信（用 cat 桥接终端）
alice$ cat | qvole pipe
bob$   cat | QVOLE_CODE=CODE qvole pipe
```

#### Shell 辅助函数
```bash
# Shell 辅助函数：预共享一个代码，在受信任的机器之间实现随时访问
# 生成一次代码，然后添加到两台机器的 ~/.bashrc：
$ openssl rand -base64 24
$ echo 'export QVOLE_CODE=<generated-code>' >> ~/.bashrc
$ echo "qsend() { tar czf - \"\$@\" | qvole pipe --stats; }" >> ~/.bashrc
$ echo "qrecv() { qvole pipe --stats | tar xzf -; }" >> ~/.bashrc
$ echo "alias qp='qvole pipe --stats'" >> ~/.bashrc

alice$ qsend file.dat                              # 发送文件（gzip 压缩）
alice$ qsend dir/                                  # 发送目录（gzip 压缩）
bob$   qrecv                                       # 接收并解压
alice$ qsend file1.dat dir/                        # 发送多个
bob$   qrecv                                       # 全部接收

alice$ arecord -f cd | qp                          # 通过 P2P 传输麦克风音频（macOS：ffmpeg -f avfoundation -i ":0" -f mp3 - | qp）
bob$   qp | aplay -f cd                            # 收听 alice 的麦克风（或：qp | ffplay -i pipe:0 -nodisp）
```

### 执行

一侧直接运行本地命令，并将其 stdin/stdout 桥接到 QUIC 流；另一侧则作为普通
管道工作（stdin→流→stdout）。

| 标志 | 描述 |
|------|-------------|
| `--cmd <cmd>` | 在本机运行 `<cmd>`，并将其 stdin/stdout 通过隧道传输 |
| `--code CODE`  | 连接代码（或 `$QVOLE_CODE`） |
| `--relay addr` | 中继地址（host:port） |
| `--debug` | 将详细的调试日志输出到 stderr |

与 pipe 子命令不同（pipe 的流方向取决于 stdin 是否为终端：被管道输入的 stdin
会打开出站流，终端 stdin 则不会），`exec` 一侧始终打开流，而 pipe 一侧始终接受。
只有带 `--cmd` 的一侧会运行命令，另一侧只需连接即可。

**示例：**

在限制性网络或客户现场，无需分发 SSH 密钥即可进行临时远程服务器管理和服务访问。

```bash
# 远程 Shell：通过隧道提供完整的交互式 Shell
alice$ qvole exec --cmd "script -q bash /dev/null"
bob$   QVOLE_CODE=CODE qvole exec

# 远程监控：使用 script -q 运行 TUI 应用
alice$ qvole exec --cmd "script -q htop /dev/null"
bob$   QVOLE_CODE=CODE qvole exec

# 远程磁盘占用：远程查看磁盘占用情况
alice$ qvole exec --cmd "script -q 'ncdu /' /dev/null"
bob$   QVOLE_CODE=CODE qvole exec

# 远程日志查看：查看远程系统上的日志
alice$ qvole exec --cmd "journalctl -n 100 --no-pager"
bob$   QVOLE_CODE=CODE qvole exec

# 远程系统信息：查看远程系统详情
alice$ qvole exec --cmd "uname -a; uptime"
bob$   QVOLE_CODE=CODE qvole exec

# Docker 管理：在远程主机上运行 docker 命令
alice$ qvole exec --cmd "docker ps"
bob$   QVOLE_CODE=CODE qvole exec

# 远程抓包：将抓包数据流式传输到本机 tcpdump
alice$ qvole exec --cmd "tcpdump -w - -i eth0"
bob$   QVOLE_CODE=CODE qvole exec | tcpdump -r -

# 共享终端会话：通过 tmux 进行协作终端
alice$ qvole exec --cmd "script -q 'tmux new -A -s qvole' /dev/null"
bob$   QVOLE_CODE=CODE qvole exec
```

### <a id="tunnel"></a>隧道

基于 QUIC 隧道的 TCP 端口转发，仿照 SSH 的 `-L` 和 `-R` 模式。

| 标志 | 描述 |
|------|-------------|
| `-L [laddr:]lport:raddr:rport` | 在本机 `[laddr:]lport` 上监听，转发到远程 `raddr:rport`（对端需用 `-aF` 允许） |
| `-R [raddr:]rport:laddr:lport` | 对端在远程 `[raddr:]rport` 上监听，转发到本机 `laddr:lport`（对端需用 `-aL` 允许） |
| `-aL addr:port` | 允许对端在该地址上开放监听端口（允许对端的 `-R`；可重复） |
| `-aF addr:port` | 允许对端通过你连接该 addr:port（允许对端的 `-L`；可重复） |
| `-a` | 允许对端的**所有**隧道请求（不安全）：对端可以通过你的机器打开任意端口、访问任意地址 |
| `--code CODE` | 连接代码（或 `$QVOLE_CODE`） |
| `--relay addr` | 中继地址（host:port） |
| `--debug` | 将详细的调试日志输出到 stderr |

多个 `-L` 和 `-R` 标志可以组合使用。默认不接受任何请求：你的 `-L` 请求需要对端
以 `-aF <target>` 运行，你的 `-R` 请求需要对端以 `-aL <listen addr>` 运行。

对端的接受信号是按侧而非按请求生效的：如果对端的白名单没有覆盖你的某个请求，
你的监听器可能仍能绑定，但通过它的每条连接都会在对端一侧被拒绝（对端会记录
`Tunnel rejected: … (add -aF …)`）。当对端完全没有白名单运行时，你的一侧会
精确记录对端必须运行的标志。

`-aL`/`-aF` 条目中主机名为空（例如 `:8081`）时，将匹配该端口上的**任意**主机，
因此 `-aL :8081` 允许对端绑定 `0.0.0.0:8081`，`-aF :8081` 允许对端通过你的
机器访问任意 `host:8081`。除非有意使用通配符，否则请使用明确的主机（`127.0.0.1:8081`）。

```bash
# Alice 监听 8080，转发到 Bob 的 :80（Bob 需要对目标使用 -aF）
alice$ qvole tunnel -L 8080:localhost:80
bob$   QVOLE_CODE=CODE qvole tunnel -aF localhost:80

# Alice 请求 Bob 监听 9022，并转发回 Alice 的 :22（Bob 需要对端口使用 -aL）
alice$ qvole tunnel -R 9022:localhost:22
bob$   QVOLE_CODE=CODE qvole tunnel -aL 127.0.0.1:9022

# 组合使用：Alice 通过 :8080 访问 Bob 的 :80，Bob 通过 :9022 访问 Alice 的 :22
alice$ qvole tunnel -L 8080:localhost:80 -R 9022:localhost:22
bob$   QVOLE_CODE=CODE qvole tunnel -aL 127.0.0.1:9022 -aF localhost:80
```

| 标志形式 | 含义 |
|-----------|---------|
| `-L lport:raddr:rport` | 在 `127.0.0.1:lport` 上监听，转发到对端的 `raddr:rport` |
| `-L laddr:lport:raddr:rport` | 在 `laddr:lport` 上监听，转发到对端的 `raddr:rport` |
| `-R rport:laddr:lport` | 对端在 `127.0.0.1:rport` 上监听，转发到你一侧的 `laddr:lport` |
| `-R raddr:rport:laddr:lport` | 对端在 `raddr:rport` 上监听，转发到你一侧的 `laddr:lport` |

**示例：**

无需 root、内核模块、TUN 设备或持久化状态即可进行 TCP 流量转发。恰好两个对端
之间的点对点连接；跨重启保持前向保密。

```bash
# Postgres：远程数据库访问
alice$ qvole tunnel -L 5432:localhost:5432
bob$   QVOLE_CODE=CODE qvole tunnel -aF localhost:5432

# 远程桌面：通过 P2P 链路转发 VNC
alice$ qvole tunnel -L 5900:localhost:5900
bob$   QVOLE_CODE=CODE qvole tunnel -aF localhost:5900

# 远程 Docker 守护进程：无需暴露 TCP socket
alice$ qvole tunnel -L 2375:localhost:2375
bob$   QVOLE_CODE=CODE qvole tunnel -aF localhost:2375

# SOCKS 代理：通过对端网络路由流量
alice$ ssh -D 1080 localhost -N                          #启动本地 SOCKS 代理
alice$ qvole tunnel -R 1080:localhost:1080               #将其暴露给 Bob
bob$   QVOLE_CODE=CODE qvole tunnel -aL 127.0.0.1:1080   #Bob 现在在 :1080 上有 SOCKS

# 监控栈：访问 NAT 之后的 Prometheus/Grafana
alice$ qvole tunnel -L 9090:localhost:9090 -L 3000:localhost:3000
bob$   QVOLE_CODE=CODE qvole tunnel -aF localhost:9090 -aF localhost:3000

# 媒体服务器：在远程电视上观看 Plex/Jellyfin
alice$ qvole tunnel -L 32400:localhost:32400
bob$   QVOLE_CODE=CODE qvole tunnel -aF localhost:32400

# SSH 逃生通道：在锁定严格的机器上暴露 sshd
# Alice 在 :22 上运行 sshd 并授权对端访问（-aF）。断线时用循环重连（或参见 Persistent Tunnels 的 systemd 单元）。
alice$ while true; do QVOLE_CODE=CODE qvole tunnel -aF localhost:22; sleep 1; done
# Bob 选择任意方便的本机端口，即可连到 Alice 的 sshd
bob$   QVOLE_CODE=CODE qvole tunnel -L 2222:localhost:22
bob$   ssh -p 2222 localhost                              #Bob 访问 Alice 的 sshd

# 游戏服务器：在 NAT 之后托管 Minecraft 服务器
alice$ qvole tunnel -L 25565:localhost:25565
bob$   QVOLE_CODE=CODE qvole tunnel -aF localhost:25565

# Redis：访问远程 Redis 实例
alice$ qvole tunnel -L 6379:localhost:6379
bob$   QVOLE_CODE=CODE qvole tunnel -aF localhost:6379

# Web 开发预览：向对端暴露本地开发服务器
alice$ npm run dev                                       #运行在 :3000
alice$ qvole tunnel -R 3000:localhost:3000               #Alice 将她的 :3000 暴露给 Bob
bob$   QVOLE_CODE=CODE qvole tunnel -aL 127.0.0.1:3000   #Bob 现在可以访问 localhost:3000
```


#### 持久隧道
```bash
# 自动重连：使用固定代码在断开后重新连接
alice$ while true; do QVOLE_CODE=CODE qvole tunnel -L 8080:localhost:80; sleep 1; done

# 对于长期运行的部署，请生成一个强随机代码：
$ openssl rand -base64 24

# 或者使用 systemd 持久化，让隧道永远保持存活
# /etc/systemd/system/qvole-tunnel.service
[Unit]
Description=qvole P2P 隧道
After=network-online.target

[Service]
Type=simple
# 先生成一个强代码：openssl rand -base64 24
Environment=QVOLE_CODE=<generated-code>
ExecStart=/usr/local/bin/qvole tunnel -L 8080:localhost:80
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

### 环境变量

#### 客户端变量（pipe / exec / tunnel）

| 变量 | 默认值 | 描述 |
|----------|---------|-------------|
| `QVOLE_CODE` | - | 连接代码（`--code` 的替代方式） |
| `QVOLE_KDF_ITERATIONS` | 600000 | 代码到标量推导的 PBKDF2 迭代次数 |
| `QVOLE_KEEPALIVE_MS` | 5000 | QUIC 保活间隔 |
| `QVOLE_IDLE_TIMEOUT_MS` | 120000 | 断开前的 QUIC 空闲超时 |
| `QVOLE_HANDSHAKE_TIMEOUT_MS` | 30000 | 握手超时 |
| `QVOLE_MAX_STREAMS` | 100 | 最大并发 QUIC 流数 |
| `QVOLE_FORWARD_MAX_STREAMS` | 200 | 最大转发隧道流数 |
| `QVOLE_PUNCH_TIMEOUT_MS` | 10000 | UDP 打洞超时 |
| `QVOLE_EXCHANGE_DEADLINE_MS` | 90000 | SPAKE2 交换截止时间 |
| `QVOLE_SPAKE2_RESEND_MS` | 2000 | SPAKE2 消息重发间隔 |
| `QVOLE_CONFIRM_RESEND_MS` | 2000 | 确认消息重发间隔 |
| `QVOLE_EXCHANGE_READ_DEADLINE_MS` | 1000 | 中继读取轮询间隔 |
| `QVOLE_REG_INTERVAL_MS` | 30000 | 中继注册间隔 |
| `QVOLE_DIAL_TIMEOUT_MS` | 10000 | 隧道 TCP 拨号超时 |
| `QVOLE_STREAM_IDLE_TIMEOUT_MS` | 0 | 静默转发流被回收前的空闲窗口（0 表示禁用；按流量刷新，因此活跃传输不会被截断） |
| `QVOLE_EXEC_DRAIN_TIMEOUT_MS` | 5000 | 命令退出后的 exec 排空超时 |
| `QVOLE_INITIAL_STREAM_WINDOW` | 1048576 | QUIC 流接收窗口 |
| `QVOLE_INITIAL_CONNECTION_WINDOW` | 4194304 | QUIC 连接接收窗口 |

#### 中继变量（`relay` 子命令）

| 变量 | 默认值 | 描述 |
|----------|---------|-------------|
| `QVOLE_RELAY_MAX_ROOMS` | 10000 | 最大并发房间数 |
| `QVOLE_RELAY_MAX_ROOMS_PER_IP` | 10 | 每个 IP 地址的最大房间数 |
| `QVOLE_RELAY_MAX_CLIENTS_HARD` | 20 | 每个房间客户端数量的硬上限 |
| `QVOLE_RELAY_MSG_RATE` | 10 | 每个客户端每个速率窗口的最大消息数 |
| `QVOLE_RELAY_RATE_WINDOW_MS` | 1000 | 消息限流的速率窗口时长 |
| `QVOLE_RELAY_TTL_MS` | 60000 | 客户端过期前的注册 TTL（1 分钟） |
| `QVOLE_RELAY_CLEANUP_INTERVAL_MS` | 60000 | 过期注册的清理间隔 |
| `QVOLE_RELAY_WORKERS` | 4 | worker goroutine 数量 |
| `QVOLE_RELAY_PKT_CHAN_BUF` | 256 | 数据包通道缓冲区大小 |
| `QVOLE_RELAY_WRITE_DEADLINE_MS` | 500 | UDP 写入截止时间 |
| `QVOLE_RELAY_WRITE_POOL` | 256 | 中继出站写入的写入池大小 |
| `QVOLE_RELAY_STATS_INTERVAL_MS` | 300000 | 统计信息记录间隔 |

### 隐私与条款

[公共中继的隐私政策与服务条款 &rarr; PRIVACY.md](../../docs/PRIVACY.md)

---

