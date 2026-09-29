# SPEC compliance

Every requirement of [SPEC.md](SPEC.md) mapped to where it is implemented and how it is
verified. Section numbers follow the SPEC. §19 ("ideas for the future") is out of scope and
tracked in [ROADMAP.md](ROADMAP.md).

Verification column:

- `TestName` — a Go unit or integration test (`go test ./...`), file in parentheses;
- **e2e Mini** / **e2e Full** — `test/e2e/mini_test.go` `TestMini` / `test/e2e/full_test.go`
  `TestFull`: real agents and servers in the network-namespace topology of §20
  (`test/e2e/topo.sh`), run by the `e2e` CI job;
- **UI** — Playwright specs `test/ui/mini.spec.ts`, `test/ui/full.spec.ts` against those runs;
- **CI** — a job of `.github/workflows/ci.yml` (sizes, packages, images, OpenAPI lint, Helm).

Deviation agreed with the owner: §16 asks for MIT or Apache-2.0; the project is GPL-3.0-or-later
([decision 0001](decisions/0001-license-gpl-3.md)).

## §0. Lanscape Mini

| Requirement | Implementation | Verification |
|---|---|---|
| 0.2 `lsm-agent`: interfaces (name, IP/prefix, MAC, VLAN, link speed, state); ping/RTT/loss, PMTU with DF, TCP throughput bound to the interface | `mini/agent/` (`main.c`, `dp.c`, `icmp.c`), `mini/common/netif.c` | e2e Mini; `mini/tests/tests.c` |
| 0.2 `lsm-server`: agent list, segments by subnet+VLAN, serial run of all paths × shared segments, web page and JSON | `mini/server/` (`agents.c`, `segments.c`, `run.c`, `http.c`) | e2e Mini (4 segments, matrix, map); UI |
| 0.3.1 registration by shared token, outgoing TCP with reconnection | `mini/server/agents.c`, `mini/agent/main.c` | e2e Mini |
| 0.3.2 interface inventory, exclusion masks (`wan*`, `tailscale*`) | `mini/common/netif.c`, `-x` flag, `packaging/mini/agent.conf` | e2e Mini (`-x 'mgmt*'`) |
| 0.3.3 reachability, RTT (10 packets), PMTU 1500 and jumbo, TCP both ways with 1 and 4 streams, 5 s | `mini/server/run.c`, `mini/agent/dp.c` | e2e Mini (TCP1/TCPN per path) |
| 0.3.4 `SO_BINDTODEVICE` and interface counters before/after; "path does not match" | `mini/agent/dp.c` | e2e Mini (`path_ok` on every measured path) |
| 0.3.5 expected speed from link speed, manual value for virtio, verdicts 🟢🟡🔴🟣 (CPU of the agent) | `mini/server/run.c`, `expect=` in `packaging/mini/server.conf` | e2e Mini (segment C expected 100, green) |
| 0.3.6 page: "Check all" with progress, matrix per segment tab, map (segments as buses, nodes as cards with ports), problem list (TCP ✗ with ICMP ✓, MTU, below expected, path mismatch) | `mini/web/index.html` (hand-drawn SVG, SSE progress) | UI `mini.spec.ts`; e2e Mini (`tcp_intercepted`, `mtu`, `macvlan`) |
| 0.3.7 last run plus N previous (default 20) in one JSON file, no database | `mini/server/store.c` (`keep_runs`) | `mini/tests/tests.c`; e2e Mini |
| 0.3.8 only `GET /api/last` and an optional webhook | `mini/server/http.c`, `webhook=` | e2e Mini (webhook payload checked) |
| 0.4 budgets: agent ≤ 128 KB on every target, server ≤ 512 KB, page ≤ 40 KB gzip | `mini/tools/size-check.sh` | CI `mini` (fails over budget), `mini-sizes` (table in the release) |
| 0.4 agent RAM idle ≤ 1 MB, server for 20 agents ≤ 8 MB | static C, no allocations in the hot loop | e2e Mini: agents 220 kB, server 516 kB resident |
| 0.4 OpenWrt package ≤ 64 KB | `packaging/openwrt/lsm-agent`, `lsm-server` | CI `openwrt` ("Check Mini package size") |
| 0.5 C with musl, `-Os`, gc-sections, strip; no third-party libraries; getifaddrs/ioctl/`/proc/net/vlan/config`; one `poll()` loop | `mini/Makefile`, `mini/common/`, [decision 0003](decisions/0003-mini-toolchain.md) | CI `mini` (13 targets) |
| 0.5 TLV control protocol, HMAC-SHA256 challenge, own SHA-256/HMAC, no TLS (documented) | `mini/common/tlv.c`, `sha256.c`, `lstp.h`; [PROTOCOL.md](PROTOCOL.md) §3 | `mini/tests/tests.c` (`test_sha256`, `test_hmac`, `test_tlv`), `fuzz_tlv.c` |
| 0.5 hand-written JSON serializer, minimal HTTP (static, API, `POST /api/run`, SSE) | `mini/common/jw.c`, `mini/server/http.c`, `httpparse.c` | `mini/tests/tests.c` (`test_fmt_jw`, `test_http`), `fuzz_http.c` |
| 0.5 page gzip-embedded, served with `Content-Encoding: gzip`; SVG map without layout libraries | `mini/tools/bin2c.c`, `mini/web/index.html` | CI size check of `index.html.gz`; UI |
| 0.5 send from a prefilled buffer, discard on receive, no allocations in the hot loop | `mini/agent/dp.c` | code review; e2e throughput |
| 0.5 Makefile + zig cc cross-compilation for linux amd64/386/arm64/armv7/armv6/armv5/mips/mipsle/mips64/mips64le/riscv64/ppc64le and freebsd/amd64; OpenWrt SDK packages | `mini/Makefile`, `packaging/openwrt/sdk-build.sh` | CI `mini` matrix (FreeBSD allowed to fail), `openwrt` |
| 0.6 OpenWrt package with procd and `/etc/config/lsm`; Linux binary with systemd/OpenRC; Docker `FROM scratch`, host network; Kubernetes DaemonSet without Helm | `packaging/openwrt/lsm-*`, `packaging/systemd/lsm-*.service`, `packaging/openrc/lsm-*`, `packaging/docker/lsm-*.Dockerfile`, `deploy/compose/mini.yaml`, `deploy/k8s/mini/lanscape-mini.yaml`, `scripts/install-mini.sh` | CI `openwrt`, `docker`, `k8s` |
| 0.7 shared, versioned data-plane protocol; Full server accepts Mini agents as lite nodes | `internal/proto/lstp.go`, `internal/server/minigw.go` | `TestMiniCompat`, `TestRoundTrip`, `TestMalformed` (`internal/proto/lstp_test.go`); e2e Full (Mini "pod" agent) |
| 0.8 acceptance: 4 segments, matrix and map from one run; proxy interception, macvlan parent and MTU recognised; budgets in CI; 🟣 on CPU limit | as above | e2e Mini, UI, CI `mini`; 🟣 in `mini/server/run.c` |

