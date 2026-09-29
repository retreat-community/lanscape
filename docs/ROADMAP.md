# Roadmap

Ideas for after 1.0 (SPEC §19). They are not part of the current scope; contributions and
discussion are welcome.

- **Wi-Fi map**: clients per access point, signal level and negotiated rate, "weak" clients.
- **Traffic analysis**: top talkers from NetFlow/sFlow/conntrack on the router — who saturated the
  link at a given moment.
- **Isolation check**: a "who must and must not reach whom" matrix between segments (the IoT VLAN
  must not reach the NAS), with a regular automatic test of the firewall rules.
- **Configuration snapshots**: periodic backups of router, switch and hypervisor configurations,
  with diffs in the change feed.
- **Documentation from data**: a generated "how the network is built" page (segments, addresses,
  services) as Markdown or PDF.
- **IP planner**: suggest a free address and VLAN when adding a device, and check for conflicts
  before the change is applied.
- **What-if scenarios**: switch off a node or a link on the map and see which services are
  affected, using the dependency graph.
- **Outside view**: a cloud agent checks public addresses, ports open to the Internet, DNS records
  and certificates.
- **Energy and quiet**: shutdown schedules and Wake-on-LAN for rarely used machines, power
  accounting.
- **Mobile app and OS widgets** with status and push notifications.
- **Plugins**: an SDK for custom discovery sources, monitors and widgets.
