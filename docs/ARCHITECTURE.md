# Architecture

```
 browser ──► lanscape (server)                                  ┌──────────────┐
            ├─ HTTP :8080  UI (Svelte, embedded) + REST API + SSE + /metrics
            ├─ gateway :8443  /v1/register (token) · /v1/renew · /v1/agent (mTLS WebSocket)
            ├─ Mini port :47701  Lanscape Mini agents as "lite" nodes
            ├─ runner  plans paths per segment, parallel reachability, serial throughput
            ├─ scheduler  "every 5m", "hourly", "daily 02:00"
            └─ store  SQLite (WAL) / PostgreSQL · pki (CA in data/pki)
                  ▲ control (outbound from agents)
      ┌───────────┴───────────┐                      ┌───────────────────────┐
      │ lanscape-agent        │◄── LSTP/1 :47700 ──►│ lanscape-agent / lsm-agent│
      │ inventory · tests     │    test traffic      │ responder (token per run) │
      └───────────────────────┘                      └───────────────────────┘
```

## Packages

| Package | Responsibility |
|---|---|
| `internal/proto` | LSTP/1 codec shared with Mini, Full control-plane message types |
| `internal/netio` | interfaces (netlink), link speed (ethtool ioctl), counters, `ip route get`, `SO_BINDTODEVICE`/`IP_BOUND_IF` |
| `internal/testengine` | responder (TCP+UDP :47700, grants, limits) and initiator (echo, TCP 1/N streams, UDP, bidirectional), ICMP ping/PMTU |
| `internal/agent` | registration with CA pinning, mTLS WebSocket, inventory (§5), netlink change watch, test execution, module hooks |
| `internal/server` | API, auth (argon2id, sessions, API tokens, TOTP), gateway, Mini port, hub of agents, runner, scheduler, metrics, map graph |
| `internal/topo` | segments, expected speed (§7.4), verdicts, problems, IPAM, anomalies |
| `internal/store` | migrations and repositories |
| `internal/pki` | CA, gateway certificate, agent certificates |
| `web/` | Svelte 5 SPA, built into `internal/server/webdist` |
| `mini/` | Lanscape Mini in C (`lsm-agent`, `lsm-server`) |

## Agent lifecycle

1. First start: the agent creates a key and a CSR, `POST /v1/register` with the token over TLS.
   The server CA is not trusted yet: the agent accepts the presented chain only if its root
   matches `--ca-fingerprint` (when given) and the CA returned in the response signed it.
2. The server issues a client certificate (CN = agent id, OU = `lanscape-agent`) valid for a year.
3. The agent connects to `wss://server:8443/v1/agent` with mTLS, sends `hello`, gets `welcome`, sends
   its inventory and then waits for requests (`grant`, `test`, …). Inventory is resent on netlink
   changes and every 5 minutes; the server pings every 30 s.
4. 30 days before expiry the agent calls `/v1/renew` with a new CSR.
5. Deleting the agent removes it from the database; the gateway rejects its certificate.

## A run

1. Snapshot online agents, build segments (subnet + VLAN; untagged members join the tagged
   segment of the same subnet), apply manual expected speeds.
2. Paths are ordered pairs of members of different nodes within a segment.
3. Phase 1 in parallel (at most one initiator test per node): ping (20 × 50 ms), TCP echo,
   PMTU 1500 with DF, jumbo when both MTUs are larger.
4. Phase 2 strictly serially (one throughput test on the network at a time): TCP with 1 and N
   streams, optionally UDP and bidirectional. Each test gets its own run token, granted to the
   responder over its control channel first.
5. Verdicts, problems and anomalies are computed, the report is stored as JSON, metrics are
   exported, webhooks are called and `run.done` is published over SSE.