## §1–3. Goal, terms, architecture

| Requirement | Implementation | Verification |
|---|---|---|
| Network, map, services and uptime, dashboard in one panel | `internal/server`, `web/` | e2e Full, UI `full.spec.ts` |
| Agents on Linux hosts, hypervisors, VMs, containers, Docker/NAS, Kubernetes, OpenWrt with LuCI, macOS, Windows, FreeBSD | `cmd/lanscape-agent`, `.goreleaser.yaml`, `packaging/`, `deploy/` | CI `build-full` (all targets), `openwrt`, `docker`, `k8s` |
| Control channel always outgoing from the agent (WebSocket over TLS, mTLS) | `internal/agent/agent.go` (`session`), `internal/server/gateway.go` | e2e Full; `TestCAFlow` |
| Test traffic directly between agents; checks from agents or the server | `internal/testengine`, `internal/server/uptime.go` (points) | e2e Full; `TestServicesFlow` |
| One static server binary with embedded SPA; SQLite by default, PostgreSQL optional; time series in the database with downsampling, Prometheus export | `cmd/lanscape`, `internal/server/webdist.go`, `internal/store` (SQLite, `postgres.go`), daily aggregates (`PruneDaily`), `internal/server/metrics.go` | CI `test-go`, `test-postgres`; e2e Full `/metrics` |
| Static agent (`CGO_ENABLED=0`) with its own test and check engine; no `iperf3`, `ping`, `nmap` needed | `internal/testengine`, `internal/monitor` | CI `build-full`; `TestEcho`, `TestTCPPing` |
| Server without agents works as dashboard and uptime monitor | `internal/server/uptime.go` ("server" point) | `TestServicesFlow`, `TestHTTP` |

## §4. Registration and security

| Requirement | Implementation | Verification |
|---|---|---|
| One-time or reusable registration tokens with labels and expiry | `internal/store/repo.go` (agent tokens), `internal/server/api.go` | `TestAgentsRunsSettings`; e2e Full (registration) |
| mTLS with automatic certificate rotation | `internal/pki`, `internal/agent/agent.go` (`renew`), `/v1/renew` | `TestCAFlow`; e2e Full (certificates issued by the CA) |
| Test connections authenticated by a run token; the agent is no open traffic generator | `internal/testengine/responder.go` (grants), [decision 0004](decisions/0004-lstp-sessions.md) | `TestAuthFailures`, `TestRefused` |
| Local users, OIDC, TOTP 2FA, API tokens | `internal/server/auth.go`, `oidc.go`, `totp.go` | `TestPasswords`, `TestUsersSessionsTokens`, `TestOIDCLogin`, `TestVerifyJWTES256`, `TestCheckClaims` |
| Roles viewer / operator (runs, incident acknowledgement) / admin | `internal/server/auth.go` (`roleRank`, `v(role, …)` on every route) | `TestUsersSessionsTokens`, `TestActions` (operator) |
| Guest mode for the dashboard and status pages only | `internal/server/auth.go` (`guestOr`), `guestDashboard` | `TestGuestDashboard`, `TestStatusPages` |
| Monitor secrets (basic auth, headers, tokens) encrypted at rest and masked in the UI | `internal/store/secrets.go` (AES-GCM), `internal/server/secrets.go`, `notify.Mask` | `TestSecretsAtRest`, `TestValidationAndSecrets` |
| Agent limits: speed and duration; forbidden interfaces; active scanning off unless allowed per subnet; checks-only and respond-only modes | `--max-duration`, `--max-streams`, `--max-udp-mbps`, `--exclude`, `--scan-allow`, `--mode` (`cmd/lanscape-agent`) | `TestScanAllowed`, `TestScan`; e2e Full (`--exclude 'mgmt*'`) |
| One fixed data port with authentication | `:47700` (`--data-listen`), run tokens | `TestAuthFailures`; e2e |

