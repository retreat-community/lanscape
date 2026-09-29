# 0007. CI on self-hosted Docker-in-Docker runners

## Context

The repository is private and GitHub-hosted runners are not available to it. CI runs on the
self-hosted runner scale set `retreat-k8s` (Actions Runner Controller in Kubernetes, Docker-in-Docker
mode). The runner container itself is unprivileged; Docker is provided by a privileged `dind`
sidecar. The kernel module `8021q` may be missing on the nodes.

## Decision

- All jobs use `runs-on: retreat-k8s`.
- Go, Mini, OpenAPI and web jobs run in job containers (`golang:1.23-bookworm`, `debian:bookworm`,
  `node:22-bookworm`) so they do not depend on the runner image.
- The e2e suite (network namespaces, VLANs, `tc`, `iptables`, Playwright) runs in one
  `docker run --privileged` container based on the Playwright image (`test/e2e/ci.sh`).
- `topo.sh` checks whether VLAN devices can be created. Without `8021q` it builds the same
  topology with separate bridges per segment and writes `out/novlan`. The e2e tests then skip only
  the VLAN-ID assertion; every other check (paths, speeds, TPROXY, MTU, macvlan, counters) runs.
- Tests for macOS, Windows and FreeBSD are compiled and vetted on Linux (`GOOS=... go test -c`).
  The job `test-go-hosted` runs them natively on `macos-latest`/`windows-latest` when the
  repository variable `HOSTED_RUNNERS` is `true`.

## Consequences

CI does not need hosted runners. The VLAN-ID check is only as strong as the node kernel allows;
loading `8021q` on the runner nodes enables it automatically.
