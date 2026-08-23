<p align="center"><img src="../../assets/logo.svg" width="96" alt="qvole"></p>

<h1 align="center">qvole</h1>
<p align="center">
  <a href="https://go.dev"><img src="https://img.shields.io/badge/go-1.27-blue" alt="Go Version"></a>
  <a href="https://github.com/fernjager/qvole/actions/workflows/ci.yml"><img src="https://github.com/fernjager/qvole/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="../../LICENSE"><img src="https://img.shields.io/badge/license-MIT-green" alt="License"></a>
</p>
<p align="center"><em>Túneles rápidos sobre QUIC, perforando NATs.</em></p>

<p align="center">
  <a href="../../README.md">English</a> &nbsp;|&nbsp;
  <a href="./README.zh-CN.md">简体中文</a> &nbsp;|&nbsp;
  <a href="./README.zh-TW.md">繁體中文</a> &nbsp;|&nbsp;
  <a href="./README.es.md">Español</a> &nbsp;|&nbsp;
  <a href="./README.ru.md">Русский</a>
</p>

Dos máquinas, ambas detrás de un NAT. Comparte un código corto con tu par y tendrás un conducto cifrado entre ambos.

Reenvía puertos, transfiere archivos, ejecuta comandos remotos y canalizaciones. Un único binario, sin archivos de configuración, sin preparativos.

<p align="center"><img src="../../assets/demo.gif" alt="demostración de qvole"></p>

---

## Ejemplos de inicio rápido