## §5. Inventory

| Requirement | Implementation | Verification |
|---|---|---|
| Sent at start, on netlink changes and every 5 minutes | `internal/agent/agent.go` (`watch`, `Inventory`) | e2e Full |
| Interfaces: name, MAC, MTU, state, IPv4/IPv6, type (physical, bridge, bond, vlan, macvlan, veth, wireguard, tun …), parent, VLAN, bridge/bond members, bond mode, link speed (ethtool), unknown speed marked | `internal/netio` (netlink, `SIOCETHTOOL`) | `TestParsers` (`internal/netio`), e2e Full (VLAN and macvlan members) |
| Routing: tables, `ip rule`, gateways, policy routing | `internal/agent/sysinfo_linux.go` | `TestDefaultGateways`, `TestSysinfoParsers` |
| Neighbours: ARP/NDP, LLDP/CDP | `sysinfo_linux.go`, `internal/agent/lldp.go` | `TestParseLLDP` |
| Environment: bare-metal / VM / LXC / Docker / Kubernetes pod / OpenWrt; hypervisor parent (guests and their MAC from the hypervisor module); k8s node and CNI | `DetectEnv`, `DetectCNI` (`internal/agent/sysinfo.go`); Proxmox/libvirt NICs | `TestDetectEnv`, `TestDetectCNI`, `TestProxmox`, `TestLibvirt` |
| Resources: arch, CPU, RAM, disks (usage, SMART), uptime, OS and kernel, pending package updates | `sysinfo_linux.go`, `internal/agent/updates.go`, SMART in `internal/discovery/host.go` | `TestCountUpdates`, `TestSmartAndZpool` |
| Segments from interfaces | `internal/topo/topo.go` | `TestBuildSegments` |
| IPAM: used and free addresses per segment, overlap with DHCP pools | `internal/topo/ipam.go`, OpenWrt leases | `TestIPAMAndAnomalies`, `TestRouterLeasesInDevices` |
| Anomalies: duplicate IP and MAC, VIP on two nodes, one subnet on several VLANs, interface without carrier, new unknown MAC | `internal/topo/ipam.go` | `TestIPAMAndAnomalies`, `TestProblems` (`vip`) |

## §6. Network tests

| Requirement | Implementation | Verification |
|---|---|---|
| Reachability: ICMP and TCP connect, 3 packets | `internal/testengine/icmp.go`, echo test | `TestEcho`, `TestTCPPing`; e2e |
| Latency: RTT min/avg/p95/max, jitter, loss; 20 × 50 ms | `internal/testengine/icmp.go`, `topo.Probe` | `TestJudge`; e2e |
| MTU/PMTU 1500 and jumbo with DF | `netio.BindControl` (DF), runner phase 1 | e2e Full (`mtu` problem n1→rt) |
| TCP throughput A→B and B→A, 1 and N streams, 5 s, N = 4 | `internal/testengine/initiator.go`, `transfer.go` | `TestTCPDirections`; e2e |
| UDP: rate, loss, jitter (optional); bidirectional (optional) | `initiator.go` | `TestUDPDirections`; e2e Full (UDP results in segment C) |
| Aggregate: all pairs at once (manual) | `internal/server/runner.go` (`aggregate`) | `TestRunEndToEnd` |
| Internet: exit, public IP and speed through every gateway (optional) | `internal/testengine/internet.go`, `internal/server/internet.go` | `TestInternet`, `TestInternetChecks` |
| 6.2 socket bound to address and device; route check before the test; interface counter check after | `netio.BindControl`, route lookup, counters (`internal/testengine`) | e2e (`path_ok` everywhere); `TestAcceptanceProblems` (`path_mismatch`) |
| 6.2 known traps explained: macvlan parent/child, transparent proxy (ICMP ✓, TCP ✗), asymmetric routes and rp_filter, policy routing | `internal/topo/verdict.go` (`Problems`), hints in `web/src/lib/dict.ts` | e2e Mini and Full (`macvlan`, `tcp_intercepted`); `TestProblems` |
| 6.3 serial by default (one throughput test on the network at a time); parallel reachability and aggregate | `internal/server/runner.go` | `TestRunEndToEnd`; e2e |
| 6.3 time estimate, cancel, partial results kept | `runner.go` (`Estimate`, cancel), `/api/v1/runs/estimate`, `/api/v1/runs/cancel` | `TestRunEndToEnd`; UI |
| 6.3 schedules (nightly full, reachability every 5 minutes) | `internal/server/scheduler.go` | `TestParseSpec` |
| 6.4 expected speed = minimum link speed on the path or manual; verdicts 🟢 ≥ 85 %, 🟡 50–85 %, 🔴 < 50 % or error, ⚪ no data, 🟣 CPU-bound | `topo.ExpectedMbps`, `topo.Judge` | `TestExpected`, `TestJudge`, `TestAcceptanceProblems` |
| 6.4 separate verdicts for MTU, loss above 0.5 %, RTT above threshold | `topo.Judge` (`MTUOK`, `LossHigh`, `RTTHigh`) | `TestAcceptanceProblems` (`loss`, `rtt`) |

