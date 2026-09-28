# 0002. Project namespace: retreat-community

## Context

The repository lives at `github.com/retreat-community/lanscape`. Images, the Helm chart and the
OpenWrt feed are published from GitHub Actions of this repository with `GITHUB_TOKEN`, which can
only push to the owner's namespace.

## Decision

- Go module: `github.com/retreat-community/lanscape`.
- Images: `ghcr.io/retreat-community/{lanscape,lanscape-agent,lsm-agent,lsm-server}`.
- Helm chart: `oci://ghcr.io/retreat-community/charts/lanscape`.
- OpenWrt feed: `https://retreat-community.github.io/lanscape/openwrt/<branch>/<arch>/`.

## Consequences

Publishing works without extra secrets. Moving the project to another owner needs a search and
replace of the namespace (`retreat-community`) in `go.mod`, workflows, charts and docs.
