# 0004. LSTP/1: control connection plus data connections

## Context

The brief fixes the frame format and the HELLO/CHALLENGE/AUTH/READY handshake but not how
multi-stream tests, results and sender CPU reports travel.

## Decision

- Each test opens one control connection (`ROLE=0`) and `STREAMS` data connections (`ROLE=1`).
  Every connection authenticates with the run token; data connections must repeat the `SESSION`
  issued on the control connection.
- Data connections carry raw bytes only; results (`RESULT`, `SENDER_STAT`) travel on the control
  connection after the senders half-close their data connections.
- Mini runs every direction from its sending side ("forward" only): for a pair A–B the server
  runs A→B initiated by A and B→A initiated by B. This detects asymmetric interception (a
  transparent proxy in front of B breaks connections to B but not connections from B). Reverse
  mode exists for agents behind NAT and is used by Full.
- `run_token` is issued per test step with a 10-minute TTL.

## Consequences

Tests stay independent of TCP connection ordering, results cannot be confused with test data,
and the same responder code serves Mini and Full initiators.
