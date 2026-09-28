# 0005. Detecting the macvlan parent/child trap

## Context

A host cannot reach macvlan children that sit on its own parent interface. The host itself does
not see the child once the child is moved to another network namespace.

## Decision

The agent reports the netlink link kind (`IFLA_INFO_KIND`) of each interface. The server reports
a `macvlan` problem for a pair A→B when:

- ICMP and TCP fail in both directions between A and B within the segment;
- exactly one side (B) is a `macvlan` interface;
- both A and B are reachable from other members of the same segment.

The problem is reported once, from the host (non-macvlan) side, with the recommendation to create
a macvlan sibling on the host parent interface and move the host address there.

## Consequences

Generic "unreachable" problems are suppressed for such pairs. If both sides are macvlan children
of different hosts, the generic rule applies.
