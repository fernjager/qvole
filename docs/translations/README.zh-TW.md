<p align="center"><img src="../../assets/logo.svg" width="96" alt="qvole"></p>

<h1 align="center">qvole</h1>
<p align="center">
  <a href="https://go.dev"><img src="https://img.shields.io/badge/go-1.27-blue" alt="Go 版本"></a>
  <a href="https://github.com/fernjager/qvole/actions/workflows/ci.yml"><img src="https://github.com/fernjager/qvole/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="../../LICENSE"><img src="https://img.shields.io/badge/license-MIT-green" alt="授權"></a>
</p>
<p align="center"><em>基於 QUIC 的快速隧道，打洞穿越 NAT。</em></p>

<p align="center">
  <a href="../../README.md">English</a> &nbsp;|&nbsp;
  <a href="./README.zh-CN.md">简体中文</a> &nbsp;|&nbsp;
  <a href="./README.zh-TW.md">繁體中文</a> &nbsp;|&nbsp;
  <a href="./README.es.md">Español</a> &nbsp;|&nbsp;
  <a href="./README.ru.md">Русский</a>
</p>

兩台都在 NAT 之後的機器。與你的對端分享一個簡短的代碼，你們之間就有一條加密管道。

轉發連接埠、傳輸檔案、執行遠端指令與管道。單一執行檔，沒有設定檔，也不需要任何設定。

<p align="center"><img src="../../assets/demo.gif" alt="qvole 示範"></p>

---

## 快速上手範例