## §7. Map

| Requirement | Implementation | Verification |
|---|---|---|
| Devices as cards with type icons; devices without agents from discovery | `internal/server/mapgraph.go`, `mapdecor.go`, `discovery.DeviceType` | `TestDeviceType`, `TestMapDecorationAndPathHistory` |
| Interfaces as ports (name, IP, speed); segments as buses (subnet · VLAN · expected speed) | `mapgraph.go` | `TestMapDecorationAndPathHistory`; UI (map) |
| Nesting: guests in the hypervisor, pods in the node; collapsible | `decorateMap`, `web/src/components/NetMap.svelte` | `TestMapDecorationAndPathHistory` |
| Services as badges with icon and uptime status, can be hidden | `decorateMap`, "services" layer | `TestMapDecorationAndPathHistory` |
| Path edges coloured by verdict, labelled with speed | `mapgraph.go` | UI (map) |
| Unmanaged switches and uplinks inferred ("bottleneck X Gbit/s"), confirmable as a switch | `Bottlenecks`, `internal/server/switches.go` | `TestBottlenecks`, `TestConfirmSwitch` |
| External dependencies (Internet, provider, cloud nodes, VPN peers) in their own zone | `internal/server/mapexternal.go` | `TestMapExternalZone` |
| 7.2 automatic layout (ELK), manual positions kept | `NetMap.svelte` (cytoscape-elk, positions in local storage) | UI (map) |
| 7.2 layers (physical and segments / services / paths), filters (only problems, segment, group) | `web/src/lib/mapview.ts`, `MapPage.svelte` | UI (map) |
| 7.2 click opens a card: history, metrics, related services, "measure again", "open web interface" | `MapPage.svelte`, `pathhistory.go` | `TestMapDecorationAndPathHistory` |
| 7.2 PNG/SVG export, link to the map state, wall mode (full screen, auto refresh) | `MapPage.svelte` (`?wall=1`) | UI |
| 7.3 matrix node × node per segment, both directions, verdict colours | `web/src/components/Matrix.svelte` | UI (`full.spec.ts`, `mini.spec.ts`) |
| 7.4 unknown link speed: manual value, then inherited from the hypervisor bridge uplink, else "unknown" (absolute value only) | `topo.ExpectedMbps`, `proxmoxSpeed` / `inheritSpeed` (`internal/server/resources.go`), `unknown_speed` anomaly | `TestResourceMonitorsAndProxmoxSpeed`, `TestExpected` |

## §8. Discovery of services and devices

| Requirement | Implementation | Verification |
|---|---|---|
| Every source switched on separately; results go to the "Found" queue, not straight into monitoring | `--discover` (`cmd/lanscape-agent/modules.go`), `internal/server/discovery.go` | `TestServicesFlow` |
| Listening sockets: port, protocol, process, user; no network scan | `internal/discovery/sockets_linux.go` | `TestParseProcNet`, `TestCgroupAndPasswd` |
| Docker/Compose: containers, images, published ports, project, labels, healthcheck, state | `internal/discovery/docker.go` | `TestDocker`, `TestDockerAndSocketMerge` |
| Kubernetes (read-only): Services incl. LoadBalancer IP, Ingress and Gateway API routes (hosts, TLS), Deployments/StatefulSets/DaemonSets with readiness, PVCs | `internal/discovery/k8s.go`, `kubeconfig.go` | `TestKubernetes`, `TestKubernetesMerge` |
| Hypervisor (Proxmox API; libvirt): VMs and CTs, state, IP (guest agent), tags and notes, storages | `internal/discovery/proxmox.go`, `libvirt.go` | `TestProxmox`, `TestLibvirt`, `TestProxmoxCards` |
| OpenWrt (ubus/UCI): DHCP leases and host names, static leases, Wi-Fi clients, port forwards, SQM | `internal/discovery/openwrt.go` | `TestOpenWrtSource`, `TestOpenWrtParts`, `TestParseUCI` |
| mDNS/DNS-SD, SSDP/UPnP, NetBIOS/LLMNR | `internal/discovery/lan.go`, `netbios.go` | `TestMDNS`, `TestSSDP`, `TestNetBIOS`, `TestParseNBStat` |
| ARP/NDP neighbour tables, vendor from OUI | agent neighbours, `internal/oui` | `TestVendor`, `TestDiscoveredDevices` |
| Active scanning (explicit subnets, soft rate limit): popular TCP ports, banners | `internal/discovery/scan.go`, `--scan-allow` | `TestScan`, `TestScanAllowed` |
| Reverse proxies: Traefik API, Caddy admin API, nginx configs | `internal/discovery/proxy.go` | `TestProxies`, `TestProxyRouteMerge` |
| DNS servers: Technitium, Pi-hole, AdGuard APIs; AXFR when allowed | `internal/discovery/dns.go`, `axfr.go` | `TestDNSRecords`, `TestAXFR` |
| TLS certificates: SAN names and expiry | prober (`internal/fingerprint`), `CertInfo` | `TestProbeAndUserSignatures`; dashboard certificates widget |
| Prometheus targets, Home Assistant, Uptime Kuma (import) | `internal/server/imports.go` | `TestImports` |
| 8.2 HTTP fingerprint by title, favicon hash, headers, characteristic paths and JSON; 400+ signatures with icon, category and recommended monitor | `internal/fingerprint` (`signatures.yaml` → `signatures.json`) | `TestLibrary` (≥ 300), `TestIdentify`, `TestProbeAndUserSignatures`, `TestProbeCache` |
| 8.2 containers and workloads by image | `Library.IdentifyImage` | `TestIdentify`, `TestSetSignatures` |
| 8.2 signatures updated independently of releases; user signatures | `--signatures`, Settings → Signatures (`internal/server/signatures.go`, pushed with `config`) | `TestSignatureDistribution`, `TestSetSignatures` |
| 8.3 one service found in several ways merged into one card with all addresses (internal and external URL, host:port, workload) | `internal/catalog/catalog.go` | `TestKubernetesMerge`, `TestDockerAndSocketMerge`, `TestProxyRouteMerge` |
| 8.3 states new → added / ignored (rule) / hidden; auto-add rules ("all Ingress with TLS → HTTPS monitor + tile") | `internal/catalog/rules.go` | `TestRules`, `TestRulesAutoAdd` |
| 8.3 change feed: new service or device, port opened/closed, IP changed, container gone, new MAC | `internal/catalog/changes.go` | `TestDiff`, `TestServicesFlow` |

