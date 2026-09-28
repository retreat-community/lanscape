# Lanscape protocols

This document specifies the wire protocols shared by Lanscape Mini (`mini/`) and Lanscape Full
(`internal/proto`). All multi-byte integers are in network byte order (big-endian).

| Channel | Port | Transport | Used by |
|---|---|---|---|
| Data plane (LSTP/1) | 47700/tcp (+udp for UDP tests) | TCP/UDP, token-authenticated per run | Mini and Full agents |
| Mini control plane | 47701/tcp | TCP, shared-token HMAC | `lsm-agent` → `lsm-server`, `lsm-agent` → `lanscape` (lite nodes) |
| Full control plane | 8443/tcp | WebSocket over TLS, mTLS after registration | `lanscape-agent` → `lanscape` |

## 1. Framing (LSTP/1)

Every message on the data plane and on the Mini control plane is a frame:

```
+------+------+---------+------+------------+------------------+
| 'L'  | 'S'  | version | type | length u16 | payload (TLV...) |
+------+------+---------+------+------------+------------------+
   1      1       1        1        2           length bytes
```

- `version` is `1`. A receiver closes the connection on an unknown magic or version.
- `length` is the payload length (0–65535).
- The payload is a sequence of TLV items: `tag u8 | len u16 | value`. A payload that does not
  split exactly into TLV items is malformed and the connection is closed.
- Unknown tags are ignored, so new optional fields can be added without a version bump.
  Removing or changing the meaning of a tag, or changing the handshake, requires `version = 2`.
- Integers use the width given in the tag table; a value of the wrong length is treated as absent.
- Strings are UTF-8 without a terminating NUL. Nested structures (`IFACE`) carry a TLV
  sequence as their value.

### 1.1. Tags

| Tag | Name | Type | Notes |
|---|---|---|---|
| 0x01 | AGENT_ID | string | |
| 0x02 | RUN_ID | u32 | run identifier issued by the server |
| 0x03 | TEST_KIND | u8 | 1 `TCP_THROUGHPUT`, 2 `UDP_THROUGHPUT`, 3 `ECHO` |
| 0x04 | DIRECTION | u8 | 0 forward (initiator sends), 1 reverse (responder sends), 2 bidirectional |
| 0x05 | STREAMS | u8 | number of data connections |
| 0x06 | DURATION_MS | u32 | measured duration, warm-up excluded |
| 0x07 | NONCE | 16 bytes | |
| 0x08 | MAC | 32 bytes | HMAC-SHA256 |
| 0x09 | ROLE | u8 | 0 control connection, 1 data connection |
| 0x0a | SESSION | u32 | issued by the responder in `READY` of the control connection |
| 0x0b | STREAM_IDX | u8 | |
| 0x0c | BYTES | u64 | |
| 0x0d | WINDOW_US | u64 | measurement window |
| 0x0e | CPU | u16 | CPU load of the reporting side during the test, per mille |
| 0x0f | SEQ | u32 | |
| 0x10 | TS_US | u64 | |
| 0x11 | ERR_CODE | u8 | 1 auth, 2 unknown run, 3 unsupported, 4 limit, 5 busy, 6 protocol |
| 0x12 | ERR_MSG | string | |
| 0x13 | UDP_RATE_KBPS | u32 | offered UDP load |
| 0x14 | UDP_PORT | u16 | |
| 0x15 | PKT_SIZE | u16 | UDP datagram payload size |
| 0x16 | PKTS_SENT | u64 | |
| 0x17 | PKTS_RECV | u64 | also: total received bytes incl. warm-up in `RESULT` |
| 0x18 | JITTER_US | u32 | RFC 3550 interarrival jitter |
| 0x19 | LOST | u64 | |
| 0x1a | IF_TX | u64 | interface TX counter delta |
| 0x1b | IF_RX | u64 | interface RX counter delta |
| 0x1c | PATH_OK | u8 | 1 when interface counters confirm the traffic |
| 0x1d | VERSION | string | software version |
| 0x1e | WARMUP_MS | u32 | default 1000 |
| 0x40–0x4e | IFACE, IF_NAME, IF_MAC(6), IF_ADDR4(4), IF_PREFIX u8, IF_VLAN u16, IF_SPEED u32 (Mbit/s, 0 unknown), IF_MTU u32, IF_FLAGS u32 (1 up, 2 carrier), IF_KIND, IF_PARENT, HOSTNAME, HOST_ID, ARCH, CAPS u32 | | inventory |
| 0x50–0x67 | CMD_ID u32, CMD_KIND u8, SRC_DEV, SRC_ADDR(4), DST_ADDR(4), COUNT u16, INTERVAL_MS u16, SIZE u16, RUN_TOKEN(32), TTL_S u32, STATUS u8, RTT_MIN/AVG/MAX_US u32, SENT u16, RECV u16, PEER_CPU u16, BPS u64, ROUTE_DEV, RTT_P95_US, SERVER_MAC(32), LOCAL_CPU u16, DST_PORT u16 | | Mini control plane |

