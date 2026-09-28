# 0006. E2E topology: separate management network

## Context

Agents must reach the server over the control plane while all tested segments are part of the
measurements.

## Decision

The e2e topology adds a management bridge (`192.168.250.0/24`, interfaces `mgmt0`) in the root
namespace. The server runs in the root namespace; agents exclude `mgmt*`. The router `rt` runs an
agent so that the MTU 1400 port is measured. `E2E_NO_VLAN=1` replaces VLAN sub-interfaces with
separate bridges for kernels without `8021q` (developer containers); CI always uses real VLANs.

## Consequences

Segment detection in e2e sees exactly the three tested segments.