## §9. Uptime monitoring

| Requirement | Implementation | Verification |
|---|---|---|
| HTTP(S): code, response time, keyword or regex, JSON path, redirects, headers, basic/bearer | `internal/monitor/check.go` | `TestHTTP` |
| TCP/UDP with optional payload and expected reply | `check.go` | `TestTCPAndUDP` |
| ICMP availability and RTT | `check.go`, `testengine` (TCP fallback) | `TestTCPPing` |
| DNS record through a given server (UDP/TCP/DoT/DoH), expected value | `check.go`, `internal/monitor/dnsclient.go` | `TestDNSTransports` |
| TLS expiry warning N days ahead, chain, name | `check.go` | `TestHTTPSAndTLS` |
| Container state and Docker healthcheck | `resourceResult` (`internal/server/resources.go`) | `TestResourceMonitorsAndProxmoxSpeed` |
| Kubernetes: workload readiness, pod restarts, Pending, PVC status | `resourceResult`, `podStats` (`k8s.go`) | `TestK8sRestartsAndPending` |
| VM/CT running on the hypervisor (down when its storage is full) | `resourceResult`, `guestStorage` | `TestResourceMonitorsAndProxmoxSpeed` |
| Heartbeat (push URL); no signal → incident | `/api/push/{token}`, `uptime.go` | `TestHeartbeatCompositeSuppression` |
| Interface/path bound to the network verdict | `monitor.TypePath`, `pathResult` | `TestPathMonitor` |
| Domain registration expiry (RDAP) | `check.go` (domain) | `TestDomain` |
| Composite "A and (B or C)" | `internal/monitor/expr.go` | `TestExpr`, `TestHeartbeatCompositeSuppression` |
| 9.2 several observation points; incident when unavailable from ≥ N points; per-point results ("reachable from LAN, not from VLAN 100") | `uptime.go` (points, `min_failing`), monitor detail view | `TestServicesFlow` |
| 9.2 dependencies: children suppressed and grouped when a router or hypervisor fails ("32 services down, cause prx1 offline"); graph from the map plus manual parents | `parentDown` (`uptime.go`), `guestHosts` (`mapdecor.go`), grouping in `Monitors.svelte` | `TestHeartbeatCompositeSuppression`, `TestHypervisorGrouping` |
| 9.3 incident after N failed checks, acknowledgement and comments, automatic close | `uptime.go` (`record`), incident notes | `TestHeartbeatCompositeSuppression`, `TestServicesFlow` |
| 9.3 maintenance windows (planned and "now for 30 minutes"), no notifications, marked separately in statistics | `uptime.go` (`inMaint`), `DayStat.Maint` | `TestServicesFlow` |
| 9.3 channels: Telegram, e-mail, webhook, ntfy, Gotify, Discord, Slack, Matrix, PWA push | `internal/notify` | `TestTelegram`, `TestEmail`, `TestNtfy`, `TestChatChannels`, `TestWebhookSignature`, `TestWebPush` |
| 9.3 deduplication (one incident per monitor), escalation (repeat until acknowledged), quiet hours | `uptime.go` (`remind`), `notify.Common` | `TestQuietAndFilter` |
| 9.3 uptime % per day, week, month and year; response time charts; SLA targets | `uptimeStats`, `MonitorDetail.svelte` | `TestServicesFlow` |
| 9.4 status page: public or by link, service groups, 90-day history, incidents and planned work, custom domain and theme | `internal/server/status.go` | `TestStatusPages` |

## §10. Dashboard

