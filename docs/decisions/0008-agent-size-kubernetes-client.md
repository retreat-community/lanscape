# 0008. Minimal Kubernetes and Docker clients in the agent

## Context

`lanscape-agent` must stay within 6–8 MB for OpenWrt (§13). The static binary is already about
6.8 MB on arm64. `k8s.io/client-go` (allowed by the brief) adds roughly 10 MB and hundreds of
transitive packages; the Docker SDK is not allowed.

## Decision

- Kubernetes discovery uses a small read-only REST client (`internal/discovery/k8s.go`) with the
  in-cluster service account token and CA, or a kubeconfig path with a bearer token. It lists
  Services, Ingresses, Gateway API HTTPRoutes, Deployments, StatefulSets, DaemonSets, Pods and PVCs
  as JSON; only the fields Lanscape uses are decoded.
- Docker discovery talks to the Docker Engine HTTP API over the Unix socket.
- `client-go` is not used.

## Consequences

The agent stays within the OpenWrt budget. Kubernetes API changes that affect the few decoded
fields have to be followed manually; the resources used are stable (`v1`, `apps/v1`,
`networking.k8s.io/v1`, `gateway.networking.k8s.io/v1`).

## Addendum: discovery and checks (v0.3)

Service discovery, HTTP fingerprinting and availability checks add about 0.9 MB to the agent
(`linux/mipsle`, stripped: 7.9 → 8.8 MB). The built-in signature library is embedded as JSON generated
from `signatures.yaml` (`go generate ./internal/fingerprint`), so the parser for the built-in library
does not need YAML. Builds with the `lanscape_small` tag (OpenWrt packages) also drop YAML support for
user signature files and kubeconfig files (JSON only), which gives 8.45 MB. Further reductions for the
OpenWrt package are part of the OpenWrt stage.
