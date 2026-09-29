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