| Requirement | Implementation | Verification |
|---|---|---|
| Tiles: icon, name, status, response time, short metric (free space of a NAS); groups, sorting, drag and drop | `serviceViews`, `tileMetric` (`api_services.go`), `Dashboard.svelte` | `TestTileMetric`, `TestServicesFlow`; UI |
| Smart links: internal or external address depending on where the user comes from | `smartURL`, `clientIsLocal` | `TestServicesFlow` |
| Search and command palette (`Ctrl/⌘+K`) over services, devices, IP and MAC; open, measure network to…, Wake-on-LAN | `web/src/components/Palette.svelte`, `/api/v1/search` | `TestActions`; UI |
| Widget: overall status and open incidents | `apiDashboard` | `TestServicesFlow` |
| Widget: node resources (CPU, RAM, disk, temperature), fullest disks | `apiDashboard` (`AgentResources`) | `TestHardwareWidgets` |
| Widget: last run, map thumbnail, current WAN/LAN traffic of the router, scheduled Internet speed, provider outage history | `NetworkSummary`, `internal/server/traffic.go`, `internet.go` | `TestTrafficWidget`, `TestInternetChecks` |
| Widget: storage (pools, disks, SMART warnings, free space, hypervisor storages) | `hardwareWidgets` | `TestHardwareWidgets`, `TestSmartAndZpool` |
| Widget: backups (last success per heartbeat, "long ago") | `BackupView` | `TestHeartbeatCompositeSuppression` |
| Widget: certificates and domains expiring soon | `CertExpiry` | `TestHTTPSAndTLS`, `TestDomain` |
| Widget: updates (OS packages on nodes, newer container image tags) | `Updates`, `internal/server/images.go`, `internal/registry` | `TestCountUpdates`, `TestImageUpdates`, `TestNewer`, `TestTagsWithTokenAndPages` |
| Widget: change feed | `store.Changes` | `TestDiff` |
| Widget: UPS (NUT) charge, load, runtime | `discovery.NUT` | `TestNUT`, `TestHardwareWidgets` |
| Widget: power (smart plugs, IPMI) when connected | `internal/discovery/power.go` | `TestPlugPower`, `TestParseIPMIPower`, `TestHardwareWidgets` |
| Widget: notes and links (runbooks) | `Board.Notes` (`boards.go`) | `TestBoards` |
| Several dashboards with role visibility | `internal/server/boards.go` | `TestBoards` |
| PWA: install on the phone, offline cache of the last state | `web/public/manifest.webmanifest`, `web/public/sw.js` | UI |

## §11. Actions

| Requirement | Implementation | Verification |
|---|---|---|
| Wake-on-LAN for devices with a known MAC | `internal/wol`, `internal/server/actions.go` | `TestMagicPacket`, `TestBroadcast`, `TestSendTo`, `TestActions` |
| Restart of a container, workload, VM/CT through its API, operators only | `internal/discovery/actions.go` | `TestRestartDocker`, `TestRestartWorkload`, `TestRebootGuest`, `TestActions` |
| Repeat a network test or a check | `/api/v1/runs` (`involve`), `/api/v1/monitors/{id}/check` | `TestRunEndToEnd`, `TestServicesFlow` |
| Audit log: who, what, when, result | `s.audit` (`internal/server`), Settings → Audit | `TestActions` |

## §12. UI sections

| Requirement | Implementation | Verification |
|---|---|---|
| Dashboard, Network (check all with live progress, matrix, explained problems), Map, Services (catalog and "Found" queue), Monitors/incidents (history, maintenance, status pages), Devices (agents, discovered devices, IPAM), Change feed, Settings (agents and tokens, discovery, notifications, users, signatures, import/export) | `web/src/pages/*.svelte`, `web/src/components/*.svelte` | UI; `svelte-check` and `vitest` in CI `web` |
| "Add node" wizard with a command per platform | Settings → Agents (`install` commands from `api.go`) | e2e Full (token creation) |
| Dark and light theme, phone layout, Russian and English | `web/src/app.css`, `web/src/lib/dict.ts` | UI |

## §13. Agent installation

| Requirement | Implementation | Verification |
|---|---|---|
| Linux: one `curl … \| sh` command, binary + systemd | `scripts/install.sh`, `packaging/systemd` | CI `build-full` |
| Debian/Ubuntu/Proxmox `.deb` and apt repository; RHEL/Fedora `.rpm`; Alpine `.apk` with OpenRC | nfpm in `.goreleaser.yaml`, `scripts/publish-repos.sh` (apt and rpm repositories on Pages) | CI `build-full`; release `pages` |
| Docker/Compose for NAS: multi-arch image, `compose.yaml` with host network, `NET_RAW`, `NET_ADMIN`, optional read-only Docker socket | `packaging/docker/lanscape-agent.Dockerfile`, `deploy/compose/agent-nas.yaml` | CI `docker` |
| Kubernetes: Helm chart and manifests, DaemonSet in hostNetwork and pod-network modes, read-only RBAC, control-plane tolerations | `deploy/helm/lanscape` | CI `k8s` (helm lint and template) |
| OpenWrt: agent and `luci-app-lanscape` as `.ipk` (≤ 24.10) and `.apk` (25.12+) from the SDK; Mini agent for small flash; binary ≤ 6–8 MB | `packaging/openwrt`, `scripts/build-openwrt-agents.sh` (8 MiB budget) | CI `openwrt` (6 targets) |
| Proxmox VE: `.deb` + hypervisor module (read-only token) | `--proxmox-url`, `--proxmox-token` | `TestProxmox` |
| macOS: Homebrew, launchd | `scripts/publish-repos.sh` (formulas), `packaging/launchd`, `service install` | CI `build-full` (darwin) |
| Windows: MSI, service | `packaging/windows/lanscape-agent.wxs`, `scripts/build-msi.sh`, `service_windows.go` | release `msi` job (allowed to fail; the zip is always published) |
| FreeBSD / pfSense / OPNsense: binary + rc.d | `packaging/freebsd` | CI `build-full` (freebsd) |
| Server: one binary, Docker image, Helm chart, Compose | `cmd/lanscape`, `packaging/docker/lanscape.Dockerfile`, `deploy/helm`, `deploy/compose/server.yaml` | CI `docker`, `k8s`; release `helm` (OCI chart) |
| 13.1 architectures linux amd64/386/arm64/armv7/armv6/armv5/mips(le) softfloat/mips64(le)/riscv64/ppc64le/s390x, darwin amd64/arm64, windows amd64/arm64, freebsd amd64/arm64; static builds | `.goreleaser.yaml` (agent builds), `CGO_ENABLED=0` | CI `build-full` |
| 13.1 multi-arch Docker manifests | `docker buildx` in CI | CI `docker` |
| 13.1 releases with checksums and signature (cosign) | release workflow (`checksums.txt`, `cosign sign-blob`) | release job |
| 13.1 agent auto-update from the panel with stable/beta channels | `internal/server/agentupdate.go`, `internal/agent/selfupdate.go` | `TestAgentUpdates`, `TestCompareVersions`, `TestAssetName`, `TestExtractBinary`, `TestReplaceBinary` |
| 13.2 LuCI: connection status, token, server; test interfaces with `wan` excluded; speed and duration limits; discovery checkboxes (leases, Wi-Fi, forwards); "Check from this router" and last results; backend on rpcd/ucode, UCI `/etc/config/lanscape`; 🟣 when limited by the router CPU | `packaging/openwrt/luci-app-lanscape` (`view/agent.js`, `lanscape.uc`), local socket (`internal/agent/local.go`) | CI `openwrt`; e2e Full (local socket `status`/`last`) |