¡Consulta los [ejemplos](#canalizaciones) más abajo para inspirarte!

```bash
# Enviar un archivo
alice$ qvole pipe < in                         # imprime el código de conexión, espera a bob
bob$   QVOLE_CODE=CODE qvole pipe > out        # bob se conecta y recibe el archivo de alice

# Enviar un directorio
alice$ tar czf - dir/ | qvole pipe             # alice transmite el tar de dir/ a bob
bob$   QVOLE_CODE=CODE qvole pipe | tar xzf -  # bob recibe y extrae dir/

# Reenvío de puertos
alice$ qvole tunnel \
    -L 8080:localhost:80 \
    -R 2222:localhost:22
# bob permite las solicitudes de túnel
bob$   QVOLE_CODE=CODE qvole tunnel \
    -aF localhost:80 \
    -aL 127.0.0.1:2222
alice$ curl localhost:8080    # llega al :80 de bob
bob$   ssh -p 2222 localhost  # llega al sshd de alice

# Comando remoto (alice ejecuta el comando, bob se conecta)
alice$ qvole exec --cmd "uptime"                    # se ejecuta en alice, bob ve la salida
alice$ qvole exec --cmd "script -q bash /dev/null"  # alice le da a bob un shell remoto
bob$   QVOLE_CODE=CODE qvole exec

# Ejecutar un servidor relay privado (UDP)
relay$ qvole relay --listen :9009
```

---

## Instalación

**Una sola línea con curl:**

```bash
curl -fsSL https://get.qvole.dev | sh
```

**Desde el código fuente:**

```bash
git clone https://github.com/fernjager/qvole && cd qvole && make && sudo make install
```

(Requiere Go 1.27+)

---

## El problema

Ejecuto servicios en todas partes: un homelab, VPS, un router de viaje con una tarjeta SIM de datos en un velero.
Las opciones habituales (VPN, túneles SSH inversos, reenvío de puertos) requieren certificados, distribución de claves
o puertos accesibles. Cada puerto abierto es un posible zero-day,
y un servidor personal que recibe parches en el mejor de los casos no puede seguir el ritmo.

Prefiero vincular los servicios a localhost, no dejar ningún puerto de entrada abierto y alcanzarlos
bajo demanda mediante un código compartido. Eso también resuelve tres problemas más difíciles:
el descubrimiento (ambos pares están detrás de un NAT), la autenticación (sin PKI, solo un código)
y el transporte (tunelizar TCP dentro de TCP es otro tipo de rotura).

## La solución

`qvole` es un único binario que crea túneles P2P cifrados y directos entre
dos máquinas. Compartes un código y, en cuestión de segundos, tienes una conexión QUIC
con canalizaciones stdin/stdout, reenvío de puertos TCP o ejecución de comandos remotos. Sin
cuentas, sin archivos de configuración: un relay público mínimo se encarga únicamente de la rendezvous. Nunca descifra tu tráfico.

Inspirado en [magic-wormhole](https://github.com/magic-wormhole/magic-wormhole),
qvole utiliza QUIC como transporte en su lugar y funciona componiendo tres primitivas
bien conocidas:

### SPAKE2: intercambio de claves autenticado por contraseña

Cada par envía un punto de curva elíptica cegado. A partir del par
derivan un secreto compartido que nadie más puede calcular, ni siquiera el relay. El código
*es* la credencial y, tras el handshake, su papel ha terminado.

### QUIC: multiplexación de flujos sobre UDP

Un único socket UDP admite múltiples flujos de bytes independientes sin bloqueo head-of-line,
TLS 1.3 integrado y control de flujo independiente por flujo. Cada conexión está siempre
cifrada y siempre autenticada.

### Perforación de NAT con UDP (UDP Hole Punching)

Dos pares detrás de NAT independientes envían paquetes a la dirección externa del otro
simultáneamente. Cada paquete saliente abre un agujero que deja pasar el paquete del otro
lado. Es la misma técnica de travesía de NAT que usan las videollamadas y las teleconferencias, y que
funciona en muchas configuraciones NAT habituales.

### El traspaso criptográfico

Ninguno de estos conceptos es nuevo; el truco está en cómo se combinan para esta aplicación concreta.

El aspecto clave es que la huella digital del certificado TLS se intercambia junto con el punto cegado de SPAKE2 y luego se vincula al secreto compartido mediante el HMAC de confirmación derivado del PAKE, autenticando así la sesión TLS.

Esto permite un traspaso criptográfico desde un código de baja entropía a una sesión TLS de 256 bits con secreto forward, autenticada por dicha huella digital, todo en unos pocos segundos:

```
Código compartido (52 bits)
       │
       ▼
  SPAKE2 PAKE ──► claves de sesión + huella del certificado del par
       │
       ▼
  Hole punch ──► ruta UDP directa
       │
       ▼
  QUIC/TLS 1.3 ──► verificación de la huella fijada
       │
       ▼
  Flujos ──► datos (pipe, exec, tunnel)
```

Tras el handshake, el relay no ve nada. No puede derivar claves, descifrar tráfico ni hacerse pasar por un par.

Incluso si alguien consigue más tarde la clave por fuerza bruta o la aprende y graba todo el tráfico del relay, como máximo podría recuperar la huella del certificado, no las claves de sesión TLS/QUIC que protegen los datos reales.

**De ahí el nombre: un topillo excava bajo los obstáculos, y `qvole` excava túneles QUIC bajo los NAT.**

## Filosofía de diseño

`qvole` es intencionadamente minimalista. Hace una sola cosa: establecer un túnel cifrado
ad-hoc entre dos pares. Funciones como los proxies SOCKS o HTTP, los comandos de
transferencia de archivos, etc., están deliberadamente ausentes porque las herramientas existentes ya cubren esos roles cuando se combinan con `qvole`.

`qvole` no será tu proxy SOCKS. SSH lo hace bien. Pero `qvole` te dará el túnel por el que ejecutar SSH.

Al limitarse a flujos stdin/stdout y al reenvío de puertos TCP, `qvole` se combina de forma natural
con el resto de tu sistema.

**Esta es la filosofía Unix: cada herramienta hace una sola cosa, y las canalizaciones las conectan.**

`qvole` es dogmático y, reconozco, un poco de nicho. Rasca mi propia comezón:
soy paranoico con los puertos abiertos y necesitaba una forma de llegar a mis máquinas bajo demanda
sin exponerlas. Si esta herramienta le resulta útil a alguien más, ¡estupendo!

### Protocolo

**[Especificación completa del protocolo → PROTOCOL.md](../../docs/PROTOCOL.md)**

El handshake se completa en segundos: SPAKE2 PAKE sobre un relay UDP para el descubrimiento de pares
y la autenticación; luego, perforación de agujeros UDP para la travesía directa de NAT; y después una conexión
QUIC con certificados autofirmados fijados (pinned).

Consulta lo anterior para una descripción paso a paso del ciclo de vida de una conexión, así como de la estructura de los datagramas que viajan por la red.

### Seguridad

**[Documentación completa de seguridad → SECURITY.md](../../SECURITY.md)**

El modelo de seguridad de `qvole` es un traspaso criptográfico: SPAKE2 PAKE autentica a ambos
pares a partir de un código compartido; luego, TLS 1.3 con huellas de certificado fijadas
protege la ruta de datos QUIC directa. Se asume que el relay no es de confianza: no puede derivar
claves de sesión, descifrar tráfico ni hacerse pasar por un par.

Consulta lo anterior para un análisis detallado en el que cubrimos el modelo de amenazas, el análisis de seguridad del protocolo,
las decisiones de diseño y las limitaciones.

### Limitaciones y compensaciones

#### Conectividad de red
* **Soporte de red inconsistente**: probé `qvole` con banda ancha doméstica, wifi de aeropuerto y hotspots móviles. Que se conecte o no depende de la configuración de la red. La mayoría de las veces funcionó; en casos raros, no. No hay respaldo, así que necesitas otra forma de conectarte.
* **Sin respaldo de relay de datos**: no implementamos un relay TURN para mantener el servidor relay simple y evitar que toque los datos del usuario. Esto significa que las conexiones fallan cuando ambos pares están detrás de NAT simétricos restrictivos que impiden la perforación de agujeros UDP; no hay ruta de respaldo de relay de datos.
* **Solo UDP**: el protocolo se basa puramente en UDP sin respaldo TCP. Los entornos muy restrictivos que bloquean todo el tráfico UDP saliente impedirán el establecimiento de la conexión.

#### Restricciones operativas
* **El relay es un punto único de fallo**: los pares no pueden descubrirse entre sí ni intercambiar material del handshake sin el relay. Si el relay no está disponible, no se puede establecer ninguna conexión.
* **Solo dos pares**: el protocolo admite exactamente dos pares por conexión. No se admiten conferencias multipartitas.

#### Rendimiento y escalabilidad
* **Diseño de relay sin estado**: el relay no implementa colas de mensajes ni almacenamiento persistente. Esto le permite mantener una sobrecarga mínima y protege contra ataques de agotamiento de estado, pero significa que los mensajes pueden perderse si un cliente está temporalmente inalcanzable durante un handshake.
* **Limitación de velocidad**: implementamos una limitación de velocidad estricta (p. ej., 10 msg/s por cliente) para evitar el abuso del relay. Esto garantiza la estabilidad para la comunidad, pero puede limitar la señalización de muy alta frecuencia.

---

## Biblioteca Go

Usa `qvole` en tus propios programas Go:

```go
import "github.com/fernjager/qvole"
```

```go
// Canalización simple entre pares
conn, _ := qvole.Dial(ctx,
    qvole.WithCode("my-secret"),
    qvole.WithRelay("relay.qvole.dev:9009"),
)
conn.Write([]byte("hello"))
conn.Close()

// Ejecución de comandos remotos
qvole.Exec(ctx,
    qvole.WithCode("secret"),
    qvole.WithRelay("relay.qvole.dev:9009"),
    qvole.WithCommand("uptime"), qvole.WithCmdMode(true),
)

// Reenvío de puertos: Alice escucha en 8080 localmente y reenvía al :80 de Bob.
// Bob debe incluir ese destino en su lista blanca con qvole.WithAllowForward("localhost:80").
qvole.Tunnel(ctx,
    qvole.WithCode("secret"),
    qvole.WithRelay("relay.qvole.dev:9009"),
    qvole.WithLocalTunnel("8080:localhost:80"),
)

// Bob (lado par): incluye en la lista blanca lo que Alice puede alcanzar a través de esta máquina.
qvole.Tunnel(ctx,
    qvole.WithCode("secret"),
    qvole.WithRelay("relay.qvole.dev:9009"),
    qvole.WithAllowForward("localhost:80"),
)

// Ejecuta tu propio relay
go relay.RunRelay(ctx, ":9009")
conn, _ = qvole.Dial(ctx,
    qvole.WithCode("secret"), qvole.WithRelay("localhost:9009"),
)
```

Consulta **[LIBRARY.md](../../docs/LIBRARY.md)** para la referencia completa de la API, la referencia de opciones, el seguimiento de estadísticas y más ejemplos.

## Subcomandos

```
Uso:
  qvole pipe      [--code CODE] [--stats]                            # modo pipe
  qvole tunnel    -L request [-L ...] [-R ...] [--code CODE]         # reenvío de puertos
  qvole exec      --cmd cmd [--code CODE]                            # comando remoto
  qvole relay     [--listen :9009]                                   # servidor relay

Opciones comunes de los comandos (pipe / exec / tunnel):
  --code CODE        código de conexión (o $QVOLE_CODE)
  --relay addr       dirección del relay (host:port)
  --debug            registro de depuración verboso en stderr
```

### Canalizaciones

Canalización stdin/stdout sobre hasta dos flujos QUIC (uno cuando stdin es una terminal, dos cuando stdin está canalizado). Bidireccional.
Cuando no se proporciona un código, se genera uno y se imprime en `stderr`.

| Indicador | Descripción |
|------|-------------|
| `--relay addr` | Dirección del relay (host:port) |
| `--stats` | Registra estadísticas de transferencia en stderr |
| `--debug` | Registro de depuración verboso en stderr |

**Ejemplos:**

`qvole` abre una transferencia de datos P2P directa sin cuello de botella del relay una vez completado el handshake.
Cada conexión es una sesión QUIC, así que puedes canalizar datos a través de ella, no solo enviar archivos.

```bash
# Supervisa el rendimiento: usa el --stats integrado (no necesitas pv)
alice$ qvole pipe --stats < largefile.dat
bob$   QVOLE_CODE=CODE qvole pipe --stats > received.dat

# Ejemplo de salida de --stats en stderr:
# ↑ 5.20 MB (2.60 MB/s)  ↓ 10.40 MB (5.20 MB/s)
# ↑ 10.40 MB (2.60 MB/s)  ↓ 20.80 MB (5.20 MB/s)
# ...
# Total: ↑ 50.00 MB  ↓ 100.00 MB

# Clonación de disco: clona una unidad remota a través del túnel P2P
alice$ sudo dd if=/dev/sda | qvole pipe
bob$   QVOLE_CODE=CODE qvole pipe | sudo dd of=/dev/sdb

# Copia de seguridad de base de datos: canaliza un volcado a través del túnel
alice$ pg_dump mydb | gzip | qvole pipe
bob$   QVOLE_CODE=CODE qvole pipe | gunzip | psql mydb

# Streaming de webcam/multimedia: transmite desde un dispositivo de captura
alice$ ffmpeg -i /dev/video0 -f mpegts - | qvole pipe
bob$   QVOLE_CODE=CODE qvole pipe | ffplay -

# Seguimiento remoto de registros: sigue los registros de un sistema remoto
alice$ journalctl -f | qvole pipe
bob$   QVOLE_CODE=CODE qvole pipe

# Compresión gzip: comprime los datos en tránsito
alice$ tar czf - dir/ | qvole pipe
bob$   QVOLE_CODE=CODE qvole pipe | tar xzf -

# Captura remota de paquetes: captura el tráfico de red de forma remota
alice$ tcpdump -w - -i eth0 | qvole pipe
bob$   QVOLE_CODE=CODE qvole pipe > capture.pcap

# Sincronización de portapapeles: comparte un fragmento entre máquinas
alice$ qvole pipe | pbcopy
bob$   pbpaste | QVOLE_CODE=CODE qvole pipe

# Chat P2P simple: comunicación de texto bidireccional directa (usa cat para conectar la terminal)
alice$ cat | qvole pipe
bob$   cat | QVOLE_CODE=CODE qvole pipe
```

#### Utilidades de shell
```bash
# Utilidades de shell: comparte previamente un código para acceso permanente entre máquinas de confianza
# Genera un código una vez y añádelo a ~/.bashrc en ambas máquinas:
$ openssl rand -base64 24
$ echo 'export QVOLE_CODE=<generated-code>' >> ~/.bashrc
$ echo "qsend() { tar czf - \"\$@\" | qvole pipe --stats; }" >> ~/.bashrc
$ echo "qrecv() { qvole pipe --stats | tar xzf -; }" >> ~/.bashrc
$ echo "alias qp='qvole pipe --stats'" >> ~/.bashrc

alice$ qsend file.dat                              # envía un archivo (comprimido con gzip)
alice$ qsend dir/                                  # envía un directorio (comprimido con gzip)
bob$   qrecv                                       # recibe y extrae
alice$ qsend file1.dat dir/                        # envía varios
bob$   qrecv                                       # recibe todo

alice$ arecord -f cd | qp                          # transmite el micrófono por P2P (macOS: ffmpeg -f avfoundation -i ":0" -f mp3 - | qp)
bob$   qp | aplay -f cd                            # escucha el micrófono de alice (o: qp | ffplay -i pipe:0 -nodisp)
```

### Exec

Un lado ejecuta un comando local directamente y conecta su stdin/stdout al
flujo QUIC; el otro lado funciona como una canalización simple (stdin→flujo→stdout).

| Indicador | Descripción |
|------|-------------|
| `--cmd <cmd>` | Ejecuta `<cmd>` localmente y canaliza su stdin/stdout a través del túnel |
| `--code CODE`  | Código de conexión (o `$QVOLE_CODE`) |
| `--relay addr` | Dirección del relay (host:port) |
| `--debug` | Registro de depuración verboso en stderr |

A diferencia del subcomando pipe (donde la dirección del flujo depende de si stdin es una terminal; un stdin canalizado abre un flujo saliente, un stdin de terminal no),
el lado `exec` siempre abre el flujo y el lado pipe siempre lo acepta.
Solo el lado con `--cmd` ejecuta un comando; el otro lado simplemente se conecta.

**Ejemplos:**

Administración remota de servidores ad-hoc y acceso a servicios sin distribución de claves SSH
en redes restrictivas o en sitios de clientes.

```bash
# Shell remoto: shell interactivo completo a través del túnel
alice$ qvole exec --cmd "script -q bash /dev/null"
bob$   QVOLE_CODE=CODE qvole exec

# Supervisión remota: usa script -q para aplicaciones TUI
alice$ qvole exec --cmd "script -q htop /dev/null"
bob$   QVOLE_CODE=CODE qvole exec

# Uso de disco remoto: explora el uso de disco de forma remota
alice$ qvole exec --cmd "script -q 'ncdu /' /dev/null"
bob$   QVOLE_CODE=CODE qvole exec

# Inspección remota de registros: consulta los registros de un sistema remoto
alice$ qvole exec --cmd "journalctl -n 100 --no-pager"
bob$   QVOLE_CODE=CODE qvole exec

# Información remota del sistema: comprueba los detalles del sistema remoto
alice$ qvole exec --cmd "uname -a; uptime"
bob$   QVOLE_CODE=CODE qvole exec

# Gestión de Docker: ejecuta comandos de docker en un host remoto
alice$ qvole exec --cmd "docker ps"
bob$   QVOLE_CODE=CODE qvole exec

# Captura remota de paquetes: transmite la captura a un tcpdump local
alice$ qvole exec --cmd "tcpdump -w - -i eth0"
bob$   QVOLE_CODE=CODE qvole exec | tcpdump -r -

# Sesión de terminal compartida: terminal colaborativa con tmux
alice$ qvole exec --cmd "script -q 'tmux new -A -s qvole' /dev/null"
bob$   QVOLE_CODE=CODE qvole exec
```

### <a id="tunnel"></a>Tunnel

Tunelización de puertos TCP sobre túneles QUIC, modelada a partir de SSH `-L` y `-R`.

| Indicador | Descripción |
|------|-------------|
| `-L [laddr:]lport:raddr:rport` | Escucha en el `[laddr:]lport` local, reenvía al `raddr:rport` remoto (el par lo permite con `-aF`) |
| `-R [raddr:]rport:laddr:lport` | El par escucha en el `[raddr:]rport` remoto, reenvía al `laddr:lport` local (el par lo permite con `-aL`) |
| `-aL addr:port` | Permite que el par abra un puerto de escucha en esta dirección (permite el `-R` del par; repetible) |
| `-aF addr:port` | Permite que el par se conecte a addr:port a través de ti (permite el `-L` del par; repetible) |
| `-a` | Permite **todas** las solicitudes de túnel del par (no es seguro): el par puede abrir puertos arbitrarios y alcanzar direcciones arbitrarias a través de tu máquina |
| `--code CODE` | Código de conexión (o `$QVOLE_CODE`) |
| `--relay addr` | Dirección del relay (host:port) |
| `--debug` | Registro de depuración verboso en stderr |

Se pueden combinar varios indicadores `-L` y `-R`. No se acepta nada por defecto:
tus solicitudes `-L` requieren que el par se ejecute con `-aF <destino>`, y tus solicitudes
`-R` requieren que el par se ejecute con `-aL <dirección de escucha>`.

La señal de aceptación del par es por lado, no por solicitud: si el par tiene una
lista blanca que no cubre una de tus solicitudes, tu listener puede seguir
vinculándose, pero cada conexión a través de él se rechaza en el lado del par (el par
registra `Tunnel rejected: … (add -aF …)`). Cuando un par se ejecuta sin lista blanca
alguna, tu lado registra exactamente qué indicador debe ejecutar el par.

Una entrada `-aL`/`-aF` con el host vacío (p. ej. `:8081`) coincide con **cualquier** host en
ese puerto, así que `-aL :8081` permite que el par se vincule a `0.0.0.0:8081` y `-aF :8081`
permite que el par alcance cualquier `host:8081` a través de tu máquina. Usa hosts explícitos
(`127.0.0.1:8081`) salvo que el comodín sea intencionado.

```bash
# Alice escucha en 8080 y reenvía al :80 de Bob (Bob necesita -aF para el destino)
alice$ qvole tunnel -L 8080:localhost:80
bob$   QVOLE_CODE=CODE qvole tunnel -aF localhost:80

# Alice pide a Bob que escuche en 9022 y reenvíe de vuelta al :22 de Alice (Bob necesita -aL para el puerto)
alice$ qvole tunnel -R 9022:localhost:22
bob$   QVOLE_CODE=CODE qvole tunnel -aL 127.0.0.1:9022

# Combinado: Alice recibe el :80 de Bob en :8080 Y Bob recibe el :22 de Alice en :9022
alice$ qvole tunnel -L 8080:localhost:80 -R 9022:localhost:22
bob$   QVOLE_CODE=CODE qvole tunnel -aL 127.0.0.1:9022 -aF localhost:80
```

| Forma del indicador | Significado |
|-----------|---------|
| `-L lport:raddr:rport` | Escucha en `127.0.0.1:lport`, reenvía a `raddr:rport` en el par |
| `-L laddr:lport:raddr:rport` | Escucha en `laddr:lport`, reenvía a `raddr:rport` en el par |
| `-R rport:laddr:lport` | El par escucha en `127.0.0.1:rport`, reenvía a `laddr:lport` en tu lado |
| `-R raddr:rport:laddr:lport` | El par escucha en `raddr:rport`, reenvía a `laddr:lport` en tu lado |

**Ejemplos:**

Reenvío de tráfico TCP sin necesidad de root, módulos del kernel, dispositivos TUN ni estado persistente.
Punto a punto entre exactamente dos pares; secreto forward entre reinicios.

```bash
# Postgres: acceso remoto a la base de datos
alice$ qvole tunnel -L 5432:localhost:5432
bob$   QVOLE_CODE=CODE qvole tunnel -aF localhost:5432

# Escritorio remoto: tuneliza VNC a través del enlace P2P
alice$ qvole tunnel -L 5900:localhost:5900
bob$   QVOLE_CODE=CODE qvole tunnel -aF localhost:5900

# Daemon de Docker remoto: no hace falta un socket TCP expuesto
alice$ qvole tunnel -L 2375:localhost:2375
bob$   QVOLE_CODE=CODE qvole tunnel -aF localhost:2375

# Proxy SOCKS: enruta el tráfico a través de la red del par
alice$ ssh -D 1080 localhost -N                          #inicia un proxy SOCKS local
alice$ qvole tunnel -R 1080:localhost:1080               #exponlo a Bob
bob$   QVOLE_CODE=CODE qvole tunnel -aL 127.0.0.1:1080   #Bob ahora tiene SOCKS en :1080

# Pila de supervisión: accede a Prometheus/Grafana detrás de un NAT
alice$ qvole tunnel -L 9090:localhost:9090 -L 3000:localhost:3000
bob$   QVOLE_CODE=CODE qvole tunnel -aF localhost:9090 -aF localhost:3000

# Servidor multimedia: mira Plex/Jellyfin en una TV remota
alice$ qvole tunnel -L 32400:localhost:32400
bob$   QVOLE_CODE=CODE qvole tunnel -aF localhost:32400

# Escotilla de escape SSH: expón sshd en una máquina muy restringida
alice$ qvole tunnel -R 2222:localhost:22
bob$   QVOLE_CODE=CODE qvole tunnel -aL 127.0.0.1:2222
bob$   ssh -p 2222 localhost                              #Bob llega al sshd de Alice

# Servidores de juego: aloja un servidor de Minecraft detrás de un NAT
alice$ qvole tunnel -L 25565:localhost:25565
bob$   QVOLE_CODE=CODE qvole tunnel -aF localhost:25565

# Redis: accede a una instancia de Redis remota
alice$ qvole tunnel -L 6379:localhost:6379
bob$   QVOLE_CODE=CODE qvole tunnel -aF localhost:6379

# Vista previa de desarrollo web: expón un servidor de desarrollo local a un par
alice$ npm run dev                                       #ejecutándose en :3000
alice$ qvole tunnel -R 3000:localhost:3000               #Alice expone su :3000 a Bob
bob$   QVOLE_CODE=CODE qvole tunnel -aL 127.0.0.1:3000   #Bob ya puede visitar localhost:3000
```


#### Túneles persistentes
```bash
# Reconexión automática: reconéctate al desconectarte con un código fijo
alice$ while true; do QVOLE_CODE=CODE qvole tunnel -L 8080:localhost:80; sleep 1; done

# Para despliegues de larga duración, genera un código aleatorio fuerte:
$ openssl rand -base64 24

# Alternativa: persistencia con systemd para mantener el túnel vivo para siempre
# /etc/systemd/system/qvole-tunnel.service
[Unit]
Description=Túnel P2P de qvole
After=network-online.target

[Service]
Type=simple
# Primero genera un código fuerte: openssl rand -base64 24
Environment=QVOLE_CODE=<generated-code>
ExecStart=/usr/local/bin/qvole tunnel -L 8080:localhost:80
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

### Variables de entorno

#### Variables de cliente (pipe / exec / tunnel)

| Variable | Predeterminado | Descripción |
|----------|---------|-------------|
| `QVOLE_CODE` | - | Código de conexión (alternativa a `--code`) |
| `QVOLE_KDF_ITERATIONS` | 600000 | Iteraciones PBKDF2 para la derivación código-a-escalar |
| `QVOLE_KEEPALIVE_MS` | 2000 | Intervalo de keepalive de QUIC |
| `QVOLE_IDLE_TIMEOUT_MS` | 120000 | Tiempo de inactividad de QUIC antes de desconectar |
| `QVOLE_HANDSHAKE_TIMEOUT_MS` | 30000 | Tiempo de espera del handshake |
| `QVOLE_MAX_STREAMS` | 100 | Máximo de flujos QUIC concurrentes |
| `QVOLE_FORWARD_MAX_STREAMS` | 200 | Máximo de flujos de túnel reenviados |
| `QVOLE_PUNCH_TIMEOUT_MS` | 10000 | Tiempo de espera de la perforación de agujeros UDP |
| `QVOLE_EXCHANGE_DEADLINE_MS` | 90000 | Fecha límite del intercambio SPAKE2 |
| `QVOLE_SPAKE2_RESEND_MS` | 2000 | Intervalo de reenvío de mensajes SPAKE2 |
| `QVOLE_CONFIRM_RESEND_MS` | 2000 | Intervalo de reenvío del mensaje de confirmación |
| `QVOLE_EXCHANGE_READ_DEADLINE_MS` | 1000 | Intervalo de sondeo de lectura del relay |
| `QVOLE_REG_INTERVAL_MS` | 30000 | Intervalo de registro en el relay |
| `QVOLE_DIAL_TIMEOUT_MS` | 10000 | Tiempo de espera de dial TCP del túnel |
| `QVOLE_EXEC_DRAIN_TIMEOUT_MS` | 5000 | Tiempo de espera de drenaje de exec tras la salida del comando |
| `QVOLE_INITIAL_STREAM_WINDOW` | 1048576 | Ventana de recepción de flujo QUIC |
| `QVOLE_INITIAL_CONNECTION_WINDOW` | 4194304 | Ventana de recepción de conexión QUIC |

#### Variables del relay (subcomando `relay`)

| Variable | Predeterminado | Descripción |
|----------|---------|-------------|
| `QVOLE_RELAY_MAX_ROOMS` | 10000 | Máximo de salas concurrentes |
| `QVOLE_RELAY_MAX_ROOMS_PER_IP` | 10 | Máximo de salas por dirección IP |
| `QVOLE_RELAY_MAX_CLIENTS_HARD` | 20 | Límite máximo de clientes por sala |
| `QVOLE_RELAY_MSG_RATE` | 10 | Máximo de mensajes por ventana de velocidad por cliente |
| `QVOLE_RELAY_RATE_WINDOW_MS` | 1000 | Duración de la ventana de velocidad para la limitación de mensajes |
| `QVOLE_RELAY_TTL_MS` | 60000 | TTL de registro (1 min) antes de que expire el cliente |
| `QVOLE_RELAY_CLEANUP_INTERVAL_MS` | 60000 | Intervalo de limpieza de registros caducados |
| `QVOLE_RELAY_WORKERS` | 4 | Número de gorutinas worker |
| `QVOLE_RELAY_PKT_CHAN_BUF` | 256 | Tamaño del buffer del canal de paquetes |
| `QVOLE_RELAY_WRITE_DEADLINE_MS` | 500 | Fecha límite de escritura UDP |
| `QVOLE_RELAY_WRITE_POOL` | 256 | Tamaño del pool de escritura para escrituras salientes del relay |
| `QVOLE_RELAY_STATS_INTERVAL_MS` | 300000 | Intervalo de registro de estadísticas |

### Privacidad y términos

[Política de privacidad y términos de servicio del relay público &rarr; PRIVACY.md](../../docs/PRIVACY.md)

---
