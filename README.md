# Lanscape

Network paths, map and service uptime in one panel.

Lanscape ships in two editions:

- **Lanscape Mini** — a tiny network checker (`lsm-agent`, `lsm-server`) written in C that fits
  on a router with 16 MB of flash. One page: *Check all* → per-segment speed matrix → automatic map.
- **Lanscape** (Full) — a single panel for your whole infrastructure: network tests, map,
  service discovery, uptime monitoring and a dashboard.

Both editions share the data-plane test protocol (LSTP/1) and the segment model.

## Quick start: Lanscape Mini

```sh
# on the server host
curl -fsSL https://raw.githubusercontent.com/retreat-community/lanscape/main/scripts/install-mini.sh | sudo sh -s -- server --token SECRET
# on every node (routers: see docs/INSTALL.md for the OpenWrt packages)
curl -fsSL https://raw.githubusercontent.com/retreat-community/lanscape/main/scripts/install-mini.sh | sudo sh -s -- agent --server SERVER_IP --token SECRET
```

Open `http://SERVER_IP:8080` and press **Check all**. Every path (node pair × shared segment) is
measured separately with traffic bound to the interface: ping/RTT, MTU 1500 (and jumbo), TCP
throughput with 1 and 4 streams. The page shows a speed matrix per segment, an automatic map and
the list of problems: TCP intercepted while ICMP works, MTU, speed below expected, path mismatch,
macvlan parent/child.

| | `lsm-agent` | `lsm-server` |
|---|---|---|
| Size (static, mipsle) | < 128 KiB | < 512 KiB incl. the page |
| Platforms | Linux amd64, 386, arm64, armv7/6/5, mips/mipsle, mips64/le, riscv64, ppc64le; OpenWrt 24.10/25.12 | same |

## Documentation

- [Installation](docs/INSTALL.md) — Linux, OpenWrt, Docker, Kubernetes
- [Protocols](docs/PROTOCOL.md) — LSTP/1 data plane and control planes
- [Architecture decisions](docs/decisions/)
- [Specification](docs/SPEC.md)

## License

GNU General Public License v3.0 or later, see [LICENSE](LICENSE) and [NOTICE](NOTICE).