## §14. Agent engine

| Requirement | Implementation | Verification |
|---|---|---|
| TCP with N streams, 1 s warm-up not counted; UDP rate, loss, jitter | `internal/testengine/transfer.go`, `initiator.go` | `TestTCPDirections`, `TestUDPDirections` |
| ICMP over raw or unprivileged sockets with TCP-ping fallback | `internal/testengine/icmp.go` | `TestTCPPing` |
| PMTU; monotonic timers; agent CPU reported | `testengine` (`epoch`, CPU in results) | `TestJudge` (🟣) |
| Optional client compatible with `iperf3 -s` | `internal/testengine/iperf3.go`, `POST /api/v1/iperf3` | `TestIperf3`, `TestIperf3Busy`, `TestIperf3API`; checked against iperf3 3.12 |
| Checks: HTTP/1.1, HTTP/2, TLS chain validation; DNS over UDP/TCP/DoT/DoH; TCP/UDP probes; ICMP | `internal/monitor` | `TestHTTP`, `TestHTTPSAndTLS`, `TestDNSTransports`, `TestTCPAndUDP` |
| Discovery: sockets, Docker, k8s, hypervisor, ubus, mDNS/SSDP, ARP, rate-limited port scan, HTTP fingerprint | `internal/discovery` | see §8 |
| Load limited by configuration: check frequency, parallelism, scan limit | `--max-*`, monitor intervals (minimum 10 s), scan rate limit | `TestScan`, `TestValidateAndHelpers` |

## §15. API and integrations

| Requirement | Implementation | Verification |
|---|---|---|
| REST + WebSocket, OpenAPI | `internal/server/api*.go`, `GET /api/v1/ws` and SSE (`events.go`), `docs/openapi.yaml` | `TestEventsWebSocket`; CI `openapi` (redocly lint) |
| Prometheus `/metrics`: paths, monitors, agents, devices, interface traffic; Grafana dashboards | `internal/server/metrics.go`, `deploy/grafana/*.json` | e2e Full (`/metrics`), `TestTrafficWidget` |
| GitOps: services, monitors, segments, dashboards and rules in YAML with import and export | `internal/server/gitops.go`, `lanscape config export\|apply`, `serve --config` | `TestGitOpsConfig`, `TestConfigDocExample`, `TestExpandVars` |
| Import from Uptime Kuma, Homepage/Homer/Dashy tiles, Prometheus targets | `internal/server/imports.go`, `import_tiles.go` | `TestImports`, `TestImportTiles`, `TestParseTiles` |
| Webhooks: run finished, incident, new device | `emit` (`server.go`) | `TestEventWebhooks` |
| Home Assistant sensors (optional) | `internal/server/homeassistant.go` (REST state and generated package) | `TestHomeAssistant` |

## §16. Non-functional requirements