Capability bits (`CAPS`): 1 lite (Mini agent), 2 ICMP, 4 TCP tests, 8 UDP tests.

## 2. Data plane

### 2.1. Run tokens

For every test the server generates a `run_id` (u32) and a 32-byte random `run_token`, valid for
at most 10 minutes. Before the initiator is told to start, the responder receives the pair over
its authenticated control channel (`GRANT` in Mini, `grant` message in Full) and acknowledges it.
A responder only accepts data-plane connections for runs it holds a live grant for, so an agent
cannot be used as a traffic generator by third parties.

### 2.2. Handshake

Every TCP connection of a test (the control connection and each data connection) starts with:

```
initiator                                   responder (port 47700)
HELLO{AGENT_ID, RUN_ID, TEST_KIND, DIRECTION, STREAMS, DURATION_MS, NONCE, ROLE, SESSION, STREAM_IDX}
                          ─────────────────►
                          ◄───────────────── CHALLENGE{NONCE (nonce2)}
AUTH{MAC = HMAC-SHA256(run_token, nonce || nonce2)}
                          ─────────────────►
                          ◄───────────────── READY{SESSION}
```

- If `RUN_ID` has no live grant, the responder closes the connection immediately without a reply.
- A wrong `MAC` closes the connection immediately.
- Limits violations (duration, streams) are answered with `ERROR{ERR_CODE=4}`.
- The handshake must complete within 3 seconds.
- The control connection (`ROLE=0`) gets a fresh random `SESSION`. Data connections (`ROLE=1`)
  repeat that `SESSION` and must arrive within 5 seconds.

### 2.3. Test kinds

**ECHO (TCP ping).** After `READY` the initiator sends `ECHO_REQ{SEQ, TS_US}` and the responder
returns the same payload as `ECHO_REP`. Used for TCP reachability and TCP RTT.

**TCP_THROUGHPUT.** After all `STREAMS` data connections are ready the initiator sends `START` on
the control connection. The sending side writes a pre-filled buffer on every data connection for
`WARMUP_MS + DURATION_MS`, then half-closes (`shutdown(SHUT_WR)`). The receiving side counts bytes
that arrive later than first byte + warm-up; the window ends with the last received byte.

- Forward: the responder receives and replies on the control connection with
  `RESULT{BYTES, WINDOW_US, PKTS_RECV (total), CPU, IF_RX, PATH_OK}`.
- Reverse: the responder sends and replies with `SENDER_STAT{BYTES, CPU, IF_TX, PATH_OK}`; the
  initiator computes the result.
- Bidirectional (Full): both directions run at the same time over separate data connections,
  each side reports as receiver for its inbound direction.

The result is always computed by the **receiving** side; the sending side reports its CPU load.

**UDP_THROUGHPUT** (Full). The initiator sends `START{UDP_RATE_KBPS, PKT_SIZE, UDP_PORT}`. Datagrams
go to port 47700/udp (or the given port) with a 20-byte header:

```
'L' 'U' | reserved u16 | SESSION u32 | SEQ u32 | send TS_US u64 | padding...
```

Datagrams with an unknown `SESSION` are dropped. The receiver reports
`RESULT{BYTES, WINDOW_US, PKTS_SENT (highest SEQ+1), PKTS_RECV, LOST, JITTER_US, CPU}`.

Other frames: `BYE` (0x0b) ends a session, `ERROR` (0x0a) carries `ERR_CODE`/`ERR_MSG`.

### 2.4. Binding and path verification

- Every test socket is bound to the source address and to the device (`SO_BINDTODEVICE` on Linux,
  `IP_BOUND_IF` on macOS).
