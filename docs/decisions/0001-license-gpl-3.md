# 0001. License: GPL-3.0-or-later

## Context

The project was initially planned under the MIT license, and the specification lists "MIT or
Apache-2.0". The repository owner decided to publish Lanscape under the GNU General Public
License v3.0.

## Decision

All code in this repository is licensed under GPL-3.0-or-later. `LICENSE` holds the full GPL v3
text, `NOTICE` states the copyright and lists third-party components.

## Consequences

- Distributors of modified binaries (firmware images, appliances) must provide the source.
- Third-party dependencies must be GPL-3.0 compatible (MIT, BSD, Apache-2.0, MPL-2.0, ISC are).
- Packaging metadata (deb/rpm/apk, OpenWrt `PKG_LICENSE`, Helm chart, OCI labels) uses
  `GPL-3.0-or-later`.
