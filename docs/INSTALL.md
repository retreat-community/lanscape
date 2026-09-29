# Installing Lanscape

- [Lanscape Mini](#lanscape-mini): `lsm-agent` and `lsm-server`, tiny C binaries.
- [Lanscape (Full)](#lanscape-full): `lanscape` server and `lanscape-agent`.

Release assets are signed: verify `checksums.txt` with cosign before installing
(the command is in every release description).

## Lanscape Mini

Mini needs one server and an agent on every node you want to measure. Agents dial the server on
TCP 47701 and accept test traffic on TCP 47700; the page is on http://server:8080.
All nodes share one token. **Mini has no TLS**: use it in a trusted LAN or behind a tunnel /
reverse proxy.

### Linux (any distribution)

One command per host (systemd or OpenRC is detected):

```sh
# server (also start an agent on the same host if you want it measured)
curl -fsSL https://raw.githubusercontent.com/retreat-community/lanscape/main/scripts/install-mini.sh | \
  sudo sh -s -- server --token 'long-random-token'
# agent
curl -fsSL https://raw.githubusercontent.com/retreat-community/lanscape/main/scripts/install-mini.sh | \
  sudo sh -s -- agent --server 192.168.1.10 --token 'long-random-token'
```

Manual installation: download `lanscape-mini_<version>_linux-<arch>.tar.gz` from the release,
copy `lsm-agent`/`lsm-server` to `/usr/bin`, the `*.conf` samples to `/etc/lsm/`, and the unit
from `systemd/` (or the script from `openrc/`). The agent needs `CAP_NET_RAW` and `CAP_NET_ADMIN`
(or root) for `SO_BINDTODEVICE` and ICMP.

Configuration keys (`/etc/lsm/agent.conf`, `/etc/lsm/server.conf`) can be overridden with
environment variables `LSM_<KEY>` (for example `LSM_TOKEN`, `LSM_SERVER`).

| Agent key | Default | Meaning |
|---|---|---|
| `server` | — | server address, `host[:47701]` |
| `token` | — | shared token |
| `id` | hostname | name on the map |
| `exclude` | `wan*,tailscale*,docker*,veth*,cni*,flannel*,lxc*,virbr*` | interfaces never tested |
| `max_duration_ms` / `max_streams` | 30000 / 8 | limits for incoming tests |

| Server key | Default | Meaning |
|---|---|---|
| `token` | — | shared token (≥ 8 characters) |
| `listen` / `ctl_listen` | `:8080` / `:47701` | HTTP and agent ports |
| `data_file` | `/var/lib/lsm/runs.json` | run history (last `keep_runs`, default 20) |
| `duration_ms` / `streams` / `ping_count` | 5000 / 4 / 10 | test parameters |
| `expect` | — | expected speed for segments without link speed: `10.30.0.0/24:2500,...` |
| `webhook` | — | `http://` URL receiving the finished run as JSON |
| `password` | — | basic-auth password (user `admin`) for POST requests |

### OpenWrt

Packages are built with the official SDK for OpenWrt 24.10 (`.ipk`) and 25.12 (`.apk`). Download
the file for your package architecture from the release, or use the feed below.

```sh
# OpenWrt 25.12 (apk), e.g. Xiaomi AX3000T = aarch64_cortex-a53
apk add --allow-untrusted ./openwrt-25.12-aarch64_cortex-a53_lsm-agent-*.apk
# OpenWrt 24.10 (opkg)
opkg install ./openwrt-24.10-mipsel_24kc_lsm-agent_*.ipk

uci set lsm.agent.server='192.168.1.10'
uci set lsm.agent.token='long-random-token'
uci set lsm.agent.enabled='1'
uci commit lsm && /etc/init.d/lsm-agent restart
```

`lsm-server` on the router: `uci set lsm.server.token=...; uci set lsm.server.enabled=1; uci commit lsm;
/etc/init.d/lsm-server restart`, then open http://router:8080.

#### Package feed

The feed is published on GitHub Pages:
`https://retreat-community.github.io/lanscape/openwrt/<branch>/<arch>/` (`<branch>` is `24.10` or
`25.12`, `<arch>` is the package architecture from `/etc/apk/arch` or `opkg print-architecture`).

```sh
# OpenWrt 25.12
echo "https://retreat-community.github.io/lanscape/openwrt/25.12/$(cat /etc/apk/arch)/packages.adb" \
  >> /etc/apk/repositories.d/customfeeds.list
apk update --allow-untrusted && apk add --allow-untrusted lsm-agent
# OpenWrt 24.10
echo "src/gz lanscape https://retreat-community.github.io/lanscape/openwrt/24.10/$(opkg print-architecture | awk 'END{print $2}')" \
  >> /etc/opkg/customfeeds.conf
opkg update && opkg install lsm-agent
```

The feed index is not signed with an OpenWrt key (packages are verified through the signed
`checksums.txt` of the release); on 24.10 `opkg` warns about the missing signature and on 25.12
`--allow-untrusted` is required.

### Docker

```sh
docker run -d --name lsm-server --network host -e LSM_TOKEN=secret -v lsm:/data \
  ghcr.io/retreat-community/lsm-server:latest
docker run -d --name lsm-agent --network host --cap-add NET_RAW --cap-add NET_ADMIN \
  -e LSM_SERVER=192.168.1.10 -e LSM_TOKEN=secret ghcr.io/retreat-community/lsm-agent:latest
```

or `deploy/compose/mini.yaml` (`LSM_TOKEN=secret docker compose -f mini.yaml up -d`). Images are
`FROM scratch` with a single binary and are available for amd64, arm64, arm/v7, arm/v6, 386,
riscv64 and ppc64le.

### Kubernetes

```sh
kubectl apply -f https://raw.githubusercontent.com/retreat-community/lanscape/main/deploy/k8s/mini/lanscape-mini.yaml
kubectl -n lanscape-mini create secret generic lsm-token --from-literal=token=$(openssl rand -hex 16) \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl -n lanscape-mini port-forward svc/lsm-server 8080
```

The agent runs as a DaemonSet with `hostNetwork: true` and measures node networks.

## Lanscape (Full)

The server (`lanscape`) is one static binary with the web UI. Agents (`lanscape-agent`) dial the
server gateway on TCP 8443, register once with a token and then use mTLS; test traffic between
agents uses TCP/UDP 47700. Lanscape Mini agents can join the same server on TCP 47701.

| Port | Purpose |
|---|---|
| 8080 | UI and API (put a TLS reverse proxy in front or use `--tls-cert/--tls-key`) |
| 8443 | agent gateway (TLS by the built-in CA, mTLS after registration) |
| 47700/tcp+udp | data plane between agents |
| 47701 | Lanscape Mini agents (optional, `--mini-listen ""` disables) |

### Server

**Docker / Compose**

```sh
docker run -d --name lanscape -p 8080:8080 -p 8443:8443 -p 47701:47701 -v lanscape:/data \
  -e LANSCAPE_ADMIN_PASSWORD='change-me-please' -e LANSCAPE_GATEWAY_HOSTS=lanscape.lan,192.168.1.10 \
  ghcr.io/retreat-community/lanscape:latest
```

or `deploy/compose/server.yaml`. Open http://server:8080, sign in as `admin` (or create the first
administrator in the browser when no bootstrap password is set).

**Debian/Ubuntu/RHEL/Alpine packages**: download `lanscape_<version>_linux_<arch>.deb|.rpm|.apk`
from the release, install it, set options in `/etc/lanscape/lanscape.env`, then
`systemctl enable --now lanscape` (OpenRC: `rc-update add lanscape && rc-service lanscape start`).

**Binary**: `lanscape serve --data-dir /var/lib/lanscape --gateway-hosts lanscape.lan`. Every flag
has an environment variable `LANSCAPE_<FLAG>`. Useful flags: `--expect 10.30.0.0/24=2500`,
`--metrics-token`, `--mini-token`, `--secure-cookies` (behind a TLS proxy).

**Prometheus and Grafana**: `/metrics` exports paths (throughput, RTT, loss, verdict), agents,
devices, runs, problems, monitors (status, response time) and failed notifications; protect it
with `--metrics-token` (bearer token). Ready dashboards are in
[`deploy/grafana`](../deploy/grafana): import `lanscape-network.json` and
`lanscape-services.json` in Grafana (**Dashboards → New → Import**) and pick the Prometheus
data source.

**Database**: SQLite in the data directory by default (WAL mode, nothing to set up). For larger
installations or an existing database cluster use PostgreSQL 13 or newer:
`--db 'postgres://lanscape:secret@db:5432/lanscape?sslmode=require'` (Helm:
`server.existingDatabaseSecret` with the URL under `url`). The schema is created and migrated on
start. The data directory is still needed for the CA and agent certificates.

**Backup and restore**: `lanscape backup /backup/lanscape.db` (consistent copy while running);
restore with the server stopped: `lanscape restore /backup/lanscape.db`. With PostgreSQL use
`pg_dump`/`pg_restore`, and back up the data directory (`pki/`) in both cases.

**Single sign-on (OpenID Connect)**: register Lanscape as a client at your provider (Authentik,
Keycloak, Authelia, Zitadel, Google, Entra ID) with the redirect URL
`https://panel.example/api/v1/auth/oidc/callback` (the **Public URL** setting, or the address the
browser uses), then start the server with:

```sh
lanscape serve --oidc-issuer https://auth.example/application/o/lanscape/ \
  --oidc-client-id lanscape --oidc-client-secret ... \
  --oidc-admin-groups lanscape-admins --oidc-operator-groups lanscape-operators
```

The login page shows **Sign in with SSO** (`--oidc-name`). The flow uses the authorization code
with PKCE; the ID token signature (RS256/ES256), issuer, audience, expiry and nonce are checked.
Roles come from the `groups` claim (`--oidc-role-claim`) on every sign-in; people outside the
listed groups get `--oidc-default-role` (`viewer`, or `none` to refuse them). An account from the
provider never takes over a local account with the same name, and local accounts keep working
(two-factor authentication with TOTP is available for them under **Account**).

### Agents

In the panel open **Settings → Agents → Add node**. It creates a registration token (single use
or reusable, optionally expiring) and shows ready commands for every platform. The commands pin
the server CA fingerprint, so the first connection cannot be intercepted.

| Platform | Command |
|---|---|
| Linux (any) | `curl -fsSL https://raw.githubusercontent.com/retreat-community/lanscape/main/scripts/install.sh \| sudo sh -s -- agent --server lanscape.lan:8443 --token lsr_... --ca-fingerprint ...` |
| Debian/Ubuntu/Proxmox, RHEL, Alpine | install `lanscape-agent_<version>_linux_<arch>.deb/.rpm/.apk`, set `LANSCAPE_SERVER`, `LANSCAPE_TOKEN`, `LANSCAPE_CA_FINGERPRINT` in `/etc/lanscape/agent.env`, `systemctl enable --now lanscape-agent` |
| Docker / NAS | `deploy/compose/agent-nas.yaml` (`network_mode: host`, `NET_RAW`, `NET_ADMIN`, optional read-only Docker socket) |
| Kubernetes | `helm install lanscape oci://ghcr.io/retreat-community/charts/lanscape` (see the chart values) |
| macOS | download the darwin archive, `sudo lanscape-agent service install --server ... --token ... --ca-fingerprint ...` (launchd) |
| Windows | unzip, run `lanscape-agent.exe service install --server ... --token ... --ca-fingerprint ...` as Administrator |
| FreeBSD / OPNsense / pfSense | copy the binary to `/usr/local/bin`, `freebsd/lanscape_agent` to `/usr/local/etc/rc.d`, then `sysrc lanscape_agent_enable=YES lanscape_agent_flags="--server ... --token ..." && service lanscape_agent start` |
| OpenWrt | `lanscape-agent` and `luci-app-lanscape` packages from the feed (below); on routers with little flash use the Mini agent |

The agent needs root or `CAP_NET_RAW` + `CAP_NET_ADMIN` for ICMP and `SO_BINDTODEVICE`. Limits
and modes: `--max-duration`, `--max-streams`, `--max-udp-mbps`, `--exclude 'wan*,tailscale*'`,
`--mode respond-only` (never initiate tests) or `--mode checks-only` (no test responder).
Certificates are stored in the data directory and renewed automatically 30 days before expiry.
Deleting an agent in the panel revokes its access.

### OpenWrt (Full agent and LuCI)

`lanscape-agent` (the Full agent built for routers: 7–8 MB) and `luci-app-lanscape` come from the
same releases and feed as the Mini packages. On routers with 16 MB of flash use `lsm-agent`.

```sh
# OpenWrt 25.12
apk add --allow-untrusted lanscape-agent luci-app-lanscape
# OpenWrt 24.10
opkg install lanscape-agent luci-app-lanscape
```

Open **Services → Lanscape** in LuCI: server address, registration token, CA fingerprint, excluded
interfaces (`wan` is excluded by default), test limits and the discovery sources (DHCP leases,
Wi-Fi clients, port forwards, mDNS, SSDP). The page shows the connection state, a
**Check from this router** button and the paths of the last run; a test limited by the router CPU
is marked 🟣. Without LuCI:

```sh
uci set lanscape.agent.server='lanscape.lan:8443'
uci set lanscape.agent.token='lsr_...'
uci set lanscape.agent.ca_fingerprint='...'
uci set lanscape.agent.enabled='1'
uci commit lanscape && /etc/init.d/lanscape-agent restart
lanscape-agent status      # connection state
lanscape-agent check       # test the paths of this router now
lanscape-agent last        # its paths in the last run
```

### Service discovery

Agents report what runs on their hosts every 5 minutes (`--discover-interval`); the panel merges
the findings into cards in **Services → Found**. Sources are chosen with `--discover`:

| Source | What it needs |
|---|---|
| `sockets` | Linux; root (or `CAP_SYS_PTRACE`) to see the processes behind other users' sockets |
| `docker` | read access to the Docker (or Podman) socket, `--docker-socket /var/run/docker.sock`; mount it read-only in containers |
| `k8s` | in-cluster service account with read-only access to Services, Ingresses, HTTPRoutes, Deployments, StatefulSets, DaemonSets and PVCs, or `--kubeconfig` |
| `openwrt` | the agent running on an OpenWrt router: DHCP leases (dynamic and static) give devices their names, hostapd reports Wi-Fi clients with band and signal, port forwards and SQM settings are listed |
| reverse proxies | `--traefik-url http://traefik:8080` (API), `--caddy-admin http://127.0.0.1:2019`, `--nginx-dir auto`: public names are attached to the containers and processes they forward to, which gives services their external URLs |
| local DNS | `--pihole-url` + `--pihole-password` (v6 app password or v5 token), `--adguard-url` + `--adguard-user`/`--adguard-password`, `--technitium-url` + `--technitium-token`, or a zone transfer from any server that allows it (`--axfr 192.168.1.11/home.arpa`): local names for discovered devices |
| hardware | `--nut` (Network UPS Tools, `auto` = 127.0.0.1:3493), `--smart` (smartctl, as root) and `--zfs` (zpool): UPS charge and runtime, disk health and pool state on the dashboard; changes such as "on battery" go to the change feed |
| `mdns`, `ssdp` | multicast on the local links (not inside Kubernetes pods): printers, media players, NAS, IoT and UPnP routers; with the ARP neighbour tables of all agents and the MAC vendor they appear under **Devices → Discovered**, in IPAM and on the map |
| `netbios` | names of Windows PCs and SMB devices: the addresses in the agent's neighbour table are asked for their NetBIOS node status (UDP 137) and their LLMNR reverse name (UDP 5355), 50 queries/s; enabled with `mdns` by `auto` |
| Proxmox VE | `--proxmox-url https://127.0.0.1:8006 --proxmox-token 'lanscape@pve!discovery=<secret>'` (a token with the PVEAuditor role; `--proxmox-insecure` for the self-signed certificate). VMs and containers with status, addresses (QEMU guest agent), tags and notes; virtio guests inherit the link speed of the host bridge |
| `libvirt` | an agent on a KVM/libvirt host with `virsh` (enabled by `auto` when the libvirt socket exists): guests with state, bridges, MAC and addresses (guest agent, DHCP leases or ARP); they are nested under the host on the map and inherit its bridge speed |

The default `--discover auto` enables `sockets` on Linux, `docker` when the socket exists,
`k8s` inside a cluster, `openwrt` on OpenWrt and `mdns`/`ssdp` everywhere else. HTTP endpoints are fingerprinted (title, headers, favicon, characteristic
paths) against the built-in library of application signatures; `--no-probe` turns this off and
`--signatures my-apps.yaml` adds your own signatures in the format of
[`internal/fingerprint/signatures.yaml`](../internal/fingerprint/signatures.yaml).

**Active scanning** (Devices → Discovered → Scan) is off by default: an agent only port-scans
subnets listed in `--scan-allow 192.168.1.0/24,10.0.0.0/24`, at the rate limit given in the
request (200 connections/s by default).

Found services wait in the queue until you add, ignore or hide them. Rules in
**Settings → Discovery** triage new cards automatically, for example "every Ingress with TLS →
add with an HTTPS monitor and a dashboard tile".

### Monitors and notifications

Monitors check HTTP(S), TCP, UDP, ICMP, DNS, TLS certificates and domain expiry (RDAP) from the
server or from any Full agent (**Check from**). DNS checks query the system resolver or a given
server over UDP/TCP (`192.168.1.11`, `tcp://…`), DNS over TLS (`tls://1.1.1.1`) or DNS over HTTPS
(`https://dns.example/dns-query`). Without permission for ICMP sockets, ping checks fall back to
TCP connects (a refused connection also proves the host is up). Docker containers, Kubernetes workloads and
Proxmox guests are watched through discovery; heartbeat monitors wait for a job to call their
push URL (`curl -fsS https://panel/api/push/<token>?status=up&msg=OK`); composite monitors
combine others (`#1 && (#2 || #3)`). A monitor can depend on others (a router, a hypervisor):
while a parent is down, or while the agent a service runs on is offline, its incidents are
suppressed and grouped under the cause. With several observation points an incident opens only when at
least *N* points fail, which separates "the service is down" from "the path to it is down". An
incident opens after the configured number of failed checks in a row and closes automatically.
Maintenance windows (planned, or "right now for 30 minutes") mute notifications.

Notification channels (**Settings → Notifications**): Telegram (bot token and chat id), webhook
(JSON body, optional HMAC-SHA256 signature in `X-Lanscape-Signature`), email (SMTP with STARTTLS
or TLS), ntfy, Gotify, Discord and Slack (incoming webhooks) and Matrix (a bot access token and a
room id). Each channel can have quiet hours, a repeat interval for unacknowledged
incidents and a list of monitors it cares about. Set **Settings → General → Public URL** so
notifications link back to the panel. **Account → Notifications on this device** turns on browser
push notifications (Web Push; the panel must be served over HTTPS). The webhooks in **Settings →
General** receive every `run.finished`, `incident.opened`/`resolved`/`reminder` and `device.new`
event as JSON (`{"event": …, "run"|"incident"|"change": …}`).

### Internet test

**Settings → General → Internet test** checks the public address and, optionally, the download
speed through every default gateway of the chosen points (the server and/or agents such as the
router; with two uplinks each one is tested separately). It is off until you set a period or press
**Check now**; only then are the address service (`https://1.1.1.1/cdn-cgi/trace` by default) and
the download URL contacted, and both can point to your own servers. The dashboard shows the
current address, the last speed and the outages of the last 30 days; an exit that goes down or
comes back is written to the change feed.

### Actions

Operators can wake devices and restart discovered objects from the panel (**Devices →
Discovered → Wake**, **Services → Restart**, or `Ctrl+K`). Every action asks for confirmation
and is written to the audit log (who, what, when, result). An agent only performs the actions
listed in `--actions` (`LANSCAPE_ACTIONS`):

| Action | Default | What the agent does |
|---|---|---|
| `wol` | on | Sends a Wake-on-LAN magic packet to the broadcast address of its interfaces on the device's subnet. The server picks agents on that subnet or that have the device in their neighbour table. |
| `restart` | off | Restarts a Docker container, performs a rolling restart of a Kubernetes deployment, statefulset or daemonset (needs `patch` on them; the Helm chart adds it when `agent.actions` contains `restart`), or reboots a Proxmox VM/CT (the API token needs `VM.PowerMgmt`). |

`--actions none` disables actions on an agent.

### Configuration as code (GitOps)

Services, monitors, segments, discovery rules, notification channels and status pages can be
kept in a YAML file under version control. Objects refer to each other by name; secrets are
never exported and a channel keeps its stored secret when the file leaves it out. `${NAME}` is
replaced from the environment of whoever reads the file (the CLI or `--config`), so tokens stay
out of the repository.

```yaml
apiVersion: lanscape/v1
services:
  - {name: Gitea, app: gitea, group: Dev, internal_url: "http://10.0.0.5:3000", tile: true}
monitors:
  - name: Router
    check: {type: icmp, target: 10.0.0.1}
  - name: Gitea HTTP
    service: Gitea
    check: {type: http, target: "http://10.0.0.5:3000/api/healthz"}
    interval: 30
    from: [server, nas]          # observation points: agent names or "server"
    depends_on: [Router]         # incidents are suppressed while the router is down
  - name: Internet
    check: {type: composite, expr: "{Router} && {Gitea HTTP}"}
segments:
  - {id: 10.0.0.0/24, name: LAN, expected_mbps: 1000}
rules:
  - {name: ingresses, kind: ingress, action: add, monitor: true}
channels:
  - name: ops
    type: telegram
    config: {bot_token: "${TELEGRAM_TOKEN}", chat_id: "-100123", monitors: [Gitea HTTP]}
status_pages:
  - slug: home
    title: Home
    public: true
    groups: [{name: Core, monitors: [Router, Gitea HTTP]}]
```

- `lanscape config export --url https://panel --api-token lst_... > lanscape.yaml` writes the
  current state (also **Settings → Import → Download current configuration**).
- `lanscape config apply -f lanscape.yaml --dry-run` prints the plan; without `--dry-run` it
  applies it. Applying the same file twice changes nothing, so it fits a CI pipeline.
- `--prune` also deletes objects of the sections present in the file that the file does not
  list; sections left out of the file are never touched.
- `lanscape serve --config /etc/lanscape/lanscape.yaml` applies the file on every start.

The API is `GET /api/v1/config` and `POST /api/v1/config?dry_run=true&prune=true` with an
administrator token.