更多靈感請見下方的[範例](#管道)！

```bash
# 傳送檔案
alice$ qvole pipe < in                         # 印出連線代碼，等待 bob 連線
bob$   QVOLE_CODE=CODE qvole pipe > out        # bob 連線，接收 alice 的檔案

# 傳送資料夾
alice$ tar czf - dir/ | qvole pipe             # alice 將 dir/ 的 tar 串流傳給 bob
bob$   QVOLE_CODE=CODE qvole pipe | tar xzf -  # bob 接收並解壓縮 dir/

# 連接埠轉發
# 雙方各自授權對方可存取的目標（-aF）；存取方自行選擇方便的本機連接埠（-L）。
alice$ qvole tunnel \
    -L 8080:localhost:80 \
    -aF localhost:22
bob$   QVOLE_CODE=CODE qvole tunnel \
    -aF localhost:80 \
    -L 2222:localhost:22
alice$ curl localhost:8080    # 連到 bob 的 :80
bob$   ssh -p 2222 localhost  # 連到 alice 的 sshd

# 遠端指令（alice 主控指令，bob 連線）
alice$ qvole exec --cmd "uptime"                    # 在 alice 上執行，bob 看到輸出
alice$ qvole exec --cmd "script -q bash /dev/null"  # alice 提供 bob 遠端 shell
bob$   QVOLE_CODE=CODE qvole exec

# 執行私有中繼伺服器（UDP）
relay$ qvole relay --listen :9009
```

---

## 安裝

**curl 一行安裝：**

```bash
curl -fsSL https://get.qvole.dev | sh
```

**從原始碼編譯：**

```bash
git clone https://github.com/fernjager/qvole && cd qvole && make && sudo make install
```

（需要 Go 1.27 以上版本）

---

## 問題所在

我在各地執行各種服務：家中實驗室（homelab）、VPS、帆船上插著行動 SIM 卡的旅行路由器。
常見的選項（VPN、SSH 反向隧道、連接埠轉發）都需要憑證、金鑰分發，或對外可達的連接埠。
每個開放的連接埠都是潛在的零日漏洞，而一台運氣好才會被修補的個人伺服器根本跟不上。

我寧可把服務綁定在 localhost，對外不開放任何連接埠，需要時再透過共享代碼隨需連線。
這也一併解決三個更棘手的問題：探索（兩端都在 NAT 之後）、驗證（不需要 PKI，只要一個代碼）、
以及傳輸（在 TCP 裡再做 TCP 隧道，本身就是另一種缺陷）。

## 解決方案

`qvole` 是單一執行檔，可在兩台機器之間建立加密、直連的 P2P 隧道。只要分享一個代碼，
幾秒鐘內你就能擁有 QUIC 連線，可用於 stdin/stdout 管道、TCP 連接埠轉發或遠端指令執行。
無需帳號、無需設定檔，一個極簡的公共中繼伺服器只負責會合（rendezvous）。它絕不會解密你的流量。

受 [magic-wormhole](https://github.com/magic-wormhole/magic-wormhole) 啟發，
qvole 改用 QUIC 作為傳輸層，並組合三種成熟易懂的基本元件來運作：

### SPAKE2：密碼驗證金鑰交換

每個對端都送出一個遮蔽後的橢圓曲線點。兩者由此推導出一個任何人都無法算出的共享密鑰，
連中繼伺服器也算不出來。這個代碼*就是*憑證，而握手完成後它的任務就結束了。

### QUIC：在 UDP 上的串流多工

單一 UDP socket 即可支援多條獨立的位元組串流，沒有隊頭阻塞（head-of-line blocking），
內建 TLS 1.3，每條串流各自獨立流量控制。每一條連線永遠都是加密且經過驗證的。

### UDP 打洞：NAT 穿透

位於各自獨立 NAT 後方的兩個對端，會同時向彼此的對外位址送出封包。每個送出的封包
都會打開一個小孔（pinhole），讓另一方的封包得以通過。這正是視訊通話與電信會議
所使用的 NAT 穿透技術，適用於許多常見的 NAT 設定。

### 加密交接

這些概念都不是新東西，訣竅在於如何把它們組合起來應用在這個特定場景。

關鍵在於：TLS 憑證指紋會與 SPAKE2 遮蔽點一起交換，並透過由 PAKE 推導出的確認 HMAC
綁定到共享密鑰上，藉此驗證 TLS 工作階段。

這讓我們能從低熵的代碼，在幾秒鐘內完成加密交接，成為具備前向保密、且經由上述指紋
驗證的強健 256 位元 TLS 工作階段：

```
共享代碼（52 位元）
          │
          ▼
  SPAKE2 PAKE ──► 工作階段金鑰 + 對端的憑證指紋
          │
          ▼
  UDP 打洞 ──► 直連 UDP 路徑
          │
          ▼
  QUIC/TLS 1.3 ──► 釘選指紋驗證
          │
          ▼
  串流 ──► 資料（pipe、exec、tunnel）
```

握手完成後，中繼伺服器什麼也看不到。它無法推導金鑰、無法解密流量，也無法冒充任何一個對端。

即使日後有人暴力破解或得知了代碼，並錄下了所有中繼流量，他們最多也只能還原出憑證指紋，
無法取得保護實際資料的 TLS QUIC 工作階段金鑰。

**名稱由此而來：田鼠（vole）會在障礙物下方打洞鑽行，而 `qvole` 則在 NAT 之下鑽出 QUIC 隧道。**

## 設計哲學

`qvole` 刻意保持極簡。它只做一件事：在兩個對端之間建立臨時的加密隧道。
諸如 SOCKS 或 HTTP 代理、檔案傳輸指令等功能都被刻意排除，因為現有工具
與 `qvole` 組合使用時，已經能勝任這些角色。

`qvole` 不會是你的 SOCKS 代理。SSH 在這方面做得很好。但 `qvole` 會為你提供一條可以跑 SSH 的隧道。

由於只專注於 stdin/stdout 串流與 TCP 連接埠轉發，`qvole` 能自然地與你系統中的其他工具組合運用。

**這就是 Unix 哲學：每個工具只做一件事，再用管道把它們串起來。**

`qvole` 有其主觀偏好，也坦承算是小眾工具。它解決的是我自己的需求：
我對開放連接埠相當偏執，我需要一種能隨需連到我的機器、卻又不必暴露它們的方法。
如果這個工具剛好也對其他人有用，那就太好了！

### 協定

**[完整協定規格 → PROTOCOL.md](../../docs/PROTOCOL.md)**

握手在幾秒內完成：先透過 UDP 中繼伺服器以 SPAKE2 PAKE 進行對端探索與驗證，
接著用 UDP 打洞達成直連 NAT 穿透，最後建立釘選自簽憑證的 QUIC 連線。

關於連線生命週期的逐步說明，以及線上傳輸的資料封包（datagram）結構，請參閱上述文件。

### 安全性

**[完整安全性文件 → SECURITY.md](../../SECURITY.md)**

`qvole` 的安全模型是一套加密交接流程：SPAKE2 PAKE 以共享代碼驗證雙方對端，
接著以釘選憑證指紋的 TLS 1.3 保護直連的 QUIC 資料路徑。中繼伺服器被視為不可信任：
它無法推導工作階段金鑰、無法解密流量，也無法冒充任何對端。

上述文件中有詳細剖析，涵蓋威脅模型、協定安全分析、設計取捨與限制。

### 限制與取捨

#### 網路連線
* **網路支援不一致**：我在家用寬頻、機場 Wi-Fi 與手機熱點上測試過 `qvole`。能否連上取決於網路設定。多數時候可以連上，少數情況下則不行。沒有備援方案，所以你需要其他連線方式。
* **沒有資料中繼備援**：我們刻意不實作 TURN 中繼，以維持中繼伺服器的簡單性，並確保它永遠不會碰觸使用者資料。這代表當雙方對端都位於嚴格的對稱型 NAT 之後、導致 UDP 打洞失敗時，連線就會失敗；沒有資料中繼的備援路徑。
* **僅限 UDP**：此協定完全依賴 UDP，沒有 TCP 備援。在嚴格封鎖所有對外 UDP 流量的環境中，將無法建立連線。

#### 營運限制
* **中繼伺服器是單點故障**：沒有中繼伺服器，對端之間就無法互相發現或交換握手資料。若中繼伺服器無法使用，就無法建立任何連線。
* **僅限兩個對端**：此協定每條連線只支援恰好兩個對端。不支援多方會議。

#### 效能與擴充性
* **無狀態中繼設計**：中繼伺服器不實作訊息佇列或持久化儲存。這讓它能保持極低的開銷，並防範狀態耗盡攻擊，但也代表若用戶端在握手期間暫時無法連上，訊息可能會遺失。
* **速率限制**：我們實作嚴格的速率限制（例如每個用戶端每秒 10 則訊息），以防止中繼伺服器被濫用。這確保了社群的穩定性，但可能限制極高頻率的訊令（signaling）。

---

## Go 函式庫

在自己的 Go 程式中使用 `qvole`：

```go
import "github.com/fernjager/qvole"
```

```go
// 簡易的點對點管道
conn, _ := qvole.Dial(ctx,
    qvole.WithCode("my-secret"),
    qvole.WithRelay("relay.qvole.dev:9009"),
)
conn.Write([]byte("hello"))
conn.Close()

// 遠端指令執行
qvole.Exec(ctx,
    qvole.WithCode("secret"),
    qvole.WithRelay("relay.qvole.dev:9009"),
    qvole.WithCommand("uptime"), qvole.WithCmdMode(true),
)

// 連接埠轉發：Alice 在本機監聽 8080，並轉發到 Bob 的 :80。
// Bob 必須用 qvole.WithAllowForward("localhost:80") 將該目標加入白名單。
qvole.Tunnel(ctx,
    qvole.WithCode("secret"),
    qvole.WithRelay("relay.qvole.dev:9009"),
    qvole.WithLocalTunnel("8080:localhost:80"),
)

// Bob（對端）：白名單列出 Alice 可以透過這台機器存取的目標。
qvole.Tunnel(ctx,
    qvole.WithCode("secret"),
    qvole.WithRelay("relay.qvole.dev:9009"),
    qvole.WithAllowForward("localhost:80"),
)

// 執行自己的中繼伺服器
go relay.RunRelay(ctx, ":9009")
conn, _ = qvole.Dial(ctx,
    qvole.WithCode("secret"), qvole.WithRelay("localhost:9009"),
)
```

完整的 API 參考、選項參考、統計追蹤與更多範例，請見 **[LIBRARY.md](../../docs/LIBRARY.md)**。

## 子指令

```
用法：
  qvole pipe      [--code CODE] [--stats]                            # 管道模式
  qvole tunnel    -L request [-L ...] [-R ...] [--code CODE]         # 連接埠轉發
  qvole exec      --cmd cmd [--code CODE]                            # 遠端指令
  qvole relay     [--listen :9009]                                   # 中繼伺服器

常見的指令旗標（pipe / exec / tunnel）：
  --code CODE        連線代碼（或 $QVOLE_CODE）
  --relay addr       中繼伺服器位址（host:port）
  --debug            將詳細的除錯記錄輸出到 stderr
```

### 管道

經由最多兩條 QUIC 串流進行 stdin/stdout 管道傳輸（stdin 是終端機時一條，stdin 是管線輸入時兩條）。雙向傳輸。
若未提供代碼，會自動產生一個並印到 `stderr`。

| 旗標 | 說明 |
|------|-------------|
| `--relay addr` | 中繼伺服器位址（host:port） |
| `--stats` | 將傳輸統計記錄輸出到 stderr |
| `--debug` | 將詳細的除錯記錄輸出到 stderr |

**範例：**

握手完成後，`qvole` 會開啟直連的 P2P 資料傳輸，不再有中繼伺服器的瓶頸。
每一條連線都是 QUIC 工作階段，所以你不只可以傳檔，還能在上面跑管道。

```bash
# 監看吞吐量：使用內建的 --stats（不需要 pv）
alice$ qvole pipe --stats < largefile.dat
bob$   QVOLE_CODE=CODE qvole pipe --stats > received.dat

# --stats 輸出到 stderr 的範例：
# ↑ 5.20 MB (2.60 MB/s)  ↓ 10.40 MB (5.20 MB/s)
# ↑ 10.40 MB (2.60 MB/s)  ↓ 20.80 MB (5.20 MB/s)
# ...
# Total: ↑ 50.00 MB  ↓ 100.00 MB

# 磁碟複製：透過 P2P 隧道複製遠端磁碟
alice$ sudo dd if=/dev/sda | qvole pipe
bob$   QVOLE_CODE=CODE qvole pipe | sudo dd of=/dev/sdb

# 資料庫備份：透過隧道傳送 dump 資料
alice$ pg_dump mydb | gzip | qvole pipe
bob$   QVOLE_CODE=CODE qvole pipe | gunzip | psql mydb

# 視訊攝影機／媒體串流：從擷取裝置串流
alice$ ffmpeg -i /dev/video0 -f mpegts - | qvole pipe
bob$   QVOLE_CODE=CODE qvole pipe | ffplay -

# 遠端日誌追蹤：追蹤遠端系統的日誌
alice$ journalctl -f | qvole pipe
bob$   QVOLE_CODE=CODE qvole pipe

# Gzip 壓縮：在傳輸過程中壓縮資料
alice$ tar czf - dir/ | qvole pipe
bob$   QVOLE_CODE=CODE qvole pipe | tar xzf -

# 遠端封包擷取：遠端擷取網路流量
alice$ tcpdump -w - -i eth0 | qvole pipe
bob$   QVOLE_CODE=CODE qvole pipe > capture.pcap

# 剪貼簿同步：在機器之間分享內容片段
alice$ qvole pipe | pbcopy
bob$   pbpaste | QVOLE_CODE=CODE qvole pipe

# 簡易 P2P 聊天：直接的雙向文字通訊（用 cat 橋接終端機）
alice$ cat | qvole pipe
bob$   cat | QVOLE_CODE=CODE qvole pipe
```

#### Shell 輔助函式
```bash
# Shell 輔助函式：預先分享代碼，在受信任的機器之間隨時可連
# 產生一次代碼，然後加到兩台機器的 ~/.bashrc：
$ openssl rand -base64 24
$ echo 'export QVOLE_CODE=<generated-code>' >> ~/.bashrc
$ echo "qsend() { tar czf - \"\$@\" | qvole pipe --stats; }" >> ~/.bashrc
$ echo "qrecv() { qvole pipe --stats | tar xzf -; }" >> ~/.bashrc
$ echo "alias qp='qvole pipe --stats'" >> ~/.bashrc

alice$ qsend file.dat                              # 傳送檔案（gzip 壓縮）
alice$ qsend dir/                                  # 傳送資料夾（gzip 壓縮）
bob$   qrecv                                       # 接收並解壓縮
alice$ qsend file1.dat dir/                        # 傳送多個
bob$   qrecv                                       # 全部接收

alice$ arecord -f cd | qp                          # 透過 P2P 串流麥克風（macOS：ffmpeg -f avfoundation -i ":0" -f mp3 - | qp）
bob$   qp | aplay -f cd                            # 收聽 alice 的麥克風（或：qp | ffplay -i pipe:0 -nodisp）
```

### 執行

一端直接在本機執行指令，並將其 stdin/stdout 橋接到 QUIC 串流；另一端則作為單純的管道運作（stdin→串流→stdout）。

| 旗標 | 說明 |
|------|-------------|
| `--cmd <cmd>` | 在本機執行 `<cmd>`，並將其 stdin/stdout 經由隧道傳輸 |
| `--code CODE`  | 連線代碼（或 `$QVOLE_CODE`） |
| `--relay addr` | 中繼伺服器位址（host:port） |
| `--debug` | 將詳細的除錯記錄輸出到 stderr |

與 pipe 子指令不同（pipe 的串流方向取決於 stdin 是否為終端機；管線輸入的 stdin 會開啟對外串流，終端機 stdin 則不會），
`exec` 端永遠開啟串流，而 pipe 端永遠接受。只有帶 `--cmd` 的那一端會執行指令，另一端只是連線。

**範例：**

臨時的遠端伺服器管理，以及在網路受限的環境或客戶端點，不需分發 SSH 金鑰即可存取服務。

```bash
# 遠端 shell：透過隧道取得完整的互動式 shell
alice$ qvole exec --cmd "script -q bash /dev/null"
bob$   QVOLE_CODE=CODE qvole exec

# 遠端監控：使用 script -q 執行 TUI 應用程式
alice$ qvole exec --cmd "script -q htop /dev/null"
bob$   QVOLE_CODE=CODE qvole exec

# 遠端磁碟用量：遠端檢視磁碟用量
alice$ qvole exec --cmd "script -q 'ncdu /' /dev/null"
bob$   QVOLE_CODE=CODE qvole exec

# 遠端日誌檢視：檢視遠端系統的日誌
alice$ qvole exec --cmd "journalctl -n 100 --no-pager"
bob$   QVOLE_CODE=CODE qvole exec

# 遠端系統資訊：查看遠端系統詳細資料
alice$ qvole exec --cmd "uname -a; uptime"
bob$   QVOLE_CODE=CODE qvole exec

# Docker 管理：在遠端主機上執行 docker 指令
alice$ qvole exec --cmd "docker ps"
bob$   QVOLE_CODE=CODE qvole exec

# 遠端封包擷取：將擷取內容串流到本機 tcpdump
alice$ qvole exec --cmd "tcpdump -w - -i eth0"
bob$   QVOLE_CODE=CODE qvole exec | tcpdump -r -

# 共享終端機工作階段：透過 tmux 進行協作
alice$ qvole exec --cmd "script -q 'tmux new -A -s qvole' /dev/null"
bob$   QVOLE_CODE=CODE qvole exec
```

### <a id="tunnel"></a>隧道

在 QUIC 隧道之上進行 TCP 連接埠隧道化，仿照 SSH 的 `-L` 與 `-R`。

| 旗標 | 說明 |
|------|-------------|
| `-L [laddr:]lport:raddr:rport` | 在本機 `[laddr:]lport` 監聽，轉發到遠端 `raddr:rport`（由對端以 `-aF` 允許） |
| `-R [raddr:]rport:laddr:lport` | 對端在遠端 `[raddr:]rport` 監聽，轉發到本機 `laddr:lport`（由對端以 `-aL` 允許） |
| `-aL addr:port` | 允許對端在此位址上開啟監聽連接埠（允許對端的 `-R`；可重複） |
| `-aF addr:port` | 允許對端透過你連到 addr:port（允許對端的 `-L`；可重複） |
| `-a` | 允許對端的**所有**隧道請求（不安全）：對端可以透過你的機器開啟任意連接埠並連到任意位址 |
| `--code CODE` | 連線代碼（或 `$QVOLE_CODE`） |
| `--relay addr` | 中繼伺服器位址（host:port） |
| `--debug` | 將詳細的除錯記錄輸出到 stderr |

多個 `-L` 與 `-R` 旗標可以組合使用。預設不接受任何請求：
你的 `-L` 請求需要對端以 `-aF <target>` 執行，你的 `-R` 請求需要對端以 `-aL <listen addr>` 執行。

對端的接受訊號是以「端」為單位，而非以「請求」為單位：若對端的白名單未涵蓋你的某個請求，
你的監聽端仍可能綁定成功，但經由它的每一條連線都會在對端被拒絕（對端會記錄 `Tunnel rejected: … (add -aF …)`）。
當對端執行時完全沒有白名單，你的這一端會記錄對端必須執行的確切旗標。

`-aL`/`-aF` 中主機為空的條目（例如 `:8081`）會比對該連接埠上的**任何**主機，
因此 `-aL :8081` 允許對端綁定 `0.0.0.0:8081`，`-aF :8081` 允許對端透過你的機器連到任何 `host:8081`。
除非刻意要用萬用字元，否則請使用明確的主機（`127.0.0.1:8081`）。

```bash
# Alice 監聽 8080，轉發到 Bob 的 :80（Bob 需要為該目標加上 -aF）
alice$ qvole tunnel -L 8080:localhost:80
bob$   QVOLE_CODE=CODE qvole tunnel -aF localhost:80

# Alice 請 Bob 監聽 9022 並轉發回 Alice 的 :22（Bob 需要為該連接埠加上 -aL）
alice$ qvole tunnel -R 9022:localhost:22
bob$   QVOLE_CODE=CODE qvole tunnel -aL 127.0.0.1:9022

# 組合：Alice 在 :8080 取得 Bob 的 :80，同時 Bob 在 :9022 取得 Alice 的 :22
alice$ qvole tunnel -L 8080:localhost:80 -R 9022:localhost:22
bob$   QVOLE_CODE=CODE qvole tunnel -aL 127.0.0.1:9022 -aF localhost:80
```

| 旗標形式 | 意義 |
|-----------|---------|
| `-L lport:raddr:rport` | 在 `127.0.0.1:lport` 監聽，轉發到對端的 `raddr:rport` |
| `-L laddr:lport:raddr:rport` | 在 `laddr:lport` 監聽，轉發到對端的 `raddr:rport` |
| `-R rport:laddr:lport` | 對端在 `127.0.0.1:rport` 監聽，轉發到你這一端的 `laddr:lport` |
| `-R raddr:rport:laddr:lport` | 對端在 `raddr:rport` 監聽，轉發到你這一端的 `laddr:lport` |

**範例：**

TCP 流量轉發，無需 root、核心模組、TUN 裝置或持久化狀態。
恰好兩個對端之間的點對點傳輸；跨重啟仍具備前向保密。

```bash
# Postgres：遠端資料庫存取
alice$ qvole tunnel -L 5432:localhost:5432
bob$   QVOLE_CODE=CODE qvole tunnel -aF localhost:5432

# 遠端桌面：透過 P2P 連線隧道化 VNC
alice$ qvole tunnel -L 5900:localhost:5900
bob$   QVOLE_CODE=CODE qvole tunnel -aF localhost:5900

# 遠端 Docker daemon：不需要對外開放的 TCP socket
alice$ qvole tunnel -L 2375:localhost:2375
bob$   QVOLE_CODE=CODE qvole tunnel -aF localhost:2375

# SOCKS 代理：透過對端的網路路由流量
alice$ ssh -D 1080 localhost -N                          #啟動本機 SOCKS 代理
alice$ qvole tunnel -R 1080:localhost:1080               #將它暴露給 Bob
bob$   QVOLE_CODE=CODE qvole tunnel -aL 127.0.0.1:1080   #Bob 現在在 :1080 有 SOCKS

# 監控組合：連到位於 NAT 之後的 Prometheus／Grafana
alice$ qvole tunnel -L 9090:localhost:9090 -L 3000:localhost:3000
bob$   QVOLE_CODE=CODE qvole tunnel -aF localhost:9090 -aF localhost:3000

# 媒體伺服器：在遠端電視上觀看 Plex／Jellyfin
alice$ qvole tunnel -L 32400:localhost:32400
bob$   QVOLE_CODE=CODE qvole tunnel -aF localhost:32400

# SSH 逃生門：在嚴格鎖定的機器上暴露 sshd
# Alice 在 :22 上運行 sshd 並授權對端存取（-aF）。斷線時用循環重連（或參見 Persistent Tunnels 的 systemd 單元）。
alice$ while true; do QVOLE_CODE=CODE qvole tunnel -aF localhost:22; sleep 1; done
# Bob 選擇任意方便的本機連接埠，即可連到 Alice 的 sshd
bob$   QVOLE_CODE=CODE qvole tunnel -L 2222:localhost:22
bob$   ssh -p 2222 localhost                              #Bob 連到 Alice 的 sshd

# 遊戲伺服器：在 NAT 之後架設 Minecraft 伺服器
alice$ qvole tunnel -L 25565:localhost:25565
bob$   QVOLE_CODE=CODE qvole tunnel -aF localhost:25565

# Redis：存取遠端 Redis 執行個體
alice$ qvole tunnel -L 6379:localhost:6379
bob$   QVOLE_CODE=CODE qvole tunnel -aF localhost:6379

# Web 開發預覽：向對端暴露本機開發伺服器
alice$ npm run dev                                       #在 :3000 上執行
alice$ qvole tunnel -R 3000:localhost:3000               #Alice 將她的 :3000 暴露給 Bob
bob$   QVOLE_CODE=CODE qvole tunnel -aL 127.0.0.1:3000   #Bob 現在可以瀏覽 localhost:3000
```


#### 持久隧道
```bash
# 自動重連：斷線時以固定代碼重新連線
alice$ while true; do QVOLE_CODE=CODE qvole tunnel -L 8080:localhost:80; sleep 1; done

# 若需長期部署，請產生一個強健的隨機代碼：
$ openssl rand -base64 24

# 或者，使用 systemd 持久化來讓隧道永遠保持運作
# /etc/systemd/system/qvole-tunnel.service
[Unit]
Description=qvole P2P 隧道
After=network-online.target

[Service]
Type=simple
# 先產生一個強健的代碼：openssl rand -base64 24
Environment=QVOLE_CODE=<generated-code>
ExecStart=/usr/local/bin/qvole tunnel -L 8080:localhost:80
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

### 環境變數

#### 用戶端變數（pipe / exec / tunnel）

| 變數 | 預設值 | 說明 |
|----------|---------|-------------|
| `QVOLE_CODE` | - | 連線代碼（`--code` 的替代方式） |
| `QVOLE_KDF_ITERATIONS` | 600000 | 將代碼轉換為純量（scalar）時所用的 PBKDF2 疊代次數 |
| `QVOLE_KEEPALIVE_MS` | 5000 | QUIC keepalive 間隔 |
| `QVOLE_IDLE_TIMEOUT_MS` | 120000 | 斷線前的 QUIC 閒置逾時 |
| `QVOLE_HANDSHAKE_TIMEOUT_MS` | 30000 | 握手逾時 |
| `QVOLE_MAX_STREAMS` | 100 | QUIC 同時串流數上限 |
| `QVOLE_FORWARD_MAX_STREAMS` | 200 | 轉發隧道串流數上限 |
| `QVOLE_PUNCH_TIMEOUT_MS` | 10000 | UDP 打洞逾時 |
| `QVOLE_EXCHANGE_DEADLINE_MS` | 90000 | SPAKE2 交換截止時間 |
| `QVOLE_SPAKE2_RESEND_MS` | 2000 | SPAKE2 訊息重送間隔 |
| `QVOLE_CONFIRM_RESEND_MS` | 2000 | 確認訊息重送間隔 |
| `QVOLE_EXCHANGE_READ_DEADLINE_MS` | 1000 | 中繼伺服器讀取輪詢間隔 |
| `QVOLE_REG_INTERVAL_MS` | 30000 | 中繼伺服器註冊間隔 |
| `QVOLE_DIAL_TIMEOUT_MS` | 10000 | 隧道 TCP 連線（dial）逾時 |
| `QVOLE_STREAM_IDLE_TIMEOUT_MS` | 0 | 閒置轉發流被回收前的閒置視窗（0 表示停用；依流量重新整理，因此主動傳輸不會被截斷） |
| `QVOLE_EXEC_DRAIN_TIMEOUT_MS` | 5000 | 指令結束後的 Exec 排空（drain）逾時 |
| `QVOLE_INITIAL_STREAM_WINDOW` | 1048576 | QUIC 串流接收視窗 |
| `QVOLE_INITIAL_CONNECTION_WINDOW` | 4194304 | QUIC 連線接收視窗 |

#### 中繼伺服器變數（`relay` 子指令）

| 變數 | 預設值 | 說明 |
|----------|---------|-------------|
| `QVOLE_RELAY_MAX_ROOMS` | 10000 | 同時存在的房間數上限 |
| `QVOLE_RELAY_MAX_ROOMS_PER_IP` | 10 | 每個 IP 位址的房間數上限 |
| `QVOLE_RELAY_MAX_CLIENTS_HARD` | 20 | 每個房間的用戶端硬性上限 |
| `QVOLE_RELAY_MSG_RATE` | 10 | 每個用戶端在每個速率視窗內的訊息數上限 |
| `QVOLE_RELAY_RATE_WINDOW_MS` | 1000 | 訊息限速的速率視窗時間 |
| `QVOLE_RELAY_TTL_MS` | 60000 | 用戶端過期前的註冊 TTL（1 分鐘） |
| `QVOLE_RELAY_CLEANUP_INTERVAL_MS` | 60000 | 清理過期註冊的間隔 |
| `QVOLE_RELAY_WORKERS` | 4 | worker goroutine 的數量 |
| `QVOLE_RELAY_PKT_CHAN_BUF` | 256 | 封包通道的緩衝區大小 |
| `QVOLE_RELAY_WRITE_DEADLINE_MS` | 500 | UDP 寫入截止時間 |
| `QVOLE_RELAY_WRITE_POOL` | 256 | 中繼伺服器對外寫入的寫入集區大小 |
| `QVOLE_RELAY_STATS_INTERVAL_MS` | 300000 | 統計記錄的間隔 |

### 隱私權與條款

[公共中繼伺服器的隱私權政策與服務條款 &rarr; PRIVACY.md](../../docs/PRIVACY.md)

---
