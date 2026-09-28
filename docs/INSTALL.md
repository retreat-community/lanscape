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

Installation of the Full edition is described once it is released (see the release notes).