| Requirement | Implementation | Verification |
|---|---|---|
| Idle agent < 15 MB RAM, ~0 % CPU, < 1 KB/s control traffic | memory trimming (`trimMemory`), no idle keep-alive connections, inventory resent only on change or every 5 min, ping every 30 s | e2e Full: agents 6–8 MB private memory (≈ 15 MB resident including ~8 MB of shared, clean pages of the binary) |
| Server < 250 MB RAM for 100 agents and 500 monitors; SQLite by default | `internal/server`, `internal/store` | e2e Full: 31 MB resident with 5 agents and the services scenario |
| Full run 4 nodes × 4 segments ≤ 5 min; 10 × 3 ≤ 15 min (serial) | runner phases (parallel reachability, serial throughput), estimate before the run | e2e Full: 4 nodes × 3 segments, 30 paths, finishes within the test (≈ 4 min) |
| Throughput within ±5 % of `iperf3` on the same path | `internal/testengine` | e2e Full: 94.9 vs 95.2 Mbit/s on the shaped segment |
| Monitors: minimum interval 10 s; 500 monitors per server | `minIntervalS` (`uptime.go`) caps every monitor and import at 10 s; scheduler with a worker pool (`--parallel`) | code (`uptime.go`, `imports.go`) |
| First discovery report ≤ 2 min after connection; scanning not faster than the rate limit | collector starts 10 s after start (`Collector.Run`), scan rate limit | `TestScan` |
| Raw data 30 days, aggregates 2 years (configurable) | `RetentionDays`, `AggregateDays`, `housekeeping` | `TestAgentsRunsSettings` |
| Agent reconnect with backoff; server survives restarts; backup and restore with one command | `agent.Run` (backoff), `lanscape backup` / `restore` | e2e Full (agents reconnect after server start); CI `test-go` |
| Linux kernel ≥ 4.x; OpenWrt ≥ 23.05 | netlink features used are available on 4.x; OpenWrt SDK 23.05/24.10/25.12 | CI `openwrt` |
| License MIT or Apache-2.0 | GPL-3.0-or-later by owner decision | [decision 0001](decisions/0001-license-gpl-3.md) |

## §17. Stages

| Stage | Release |
|---|---|
| 0 Mini | v0.1.0 |
| 1 MVP network Full (server + agent, Mini agents, inventory, matrix, check all) | v0.2.0 |
| 2 MVP services (sockets, Docker, k8s discovery; HTTP/TCP/ICMP/TLS monitors; incidents, Telegram, webhook; tile dashboard) | v0.3.0 |
| 3 Map (graph, nesting, services, path history) | v0.4.0 |
| 4 Kubernetes and hypervisors (Helm, two DaemonSet modes, Proxmox, dependencies and suppression) | v0.5.0 |
| 5 OpenWrt (packages, LuCI, leases and Wi-Fi clients, 🟣) | v0.6.0 |
| 6 Extensions (mDNS/SSDP/scanner, fingerprints, status page, heartbeat, widgets, PWA, imports, macOS/Windows/FreeBSD) | v1.0.0 |

## §18. Acceptance criteria

| Criterion | Verification |
|---|---|
| "Check all" from the panel runs a full run without SSH; matrix per segment, map and problem list afterwards | e2e Full; UI `full.spec.ts` |
| A node with interfaces in several segments: every path measured separately, traffic confirmed on the declared interface | e2e Full (`path_ok` on every measured path, 30 paths over 3 segments) |
| Artificial problems found and explained: VLAN switched off, TCP intercepted by a proxy, reduced MTU, 100M port instead of 1G | e2e Full (`tcp_intercepted`, `mtu`, `macvlan`); `TestAcceptanceProblems` (`unreachable`, `slow`, `link_speed`) |
| After connecting agents to k8s, Docker and the hypervisor, web services with correct icons appear in "Found"; one click adds them to monitoring and the dashboard | `TestServicesFlow`, `TestKubernetesMerge`, `TestProxmoxCards`; e2e Full (services scenario) |
| A stopped container or workload gives an incident and a notification within 2 check intervals; a hypervisor failure gives one grouped incident | `TestResourceMonitorsAndProxmoxSpeed`, `TestHeartbeatCompositeSuppression`, `TestHypervisorGrouping` |
| Agent installs on OpenWrt from a package and is configured in LuCI, on a NAS with `docker compose up -d`, in k8s with `helm install` | CI `openwrt`, `docker`, `k8s` |

## §20. Reference scenario

The namespace topology of `test/e2e/topo.sh` reproduces the reference network: an untagged LAN,
VLAN 300 and VLAN 301 (shaped to 100 Mbit/s) with a macvlan "pod", a router with a reduced MTU and
a transparent proxy on one node.

| Expected result | Verification |
|---|---|
| VLAN throughput close to the shaped rate, green verdicts | e2e Mini and Full (segment C 85–115 Mbit/s, expected 100, green) |
| Discovery: Ingresses with application icons, LoadBalancer services, Proxmox VMs and CTs, NFS on the NAS, the DNS server, router and Proxmox web interfaces | `TestKubernetes`, `TestKubernetesMerge`, `TestProxmox`, `TestProxmoxCards`, `TestParseProcNet` (NFS sockets), `TestDNSRecords`, `TestIdentify` (OpenWrt, Proxmox) |
| Default monitors after one click: HTTPS of every Ingress, TCP 6443 on the VIP, DNS via the resolver, NFS 2049, TLS expiry, Longhorn backup heartbeat | `TestRulesAutoAdd`, `TestServicesFlow`, recommended monitors in the signatures |
| Regressions caught and explained: TCP intercepted by a transparent proxy (ICMP 🟢, TCP 🔴); host cannot reach macvlan pods → macvlan sibling recommended; VIP on two nodes; full hypervisor root → disks widget red and incidents of the VMs on that storage | e2e Mini and Full (`tcp_intercepted`, `macvlan` with the sibling hint); `TestIPAMAndAnomalies` (VIP); `TestResourceMonitorsAndProxmoxSpeed` (guest down on a full storage), `TestHardwareWidgets` |