- Before a test the agent resolves the route for the destination: netlink `RTM_GETROUTE` with
  source and output interface in Full, `/proc/net/route` longest prefix match plus `SIOCGIFINDEX`
  in Mini. If the route leaves through another interface the test is not run and the result is
  `route_mismatch` ("path does not match").
- After the test the interface counters are compared: the sender's TX delta must be at least 90%
  of the payload bytes it sent (receiver: RX delta ≥ 90% of received bytes). Otherwise the result
  is `counter_mismatch`.

## 3. Mini control plane (port 47701)

The agent always dials the server and reconnects with exponential backoff (1 s … 30 s).

```
agent                                             server
C_HELLO{AGENT_ID, VERSION, NONCE, HOSTNAME, HOST_ID, ARCH, CAPS, DST_PORT} ──►
                                             ◄── C_CHALLENGE{NONCE (nonce2)}
C_AUTH{MAC = HMAC(token, "agent" || nonce || nonce2)} ──►
                                             ◄── C_WELCOME{SERVER_MAC = HMAC(token, "server" || nonce2 || nonce)}
C_INVENTORY{IFACE{...}...} ──►
```

Both sides prove knowledge of the shared token; the labels prevent reflection. After that:

| Frame | Dir | Payload |
|---|---|---|
| `C_INVENTORY` 0x24 | A→S | repeated `IFACE` (sent on connect, on change, and every 5 minutes) |
| `C_PING`/`C_PONG` 0x25/0x26 | both | keepalive every 30 s, connection dropped after 90 s of silence |
| `C_GRANT` 0x27 | S→A | `RUN_ID, RUN_TOKEN, TTL_S` |
| `C_GRANT_ACK` 0x28 | A→S | `RUN_ID` |
| `C_CMD` 0x29 | S→A | `CMD_ID, CMD_KIND` (1 ping, 2 PMTU probe, 3 LSTP test), `SRC_DEV, SRC_ADDR, DST_ADDR, DST_PORT, COUNT, INTERVAL_MS, SIZE`, and for LSTP `RUN_ID, RUN_TOKEN, TEST_KIND, DIRECTION, STREAMS, DURATION_MS` |
| `C_CMD_RESULT` 0x2a | A→S | `CMD_ID, STATUS, ROUTE_DEV, SENT, RECV, RTT_*, BYTES, WINDOW_US, BPS, LOCAL_CPU, PEER_CPU, PATH_OK, IF_TX, ERR_MSG` |

`STATUS` values: 0 ok, 1 route_mismatch, 2 unreachable, 3 refused, 4 protocol (connection
accepted by something that does not speak LSTP — typically a transparent proxy), 5 auth_fail,
6 timeout, 7 counter_mismatch, 8 no_device, 9 unsupported, 10 internal, 11 busy, 12 msgsize
(local MTU smaller than the probe), 13 limit.

The PMTU probe sends ICMP echo requests of `SIZE` bytes (IP packet size) with DF set
(`IP_PMTUDISC_PROBE`); the path passes when at least one reply arrives.

TLS is not built into Mini: run it in a trusted LAN or behind a tunnel / reverse proxy.

## 4. Full control plane (port 8443)

See [ARCHITECTURE.md](ARCHITECTURE.md) for the agent lifecycle. Messages are JSON objects over a
WebSocket (`wss://server:8443/v1/agent`), one object per WebSocket text message:

```json
{"type": "inventory", "id": "…", "data": {…}}
```

- Registration: `POST https://server:8443/v1/register` with `{"token", "name", "csr"}` (PEM CSR)
  over server-authenticated TLS without a client certificate. The server returns
  `{"agent_id", "cert", "ca"}`. The CA fingerprint can be pinned with `--ca-fingerprint`.
- All other requests require a client certificate issued by the server CA (mTLS).
- The agent renews its certificate (`POST /v1/renew` with a new CSR) when less than 30 days of
  validity remain.
- Message types: `hello`, `inventory`, `grant`, `grant_ack`, `test`, `test_result`, `discovery`,
  `check`, `check_result`, `action`, `action_result`, `ping`, `pong`, `config`.

Protocol versions are negotiated in `hello` (`"proto": 1`); the server rejects agents with a
higher major version and asks for an upgrade.
